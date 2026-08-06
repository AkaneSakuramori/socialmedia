#!/usr/bin/env python3
"""Smoke test for the InChat api-server (DEVOPS.md §8 smoke).

Probes /healthz (liveness) and /readyz (readiness) and exits non-zero if the
server is not healthy. Run with the project venv:  venv/bin/python scripts/smoke.py
"""
import json
import os
import sys
import urllib.error
import urllib.request

BASE_URL = os.environ.get("SMOKE_BASE_URL", "http://localhost:8080")


def get(path: str) -> tuple[int, dict]:
    req = urllib.request.Request(BASE_URL + path, headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"{}")


def main() -> int:
    failures = []

    status, body = get("/healthz")
    ok = status == 200 and body.get("status") == "ok"
    print(f"[healthz] {status} {body}")
    if not ok:
        failures.append("healthz")

    status, body = get("/readyz")
    ok = status == 200 and body.get("status") == "ready"
    print(f"[readyz ] {status} {body}")
    if not ok:
        failures.append("readyz")

    if failures:
        print(f"SMOKE FAILED: {', '.join(failures)}", file=sys.stderr)
        return 1
    print("SMOKE OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
