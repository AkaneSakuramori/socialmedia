# Messaging Platform — Security & Cryptography Specification

| | |
|---|---|
| **Document** | Security & Cryptography Specification v1.0 |
| **Kind** | Normative specification (requirements + conformance), companion to the Security Handbook |
| **Audience** | Security engineers, all feature engineers, QA (conformance testing), auditors |
| **Source of Truth** | `ARCHITECTURE.md` §10–§12, §30 · `SECURITY.md` · `DEVOPS.md` · `API.md` → this specification. Do not redesign. |
| **Stack (fixed)** | Go · Flutter · PostgreSQL · Redis · Docker · Terraform · Cloudflare |
| **Launch** | India first (single region) → global scale later |
| **Conformance** | All `MUST` clauses are mandatory. `SHOULD` clauses are mandatory unless a documented, risk-accepted exception exists. |

> This document **specifies** the security requirements of the platform in testable form. It restates no product decisions; it translates the security architecture (`ARCHITECTURE.md` §30), the security handbook (`SECURITY.md`), and the DevOps security controls (`DEVOPS.md`) into numbered, conformance-verifiable requirements. Every requirement carries a rationale grounded in how WhatsApp, Signal, Telegram, iMessage, and Discord engineer security, and in modern cloud security standards (OWASP, NIST, DPDP, GDPR, SOC 2). Where this specification and a source-of-truth document conflict, the source-of-truth document wins and the conflict is raised as a PR.

**Conventions:** `MUST`/`SHALL`, `SHOULD`, `MAY` follow RFC 2119. Requirement IDs are stable and referenced by the conformance matrix (Appendix A) and the QA handbook's security test suite.

---

## Table of Contents

1. [Scope, Normative References & Conformance](#1-scope-normative-references--conformance)
2. [Authentication Security](#2-authentication-security)
3. [Authorization Model](#3-authorization-model)
4. [Session Security](#4-session-security)
5. [JWT Strategy](#5-jwt-strategy)
6. [Refresh Tokens](#6-refresh-tokens)
7. [Device Management](#7-device-management)
8. [Password Security](#8-password-security)
9. [OTP Security](#9-otp-security)
10. [API Security](#10-api-security)
11. [WebSocket Security](#11-websocket-security)
12. [Rate Limiting](#12-rate-limiting)
13. [DDoS Protection](#13-ddos-protection)
14. [Encryption at Rest](#14-encryption-at-rest)
15. [Encryption in Transit](#15-encryption-in-transit)
16. [Key Management](#16-key-management)
17. [Secure Media Storage](#17-secure-media-storage)
18. [File Validation](#18-file-validation)
19. [Malware Protection](#19-malware-protection)
20. [Privacy Protections](#20-privacy-protections)
21. [Abuse Prevention](#21-abuse-prevention)
22. [Spam Detection](#22-spam-detection)
23. [Anti-Bot Strategy](#23-anti-bot-strategy)
24. [Secrets Management](#24-secrets-management)
25. [Audit Logging](#25-audit-logging)
26. [Security Monitoring](#26-security-monitoring)
27. [Incident Response](#27-incident-response)
28. [Data Retention](#28-data-retention)
29. [Account Recovery](#29-account-recovery)
30. [Backup Security](#30-backup-security)
31. [Future End-to-End Encryption Architecture](#31-future-end-to-end-encryption-architecture)
32. [Security Roadmap](#32-security-roadmap)
33. [Appendix A — Conformance Matrix](#appendix-a--conformance-matrix)
34. [Appendix B — Normative References & Industry Baselines](#appendix-b--normative-references--industry-baselines)

---

## 1. Scope, Normative References & Conformance

### 1.1 Scope

This specification covers the security and cryptographic requirements of the platform across all trust boundaries: the Flutter client, the Cloudflare edge, the origin services (API, WS, workers), the data tier (PostgreSQL, Redis, media volumes), and the release/supply chain. It covers current controls and the future end-to-end encryption (E2EE) architecture.

### 1.2 Normative references (normative)

- `ARCHITECTURE.md` §10 (Authentication), §11 (Sessions), §12 (Authorization), §30 (Security Architecture)
- `SECURITY.md` (Security & Cryptography Handbook — the operating model this specification makes testable)
- `DEVOPS.md` §7, §16–§22 (secrets, TLS, Cloudflare, hardening)
- `API.md` §2–§4, Appendix A/B (API conventions, auth flows, rate tiers, error catalog)
- `FLUTTER.md` §23 (client security), `DATABASE.md` §4 (credentials/sessions), §18 (retention)

### 1.3 Conformance rules

- **CONF-1:** A release is security-conformant only if every `MUST` requirement in scope is implemented and verified by the QA security suite (`QA.md` §14) or its designated control owner.
- **CONF-2:** `SHOULD` requirements are waived only via a documented, risk-accepted exception owned by the security lead, reviewed at each release.
- **CONF-3:** Requirement IDs are immutable once published; revisions add new IDs, never renumber.
- **CONF-4:** The conformance matrix (Appendix A) is the single index of requirement → control → verification → owner.

**Why these conventions exist:** a security document that cannot be verified is a wish list. WhatsApp, Signal, and the OWASP/cloud frameworks all ground their claims in testable controls; numbered, conformance-checked requirements are how this platform's security posture becomes auditable evidence (which SOC 2 and DPDP/GDPR due diligence will ask for).

---

## 2. Authentication Security

Requirements governing *proving who the user is* (`ARCHITECTURE.md` §10, `API.md` §4).

- **AUTH-1 (MUST):** The platform SHALL support passkey (WebAuthn) authentication as a first-class method. WebAuthn challenges SHALL be single-use with a short TTL stored in Redis; the credential public key SHALL be stored per user (`user_credentials.credential_data`, `DATABASE.md` §4.3) and verified at `finish`.
- **AUTH-2 (MUST):** OTP SHALL be a supported convenience fallback, stored **hashed** with a 300 s TTL in Redis, single-use, consumed atomically, and **never echoed** in any response or log.
- **AUTH-3 (MUST):** Password authentication SHALL be supported and verified against hashed credentials (policy in §8).
- **AUTH-4 (SHOULD):** OAuth linking (Google/Apple) MAY be offered, but SHALL link a secondary identity to the primary identity — it SHALL NOT replace the primary-identity verification path.
- **AUTH-5 (MUST):** Failed-login throttling SHALL be enforced per identifier with lockout backoff — 5 consecutive failures → 5-minute lockout (`423 LOCKED`), on **all** credential methods, not only password.
- **AUTH-6 (MUST):** Credential comparison SHALL be constant-time; error messages SHALL NOT reveal which component of a credential was wrong.
- **AUTH-7 (MUST):** Every authentication event (register, otp_sent, login, token_refresh, logout_all) SHALL be written to the audit log.
- **AUTH-8 (MUST):** A suspended account SHALL NOT authenticate through any method; `ACCOUNT_SUSPENDED` SHALL return uniformly and existing sessions SHALL be revoked.
- **AUTH-9 (MUST):** Sensitive actions (e.g., security-sensitive settings changes) SHALL require step-up re-confirmation (OTP/passkey or a re-confirmation token), per the sensitive-action flows in `API.md`.
- **AUTH-10 (MUST):** Registration and OTP-send endpoints SHALL sit on the strictest unauthenticated rate tier, keyed per IP + device, and be fronted by human-verification (§23).
- **AUTH-11 (SHOULD):** Device-integrity signals (Play Integrity / App Attestation) SHALL feed risk-based step-up escalation: new device, unfamiliar geography, or token-family reuse escalates authentication rather than relying on blanket rules.

**Why:** WhatsApp's security model (passkeys, two-step verification, account-security alerts) and OWASP's authentication guidance converge on the same hierarchy: strongest credential first (passkeys resist phishing), OTP as a throttled fallback (SMS is outside our control — SIM-swap and code-sharing scams are the #1 takeover path), and lockout to make credential stuffing uneconomic. AUTH-9 exists because the platform already models step-up (re-confirmation) — this specifies it as a requirement, not a nicety.

---

## 3. Authorization Model

Requirements governing *what an authenticated user may do* (`ARCHITECTURE.md` §12).

- **AUTHZ-1 (MUST):** Authorization SHALL be resource-based, evaluated at the resource module boundary, and SHALL NOT be satisfied by successful authentication alone.
- **AUTHZ-2 (MUST):** Conversation read/write SHALL require active membership, checked via the membership cache and refreshed on membership events — enforced on the REST path, the WS subscribe path, and media serve time.
- **AUTHZ-3 (MUST):** Group role hierarchy SHALL be Owner > Admin > Member; admin actions (add/remove members, edit group info, promote/demote) SHALL require Admin or Owner; destructive actions (delete group, transfer ownership) SHALL require Owner.
- **AUTHZ-4 (MUST):** Message edit/delete-for-all SHALL require message ownership or Admin capability; delete-for-self SHALL always be permitted.
- **AUTHZ-5 (MUST):** Media download SHALL require membership in the originating conversation **at serve time**; signed URLs SHALL embed the requester identity and expiry, and the handler SHALL re-check membership before streaming.
- **AUTHZ-6 (MUST):** Presence privacy SHALL follow per-user tiers (everyone → contacts → nobody); reads outside the tier SHALL be denied.
- **AUTHZ-7 (MUST):** Search SHALL be scoped to the caller's conversations and memberships — never global.
- **AUTHZ-8 (MUST):** PostgreSQL Row-Level Security (RLS) SHALL be applied as a safety net on the most sensitive aggregates so that even a query-level bug cannot expose another user's rows.
- **AUTHZ-9 (MUST):** Database roles SHALL be least-privilege and distinct per function (API, workers, migrations, media); the media role SHALL have no write access to user tables.
- **AUTHZ-10 (MUST):** Authorization **denials** SHALL be audited and alerted as a security signal; denial SHALL be the default on uncertainty (fail closed).
- **AUTHZ-11 (MUST):** The admin plane SHALL use separate authentication (SSO), mTLS at the network layer, an IP allowlist, and per-action audit.

**Why:** the industry lesson from every messaging platform — WhatsApp, Telegram, Discord — is that authorization bugs are silent and catastrophic: an unenforced membership check exposes private conversations. This model makes membership the unit of authorization everywhere (API, realtime, media), adds RLS as a defense-in-depth safety net, and treats denials as telemetry, because "who was blocked from what" is itself an attack signal. Discord's server-role model is the closest analogue to the Owner/Admin/Member hierarchy; enterprise frameworks (SOC 2, least-privilege) drive AUTHZ-9/AUTHZ-11.

---

## 4. Session Security

Requirements governing the durable link between a user and their devices (`ARCHITECTURE.md` §11).

- **SESS-1 (MUST):** PostgreSQL SHALL be the source of truth for sessions; Redis SHALL hold only a hot subset for gateways and presence.
- **SESS-2 (MUST):** Every session SHALL record the validated device identifier, platform, app version, push token, last-active time, IP/geo, and token-family version.
- **SESS-3 (MUST):** A session's identity SHALL come from the token, never from a request-body field; a caller SHALL only be able to revoke its own sessions.
- **SESS-4 (MUST):** Session lifecycle SHALL be: Active → (refresh) Active; → Revoked on sign-out/admin/theft; → Expired on sliding idle timeout; → Suspended on security event or password change (other sessions suspended, current kept).
- **SESS-5 (MUST):** Revocation SHALL be immediate and complete: revoke the session row, blacklist the access-token `jti` in Redis for its remaining TTL, force-close the bound WS connection (pub/sub `sessions:revoke:{sessionId}`), clear the push token, and publish `SessionRevoked` so other devices react.
- **SESS-6 (MUST):** Logout-all SHALL bump a global token version invalidating all outstanding access tokens at the gateways, revoke all sessions, close all WS connections, and clear push tokens.
- **SESS-7 (MUST):** An admin kill-switch SHALL reuse the session↔connection binding to cut a user or device instantly.
- **SESS-8 (MUST):** Users SHALL have a device-management surface (list sessions, revoke any own session) that surfaces `last_active_at` and device metadata.
- **SESS-9 (SHOULD):** Sessions SHALL expire on sliding idle timeout and require re-authentication after expiry.

**Why:** WhatsApp's linked-device model shows the double-edged nature of sessions — they are what make multi-device work and what attackers persist through. The session registry + revocation trinity (row + blacklist + socket close) exists because a revoked token is worthless if the WS connection stays alive, and a forgotten session is a persistence path (WhatsApp recovery guidance explicitly targets unknown linked devices). The global token version (SESS-6) is the escape hatch that makes "sign out everywhere" actually mean it.

---

## 5. JWT Strategy

Requirements governing the short-lived access token (`ARCHITECTURE.md` §10.2, `API.md` §2.3, §4.9).

- **JWT-1 (MUST):** Access tokens SHALL be signed with Ed25519 (primary) or RS256; public keys SHALL be exposed read-only via `GET /auth/jwks` and cached by verifiers with standard cache-control.
- **JWT-2 (MUST):** Claims SHALL include `userId`, `sessionId`, `deviceId`, scopes, issuer, audience, and `jti`. Claims SHALL NOT contain secrets or PII beyond the user id.
- **JWT-3 (MUST):** Access-token TTL SHALL be 15–30 minutes (API default: 15 min, `expires_in: 900`).
- **JWT-4 (MUST):** Verification SHALL pin the allowed algorithm set and reject the `none` algorithm outright; any "alg confusion" vector SHALL be treated as a release blocker.
- **JWT-5 (MUST):** Every gateway SHALL validate signature (JWKS), issuer, audience, expiry, `jti` blacklist status, and the session's global token version before granting access.
- **JWT-6 (MUST):** On revocation, the `jti` SHALL be blacklisted in Redis for the remainder of the token TTL.
- **JWT-7 (MUST):** Signing keys SHALL rotate on schedule with a JWKS multi-key scheme: add the new key first, retire the old key only after the maximum token TTL has passed.
- **JWT-8 (SHOULD):** A live key-rotation rehearsal SHALL be part of the operating rhythm.

**Why:** OWASP's 2026 JWT guidance is unambiguous: allowlist algorithms, reject `none`, keep TTLs short, and pin claims. Short TTLs are what make the Redis `jti` blacklist tractable and what make the "validate once per WS connection" model safe — the design tension (a stolen token is a valid credential) is bounded by TTL + blacklist + device binding + refresh rotation. Signal/WhatsApp do not use JWTs (different architecture), but the short-lived-token discipline transfers directly from every OAuth/JWT service at scale (Microsoft, Google, Auth0).

---

## 6. Refresh Tokens

Requirements governing long-lived session renewal (`ARCHITECTURE.md` §10.2, `API.md` §4.4).

- **REFR-1 (MUST):** Refresh tokens SHALL be opaque, high-entropy random values — not JWTs, never parsed.
- **REFR-2 (MUST):** Refresh tokens SHALL be stored only as a hash in PostgreSQL alongside the session; the plaintext exists only on the client and in the refresh request.
- **REFR-3 (MUST):** Refresh tokens SHALL travel only in the `X-Refresh-Token` header, never in bodies or logs.
- **REFR-4 (MUST):** Refresh tokens SHALL rotate on every use; each refresh SHALL issue a new refresh token and invalidate the old one (single-use).
- **REFR-5 (MUST):** Reuse detection SHALL be implemented: presenting a rotated-out token returns `410` and SHALL revoke **all** sessions for the user and force re-login (theft response).
- **REFR-6 (MUST):** Refresh-token TTL SHALL be 30–90 days, extended by a sliding window on active use; idle sessions expire.
- **REFR-7 (MUST):** The refresh endpoint SHALL be the only consumer of refresh tokens; the client SHALL use the access token only for its TTL.
- **REFR-8 (SHOULD):** Refresh SHALL re-establish the WS session binding so a dropped socket reconnects without full re-login.

**Why:** rotation-with-reuse-detection is the single most effective defense against long-lived-session theft — it converts "a stolen refresh token" into "a detected theft that revokes the family." WhatsApp-style session security and OWASP refresh-token guidance both converge here. REFR-5's global revocation is deliberately aggressive: the cost of a forced re-login is trivial next to the cost of an active hijack.

---

## 7. Device Management

Requirements governing the device surface (`ARCHITECTURE.md` §11.1, `API.md` §4.7–§4.8, `FLUTTER.md` §23).

- **DEVM-1 (MUST):** Each session SHALL be bound to a validated device identifier that matches `user_sessions.device_id`; requests carrying a mismatched device SHALL be rejected.
- **DEVM-2 (MUST):** Users SHALL be able to list and revoke any of their own device sessions (`GET/DELETE /auth/sessions`).
- **DEVM-3 (SHOULD):** Device integrity SHALL be attested via Play Integrity (Android) and App Attestation (iOS) and SHALL feed risk scoring (new-device escalation, step-up) — as a signal, never as a bypass.
- **DEVM-4 (SHOULD):** A login from a new device or unfamiliar geography SHALL be treated as a risk event: notified, audited, and eligible for step-up escalation.
- **DEVM-5 (MUST):** Push tokens SHALL be per-device, registered on session creation, and cleared on session revocation.
- **DEVM-6 (MUST):** The client SHALL store tokens in `flutter_secure_storage` (Keychain/Keystore), purge all tokens and local state on logout/logout-all/session-revoked, and encrypt its local DB with SQLCipher (key in secure storage).
- **DEVM-7 (SHOULD):** Root/JB detection SHALL be informational (report, not block), and SHALL never replace secure storage.

**Why:** WhatsApp's "linked devices" surface and recovery guidance make the device the unit of account security — most takeovers persist through an unrecognized linked session. DEVM-1 binds tokens to devices so a token can't be replayed from a different device; DEVM-3/4 turn the device into a risk input (the industry's device-trust pattern used by iMessage and modern authenticator apps); DEVM-6 is the client-side floor that makes a stolen phone not an automatic data breach.

---

## 8. Password Security

Requirements governing password credentials (`API.md` §4.1, `ARCHITECTURE.md` §10.1).

- **PASS-1 (MUST):** Passwords SHALL be stored using a memory-hard KDF (Argon2id preferred, or bcrypt at an appropriate cost), salted uniquely per user; SHA-family, unsalted, or reversible schemes SHALL NOT be used.
- **PASS-2 (MUST):** Minimum password length SHALL be 8 characters at registration; identifier-matching and trivially-weak values SHALL be rejected.
- **PASS-3 (SHOULD):** Scheduled password rotation SHALL NOT be required; re-authentication and a change prompt SHALL be forced on security events instead.
- **PASS-4 (MUST):** A password change SHALL suspend all other sessions (keep the current one), and SHALL be audited as a security event.
- **PASS-5 (MUST):** Raw passwords SHALL never be logged, stored, transmitted outside TLS, or derivable; the password field SHALL exist only in request bodies over TLS.
- **PASS-6 (MUST):** All credential methods SHALL share the same lockout/session machinery so the weakest method does not weaken the strongest.

**Why:** NIST SP 800-63B and OWASP both move away from forced rotation (it produces weaker passwords) toward length floors, breach awareness, and event-driven re-auth. The memory-hard KDF is non-negotiable because GPU/ASIC cracking economics make SHA-family hashing cheap. PASS-4 exists because a password change is a *takeover-response* action — WhatsApp-style account-security events treat it as such.

---

## 9. OTP Security

Requirements governing the OTP fallback (`API.md` §4.2–§4.3, `ARCHITECTURE.md` §10.1).

- **OTP-1 (MUST):** OTPs SHALL be 6 digits, single-use, with a 300 s TTL, stored hashed in Redis, and consumed atomically on verification.
- **OTP-2 (MUST):** OTP endpoints SHALL never echo the code; responses SHALL return only `expires_in` and `resend_after`.
- **OTP-3 (MUST):** OTP-send SHALL throttle per identifier (30 s cooldown, max 5/hour) and per IP + device; OTP-verify SHALL share the login lockout (§2).
- **OTP-4 (MUST):** The sign-up/OTP flow SHALL be fronted by human verification (Turnstile, §23) and the strictest unauth rate tier.
- **OTP-5 (SHOULD):** SMS/email delivery SHALL be treated as a known-weak link (SIM swap, carrier interception, code-sharing scams) — OTP SHALL be positioned as a fallback, with passkeys as the recommended path.
- **OTP-6 (SHOULD):** Unexpected OTP-send volumes SHALL alert as a possible attack (code-bombing / verification-code scams).

**Why:** WhatsApp's most common takeover is a shared verification code; SIM swap is the carrier-level variant. The requirements bound exactly that risk: short TTL, single-use, throttled, hashed, never echoed, and human-verified at the entry — while the product steer toward passkeys (WhatsApp's own direction) reduces reliance on the weakest link. OTP-6 turns "codes you didn't request" (WhatsApp's own guidance) into a monitored signal.

---

## 10. API Security

Requirements governing the REST surface (`API.md` §2, `ARCHITECTURE.md` §30).

- **API-1 (MUST):** Access tokens SHALL be carried in the `Authorization` header, never cookies — eliminating classic CSRF; any cookie-based flow (admin console) SHALL enforce SameSite + Origin checks + CSRF tokens.
- **API-2 (MUST):** Input SHALL be validated at the boundary: types, lengths, charsets, identifier normalization (E.164 / lowercase email), reserved-word checks — before any business logic, with stable machine-readable errors.
- **API-3 (MUST):** Unsafe writes SHALL require an `Idempotency-Key`, stored hashed with the response for 24 h; validation failures SHALL NOT be cached; message send SHALL additionally use `client_msg_id` as the durable DB-layer dedupe.
- **API-4 (MUST):** Media SHALL be served only via short-lived signed URLs embedding `mediaId`, expiry, and a per-user signature.
- **API-5 (MUST):** Error responses SHALL use the problem+json catalog with a stable `code`; blocked targets SHALL return opaque `404`; stack traces and internal identifiers SHALL NOT be exposed.
- **API-6 (MUST):** Breaking API/auth changes SHALL require `/v2` with a migration window; additive changes stay in `/v1`. Security fixes SHALL NOT wait for a version bump.
- **API-7 (MUST):** The reverse proxy SHALL sanitize `X-Forwarded-*`/`CF-*` headers so clients cannot spoof origin-derived identity.
- **API-8 (MUST):** Responses and errors SHALL NOT contain secrets, tokens, or PII beyond the request's own data.

**Why:** OWASP API Security Top 10 and Stripe-style idempotency are the baselines: header-carried bearer tokens remove the entire CSRF class; boundary validation is the entry to injection defense; idempotency is both a reliability and an anti-replay control; signed URLs bound media exposure; and a safe error catalog stops information disclosure (the failure mode that turns bugs into exploits).

---

## 11. WebSocket Security

Requirements governing the realtime surface (`API.md` §16, `ARCHITECTURE.md` §10.4, §11.3).

- **WS-1 (MUST):** The WS handshake SHALL authenticate with the access token and SHALL be validated **before** the connection is upgraded; protocol version SHALL be negotiated via `sec-websocket-protocol: chat.v1`.
- **WS-2 (MUST):** The gateway SHALL validate the token once per connection and SHALL force-close the connection when the bound session is revoked (pub/sub `sessions:revoke:{sessionId}`).
- **WS-3 (MUST):** WS frames SHALL be throttled separately from HTTP (typing/presence/read budgets per `API.md` Appendix B) to prevent frame flooding.
- **WS-4 (MUST):** A client SHALL only subscribe to conversations it is an active member of; unauthorized subscribes SHALL be denied and audited (same authz as REST).
- **WS-5 (MUST):** Transport SHALL be `wss://` only, with the same TLS policy as REST; no plaintext upgrade.
- **WS-6 (MUST):** WS SHALL be proxied through the edge with proxied DNS records; the origin SHALL NOT accept direct public WS.
- **WS-7 (MUST):** The client SHALL verify `hello_ack` identity matches the logged-in user before trusting inbound events, and SHALL tear down the socket on `session.revoked`.
- **WS-8 (SHOULD):** Server-initiated close (`1012`) on shutdown SHALL be used so clients reconnect elsewhere gracefully.

**Why:** realtime surfaces are the least-tested and most-abused attack path in messaging (Discord and Telegram both harden their gateways specifically against connect floods and frame spam). Authenticating before upgrade, authorizing every subscription, throttling per frame, and force-closing on revocation close the loop that a token-check-at-connect-only model would leave open: a revoked session cannot hold an open socket, and a non-member cannot subscribe to a conversation.

---

## 12. Rate Limiting

Requirements governing per-identity and per-path limits (`API.md` §2.8, Appendix B).

- **RATE-1 (MUST):** Rate limiting SHALL be identity-based (per `user_id` + `device_id`) with IP fallback for unauthenticated endpoints, token-bucket, enforced at the Go gateway.
- **RATE-2 (MUST):** The tiers in `API.md` Appendix B SHALL be enforced: `auth_anon`, `login` (with lockout), `standard`, `search`, `upload_slots`, `upload_bytes`, `contacts_sync`, `snapshot`, `create_caps`, WS frame budgets, `admin`.
- **RATE-3 (MUST):** Every response SHALL carry `X-RateLimit-Limit/-Remaining/-Reset`; `429` SHALL include `Retry-After` and `code=RATE_LIMITED`.
- **RATE-4 (MUST):** WS frame budgets SHALL be enforced independently of HTTP.
- **RATE-5 (MUST):** Media uploads SHALL have concurrent-slot and byte budgets per user.
- **RATE-6 (SHOULD):** Raising any tier SHALL require a security review (tiers are anti-abuse controls, not knobs).
- **RATE-7 (MUST):** Edge (Cloudflare) and origin (gateway) limits SHALL both exist; the origin SHALL NOT assume the edge ran.

**Why:** rate limiting is the economic defense — it makes mass enumeration, credential stuffing, and abuse unprofitable at the layer the app can see. The two-layer design (edge for floods, origin for identity economics) is the cloud-standard pattern (Cloudflare + origin), and RATE-6 protects the control from being tuned away by a feature that "needs more QPS."

---

## 13. DDoS Protection

Requirements governing availability under attack (`DEVOPS.md` §16, §18).

- **DDoS-1 (MUST):** The platform SHALL be behind Cloudflare with WAF, managed DDoS protection, and bot management always on.
- **DDoS-2 (MUST):** The origin SHALL be locked to Cloudflare IP ranges or a Cloudflare Tunnel (no public origin IP); origin-facing DDoS bypass is the classic failure to prevent.
- **DDoS-3 (MUST):** Edge rate limiting SHALL protect auth and search paths as the coarse defense layer.
- **DDoS-4 (SHOULD):** Capacity headroom and autoscaling SHALL be sized from load tests against the projected maximum (§12 of `QA.md`), so a flood is absorbed, not merely absorbed.
- **DDoS-5 (SHOULD):** Edge attack telemetry (Cloudflare Analytics/Logpush) SHALL feed Loki/SIEM and alert on attack signatures.
- **DDoS-6 (MUST):** Stress tests SHALL record the ceiling and the failure mode (graceful degradation vs. hard failure) per `QA.md` §13.

**Why:** messaging platforms are prime DDoS targets (they must accept connections from the whole internet and push to all of them). The industry answer — WhatsApp/Telegram-class edge absorption via CDN/WAF with a hidden origin — is exactly DDoS-1/2: the edge absorbs the flood, the origin is unreachable directly. DDoS-4/6 tie availability claims to tested capacity rather than assumption.

---

## 14. Encryption at Rest

Requirements governing stored data (`ARCHITECTURE.md` §30.1, `SECURITY.md` §19, `DEVOPS.md` §22).

- **ATR-1 (MUST):** All volumes SHALL be encrypted at rest (LUKS on self-managed hosts, managed disk encryption in cloud).
- **ATR-2 (MUST):** PostgreSQL encryption at rest SHALL be enabled; client connections SHALL use TLS; `pg_hba` SHALL restrict to the app network.
- **ATR-3 (MUST):** Backups SHALL be encrypted, with keys separate from data keys, and stored off-machine.
- **ATR-4 (MUST):** The client's local message DB SHALL be encrypted with SQLCipher, key in secure storage; sensitive media SHALL use the OS file-protection level and never shared/public storage.
- **ATR-5 (MUST):** Data-encryption keys, backup-encryption keys, and signing keys SHALL be distinct, with distinct rotation.
- **ATR-6 (SHOULD):** Redis SHALL not store business data that would constitute loss if exposed; it SHALL be non-public with `requirepass` + ACLs, and TLS where supported.

**Why:** "assume the disk is lost" is the operating assumption — WhatsApp ships encrypted backups and Signal encrypts local state because a stolen device or leaked backup is otherwise a data breach. CIS/cloud standards (SOC 2, AWS/Google/Azure shared-responsibility) all mandate at-rest encryption; key separation (ATR-5) ensures one compromise (e.g., a backup key) doesn't decrypt everything else.

---

## 15. Encryption in Transit

Requirements governing data in motion (`DEVOPS.md` §17, `SECURITY.md` §19, §23).

- **ITR-1 (MUST):** TLS 1.2+ SHALL be enforced, with TLS 1.3 preferred, at edge and origin; obsolete protocols/ciphers SHALL be disabled.
- **ITR-2 (MUST):** Cloudflare SHALL operate in Full (Strict) mode so the origin presents a real CA cert and the edge verifies origin identity.
- **ITR-3 (MUST):** HSTS (`includeSubDomains`) SHALL be set at the edge; the API and app SHALL be HTTPS-only.
- **ITR-4 (MUST):** WebSocket SHALL use `wss://` under the same TLS policy as REST.
- **ITR-5 (SHOULD):** Certificate pinning SHALL be opt-in/config-driven for API/WS hosts in prod builds, with a rotation mechanism so pinned-cert expiry is not an outage; disabled in debug builds.
- **ITR-6 (SHOULD):** Service-to-service communication within the origin SHALL use TLS.
- **ITR-7 (MUST):** Certificate lifecycle SHALL be automated; expiry alerts SHALL be pages and tested.

**Why:** transport is the one layer every other control assumes. OWASP and Google/Microsoft transport guidance all mandate modern TLS + HSTS; Full (Strict) closes the "edge trusts origin by IP only" hole; pinning is deliberately opt-in because WhatsApp/Telegram-class apps treat pinning as a phishing-resistance *and* availability trade-off (a pinned-cert expiry must never be an outage).

---

## 16. Key Management

Requirements governing key lifecycle (`SECURITY.md` §20, `ARCHITECTURE.md` §30).

- **KEY-1 (MUST):** All server-side keys SHALL be held in a managed vault/KMS, never in source, images, or Terraform state.
- **KEY-2 (MUST):** Key classes SHALL be separated: access-token signing, media signing, data encryption, backup encryption, push (FCM/APNs), Cloudflare API — with distinct rotation.
- **KEY-3 (MUST):** Every key class SHALL have a documented lifecycle (generate → distribute → use → rotate → destroy) and a rehearsed rotation runbook in the vault.
- **KEY-4 (MUST):** Key access SHALL be audited; break-glass access SHALL be time-boxed and flagged.
- **KEY-5 (MUST):** Access-token signing keys SHALL rotate on the JWKS add-before-retire scheme (§5).
- **KEY-6 (MUST):** The media-signing secret SHALL be distinct from token signing and rotated with a grace window bounded by URL expiry.
- **KEY-7 (MUST):** Client-side keys (SQLCipher DB key, secure-storage contents) SHALL live in OS secure enclaves (Keychain/Keystore) and SHALL never be transmitted server-side.
- **KEY-8 (SHOULD):** Key rotation drills SHALL run on the operating cadence (quarterly), not only on incident.

**Why:** NIST SP 800-57 and cloud KMS best practice agree: keys are the crown jewels, separation limits blast radius, and rotation is the "assume breach" control — a key that never rotates is a key that, once stolen, works forever. WhatsApp's encrypted-backup key (client-held, 64-digit) is the client-side exemplar of KEY-7: the platform cannot decrypt what it never holds.

---

## 17. Secure Media Storage

Requirements governing media at rest and at serve time (`ARCHITECTURE.md` §19, §30.3, `API.md` §9).

- **MED-1 (MUST):** Media SHALL be stored via the `storage` abstraction with keys structured by type/date; storage SHALL be non-public and origin-locked.
- **MED-2 (MUST):** Media SHALL be served only via short-lived signed URLs embedding `mediaId`, expiry, and a per-user signature; downloads SHALL redirect (`302`) to the signed CDN URL so origin storage keys stay private.
- **MED-3 (MUST):** Serve time SHALL re-check membership in the originating conversation — a leaked URL is unusable after expiry or membership removal.
- **MED-4 (MUST):** Uploads that fail validation or scanning SHALL be quarantined (`tmp/quarantine`) and never issued a shareable signed URL.
- **MED-5 (MUST):** Media SHALL carry state + retention dates; cleanup workers SHALL purge staged, deleted, orphan, and expired-retention objects.
- **MED-6 (MUST):** The media database role SHALL have no write access to user tables.
- **MED-7 (SHOULD):** Backups and the future S3/R2 migration SHALL go through the `storage` interface so signing/quarantine/membership security is backend-independent.

**Why:** Discord and WhatsApp both treat media as a distinct security class — user-controlled binaries at scale that are the #1 vector for malware, abuse, and storage-exhaustion attacks. Signed, per-requester, membership-rechecked URLs are the cloud-standard defense (AWS S3 presigned-URL practice) that converts a leaked URL from a data breach into a useless string; quarantine + retention bound the abuse surface.

---

---

## 18. File Validation

Requirements governing uploads before they become shareable (`API.md` §9, `SECURITY.md` §13).

- **FILE-1 (MUST):** Uploads SHALL be validated at the boundary: content-type allowlist, **magic-byte sniffing** (never trust the client's declared type), size caps, and per-user upload quotas (`upload_slots`/`upload_bytes`).
- **FILE-2 (MUST):** Original filenames SHALL be sanitized/stripped; storage keys SHALL be server-generated (never user-controlled paths), preventing path traversal.
- **FILE-3 (MUST):** Thumbnails/variants SHALL be generated server-side only and follow the same signing rules as originals.
- **FILE-4 (MUST):** Validation SHALL run before any business logic or quota accounting that would trust the file.
- **FILE-5 (SHOULD):** Failed validation SHALL route to quarantine (§17, §19) rather than hard-rejection-without-trace, so abuse signals are captured.

**Why:** OWASP file-upload guidance and cloud storage practice are explicit: the file is attacker-controlled binary data, and trusting declared content types or client paths is how malware, XSS-through-uploads, and traversal bugs enter. Magic-byte validation + server-generated keys are the two non-negotiable controls; validation-before-trust keeps the file out of any state that later serves it.

---

## 19. Malware Protection

Requirements governing malicious content detection (`SECURITY.md` §14, `DEVOPS.md` §19).

- **MAL-1 (MUST):** Uploads SHALL be scanned at ingest; flagged content SHALL be quarantined and never shareable.
- **MAL-2 (MUST):** Originals and generated variants SHALL be covered by the same quarantine decision.
- **MAL-3 (MUST):** Scanner health SHALL be monitored — a silently stopped scanner is a data-integrity incident, not a no-op; scan backlog SHALL be tracked.
- **MAL-4 (SHOULD):** Scanner definitions SHALL update on the patching cadence.
- **MAL-5 (MUST):** Scanning SHALL apply to files, not message content; message text and message bodies SHALL NOT be scanned or logged (privacy, §20).
- **MAL-6 (MUST):** Quarantined media SHALL be backed up separately and purged per policy — never mixed with clean content.
- **MAL-7 (SHOULD):** Scan results SHALL feed the moderation/takedown queues and abuse telemetry.

**Why:** media at scale is the primary malware distribution channel in messaging (WhatsApp/Telegram both scan uploads and rely on user-report + takedown loops). The distinction in MAL-5 is a privacy line: the platform is not E2EE today, but it still must not treat message *content* as scanable data while it scans *files* — this preserves both security and the privacy posture (§20). MAL-3 exists because a scanner that silently fails is a false sense of security, the worst kind.

---

## 20. Privacy Protections

Requirements governing personal data (`SECURITY.md` §25, `DATABASE.md` PII posture, `ARCHITECTURE.md` §30.4).

- **PRIV-1 (MUST):** Data collection SHALL be minimized to what the product needs (identity, credentials, device/session metadata, conversation metadata, media, operational usage); nothing else.
- **PRIV-2 (MUST):** PII SHALL be separated from auth material (auth columns isolated from profile data) so profile reads can never leak credentials.
- **PRIV-3 (MUST):** The encryption posture SHALL be disclosed transparently — what the service can and cannot see — as a product surface and reviewed at release.
- **PRIV-4 (MUST):** Consent SHALL be obtained where required (contacts sync, push) and withdrawal SHALL be honored operationally (feature degrades, nothing more).
- **PRIV-5 (MUST):** Data rights SHALL be product features: access, correction, export, and erasure mapped to APIs and jobs (soft-delete + hard-purge, data export flows, account lifecycle states).
- **PRIV-6 (MUST):** Logs, telemetry, crash reports, and audit logs SHALL NOT contain message content or PII beyond operational need.
- **PRIV-7 (MUST):** The platform SHALL maintain a DPDP (India) posture at launch: data-principal rights, lawful processing, breach notification; GDPR posture SHALL be achievable (§32 roadmap).
- **PRIV-8 (SHOULD):** Privacy review SHALL be part of the pre-release checklist for every feature that touches user data.

**Why:** privacy is the trust foundation of messaging — Signal's privacy-by-default and WhatsApp's "your chats are between you" positioning show that perceived surveillance destroys adoption. PRIV-2 is the structural guarantee that a profile bug can never leak credentials; PRIV-6/PRIV-8 make privacy a tested property ("assume logs leak") rather than an aspiration; PRIV-7 sequences compliance with the India-first launch reality (DPDP Act) while keeping GDPR reachable.

---

## 21. Abuse Prevention

Requirements governing platform-integrity attacks (`SECURITY.md` §16, `API.md` Appendix B).

- **ABUS-1 (MUST):** Creation quotas SHALL cap conversation/message creation per user (`create_caps`), mass account creation, and enumeration (`contacts_sync` 1/24h, `snapshot` 10/24h/device).
- **ABUS-2 (SHOULD):** Fresh accounts SHALL operate under stricter caps until they establish history (verified credential, device trust, account age) — the spam-launch-pad defense.
- **ABUS-3 (MUST):** Block and report flows SHALL exist as product features; reports and blocks SHALL feed moderation and abuse telemetry.
- **ABUS-4 (MUST):** User suspension SHALL be an audited admin action, reversible with an incident trail.
- **ABUS-5 (MUST):** Abuse telemetry (block/report volume, fresh-account creation, suspension rate) SHALL feed the security dashboard with alert thresholds.

**Why:** every messaging platform fights the same economics — spam is cheap to produce and expensive to detect. WhatsApp and Telegram rely on report-driven moderation plus rate discipline; the caps in ABUS-1 make mass production uneconomic per identity; ABUS-2 attacks the classic spam-launch-pad (fresh accounts); ABUS-5 makes abuse a monitored signal rather than a support backlog.

---

## 22. Spam Detection

Requirements governing spam-specific defense (`SECURITY.md` §16).

- **SPAM-1 (MUST):** Per-conversation message-rate and size limits SHALL bound group spam and flood attacks.
- **SPAM-2 (SHOULD):** Volume- and pattern-based heuristics (and, where warranted, ML signals) SHALL score likely-spam behavior: rate, burstiness, recipient diversity, fresh-account links, link/referral content.
- **SPAM-3 (MUST):** Rate tiers SHALL be enforced independently of detection — spam control never relies on detection alone (§12).
- **SPAM-4 (SHOULD):** Likely-spam SHALL route to human review queues (moderation) rather than auto-destroying legitimate messages — false positives are a trust cost.
- **SPAM-5 (SHOULD):** Coordinated takedown SHALL be supported: admin suspends/restricts across accounts with audit.

**Why:** the industry consensus (Meta's spam defense, Telegram's public-channel moderation) is layered: hard rate economics + heuristic scoring + human review, with the explicit trade-off that false positives are worse than false negatives for a messaging product — a legitimate message silently dropped is a trust loss that no detection gain justifies. SPAM-3 keeps detection as an enhancement over, not a replacement for, the rate floor.

---

## 23. Anti-Bot Strategy

Requirements governing automated traffic (`DEVOPS.md` §18, `SECURITY.md` §17).

- **BOT-1 (MUST):** Cloudflare Bot Management SHALL be enabled, score-based, with challenge/block on credential-stuffing and scraping patterns.
- **BOT-2 (MUST):** Turnstile (or equivalent managed challenge) SHALL protect the sign-up/OTP flow — human verification without privacy-hostile CAPTCHAs, with scores feeding bot management.
- **BOT-3 (MUST):** Edge rate limits SHALL protect auth and search paths as the coarse anti-bot layer.
- **BOT-4 (SHOULD):** Device-integrity attestation (§7) SHALL add a non-web automation signal on mobile.
- **BOT-5 (MUST):** Bot detection SHALL reduce cost and add friction only; it SHALL NEVER replace server-side authorization or per-identity limits.
- **BOT-6 (SHOULD):** Bot-challenge and block rates SHALL be monitored as security telemetry.

**Why:** bot traffic is filtered most cheaply at the edge (Cloudflare's model) — the score-based approach beats binary blocking because legitimate-but-automated traffic (users' own clients, enterprise tools) must not be collateral damage. Turnstile represents the modern human-verification direction (Google moved reCAPTCHA toward invisible/managed challenges) and fits the privacy posture. BOT-5 is the guardrail: anti-bot is a cost-reduction and friction layer, never a security gate — exactly as the Security Handbook states.

---

## 24. Secrets Management

Requirements governing secrets (`DEVOPS.md` §7, `ENGINEERING.md` §31, `SECURITY.md` §21).

- **SECR-1 (MUST):** A managed vault (HashiCorp Vault or cloud secret manager) SHALL be the single source of truth for all secrets: DB passwords, signing keys, refresh-token hash pepper, push keys, Cloudflare tokens, backup keys.
- **SECR-2 (MUST):** Secrets SHALL NOT exist in source, Docker images, `.env`, Terraform state, CI logs, or application logs. A gitleaks scan SHALL be a CI merge-blocker.
- **SECR-3 (MUST):** Secrets SHALL be injected at runtime through the config layer; images SHALL be secret-free and safely promotable.
- **SECR-4 (MUST):** Credentials SHALL be least-privilege and per-service, per-environment; the migration role SHALL NOT be shared with the app role.
- **SECR-5 (MUST):** Rotation SHALL be rehearsed, not incident-driven: DB passwords, signing keys, push keys, and vault access all have rotation runbooks with scheduled drills.
- **SECR-6 (MUST):** Vault access SHALL be audited; break-glass access SHALL be time-boxed and flagged.
- **SECR-7 (MUST):** The Flutter client SHALL contain no secrets in source or prod `--dart-define`; keystores SHALL live in the vault and be used by CI only.

**Why:** the vault-as-single-source and no-secrets-in-artifacts rules are the baseline of every cloud security standard (SOC 2, ISO 27001, AWS/Azure/Google secret-management guidance) because a leaked secret is the highest-leverage attack — it can sign tokens, read backups, or impersonate the service. SECR-5 is the difference between rotation as practice and rotation as theater; SECR-7 closes the client-side leak (a hardcoded key in a mobile binary is extractable by anyone).

---

## 25. Audit Logging

Requirements governing the record of record (`ARCHITECTURE.md` §30.4, `SECURITY.md` §24).

- **AUD-1 (MUST):** The audit log SHALL record: authentication events, session lifecycle, authorization **denials**, admin and moderation actions, media deletions, and data exports.
- **AUD-2 (MUST):** The audit store SHALL be append-only, write-once, and tamper-evident (rotationally hash-chained so silent modification is detectable).
- **AUD-3 (MUST):** Audit logs SHALL be access-controlled (audit service + designated security roles only); break-glass reads SHALL be themselves audited.
- **AUD-4 (MUST):** Audit retention SHALL follow compliance policy (longer than operational logs) and be configured, not defaulted.
- **AUD-5 (MUST):** Audit entries SHALL be correlatable by `user_id`, `session_id`, `request_id`, and IP.
- **AUD-6 (MUST):** Audit logs SHALL record events and identities, not message content — the privacy guarantee (§20) holds inside the audit store.
- **AUD-7 (MUST):** Audit logs SHALL be in a dedicated sink with retention and access control separate from operational logs.

**Why:** SOC 2, ISO 27001, and DPDP/GDPR all require a defensible audit trail; an append-only, tamper-evident store is what makes the trail *evidence* rather than assertion. The content boundary (AUD-6) is deliberate: the audit store is high-value for attackers too, so it must not become a PII treasure chest — the same principle as Signal's metadata-minimization.

---

## 26. Security Monitoring

Requirements governing detection (`ARCHITECTURE.md` §29, `SECURITY.md` §22, `DEVOPS.md` §12).

- **MON-1 (MUST):** Authentication analytics SHALL be monitored: login success/failure, OTP request volume, refresh reuse detections, new-device/new-geography logins, authz-denial spikes.
- **MON-2 (MUST):** Abuse signals SHALL be monitored: block/report volume, fresh-account creation, suspension rate, spam triggers (§21–§22).
- **MON-3 (SHOULD):** Data-layer anomalies SHALL be monitored: unusual read patterns, query volume on sensitive tables, RLS-denial volume, media download/export spikes.
- **MON-4 (SHOULD):** Edge telemetry (WAF block rate, bot-challenge rate) SHALL feed Loki/SIEM for edge-level visibility.
- **MON-5 (MUST):** Security events SHALL page distinct from product noise: a `sessions:revoke` storm or refresh-reuse burst is a page, not a dashboard curiosity.
- **MON-6 (SHOULD):** An "assume breach" dashboard SHALL track: token-family reuse, login volume from new regions, export/snapshot volume, authz-denial spikes, media-deletion storms, admin-action volume.

**Why:** detection is the difference between a breach and a quiet exfiltration. Security monitoring converts the audit log (§25) from reactive evidence into proactive signal — the SOC/SIEM pattern (alert on suspicious authentication, data-exfiltration markers, and admin anomalies) applied at the platform's scale. MON-5 encodes the insight that security-relevant volume changes are the strongest early-warning signals in a messaging platform.

---

## 27. Incident Response

Requirements governing containment and recovery (`SECURITY.md` §26, `DEVOPS.md` §14).

- **IR-1 (MUST):** Every security incident SHALL follow the skeleton: detect → triage & classify → contain → investigate → remediate → verify → post-mortem.
- **IR-2 (MUST):** Containment primitives SHALL be available and rehearsed: session/device suspension, admin kill-switch, refresh-token-family revocation, `jti` blacklist, media quarantine, edge block/rate-limit, key rotation.
- **IR-3 (MUST):** Contain-before-investigate SHALL apply where blast radius is user data; disclosure SHALL follow policy and law (DPDP/GDPR breach-notification timelines), coordinated, never ad-hoc.
- **IR-4 (MUST):** Security runbooks SHALL exist per incident class (Appendix C of `SECURITY.md`) with detection, severity, owner, steps, verification.
- **IR-5 (MUST):** At least one security tabletop drill SHALL run per quarter (e.g., "token family stolen at scale", "media quarantine breach").
- **IR-6 (MUST):** Every security incident SHALL update the threat model and the affected runbooks — a surviving failure mode is a process failure.
- **IR-7 (MUST):** Post-mortems SHALL be blameless, with follow-ups tracked to completion.

**Why:** NIST SP 800-61 and every serious IR framework agree: response quality is determined before the incident by rehearsed playbooks and pre-positioned containment primitives. The containment-first ordering exists because for user-data incidents, time-to-contain is the metric that matters (WhatsApp's own recovery guidance is a containment playbook). IR-6 closes the loop that keeps the playbook accurate.

---

## 28. Data Retention

Requirements governing data lifecycle (`DATABASE.md` §17–§18, `DEVOPS.md` §13, §19).

- **RET-1 (MUST):** Retention policy SHALL be defined per data class (accounts, messages, media, credentials, sessions, audit, backups) and SHALL be the driver of purge jobs — not an afterthought.
- **RET-2 (MUST):** Hard-purge SHALL be a background worker that removes rows after the retention + grace window; soft-delete + `deleted_at` SHALL precede hard-purge for GDPR/DPDP-style erase.
- **RET-3 (MUST):** Accounts SHALL NOT be auto-deleted on inactivity; suspended/deleted accounts SHALL be retained per legal policy and hard-erased via the purge worker.
- **RET-4 (MUST):** Media SHALL carry retention dates; cleanup SHALL purge staged, deleted, orphan, and expired objects.
- **RET-5 (MUST):** Backup retention SHALL align with compliance and be sized to it, not to habit; WAL retention SHALL be monitored.
- **RET-6 (MUST):** Retention/archiving/purge SHALL be scheduled jobs with defined RPO/RTO (an operable data governance model, `DATABASE.md` §9).
- **RET-7 (MUST):** Erasure SHALL hold end-to-end: a deleted account is not recoverable from a retained backup outside the legal retention window.

**Why:** data governance is where privacy promises (§20) become operational truth. Retention-bounded storage also limits the blast radius of any data leak and controls cost (the classic operating tension: storage is cheap until it's a liability). RET-3 encodes the DPDP/GDPR distinction between inactivity and erasure; RET-7 makes erasure a real guarantee rather than a UI action that a backup quietly undoes.

---

## 29. Account Recovery

Requirements governing getting users back in safely (`API.md` §4, WhatsApp-class recovery practice).

- **REC-1 (MUST):** Recovery SHALL follow a verified hierarchy: (1) a currently-registered trusted device; (2) identifier verification via OTP; (3) a two-step-verification PIN/email if configured — never a support-agent override without full verification.
- **REC-2 (SHOULD):** Two-step verification (WhatsApp-style PIN + recovery email) SHALL be offered to raise the cost of takeover after OTP compromise; a forgotten PIN without recovery email SHALL trigger a safe waiting period, not a bypass.
- **REC-3 (MUST):** The recovery flow SHALL treat the phone-number/carrier layer as a known weakness (SIM swap) and SHALL NOT treat SMS receipt alone as sufficient for a *high-risk* recovery without additional signals.
- **REC-4 (MUST):** Recovery SHALL revoke existing sessions/devices as part of re-establishing control (attacker sessions do not survive recovery).
- **REC-5 (MUST):** Every recovery event SHALL be audited and alerted (new-device registration, recovery attempts, failed-PIN volume).
- **REC-6 (MUST):** Recovery attempts SHALL be rate-limited; repeated rapid attempts SHALL extend waiting periods (lockout-aware, per WhatsApp-class practice).
- **REC-7 (SHOULD):** The product SHALL surface security guidance for the two real-world takeover paths the platform cannot fix: verification-code sharing and SIM swap — via in-app education at the point of risk.

**Why:** account recovery is the hardest balance in messaging security: it must be accessible enough that real users regain access and strict enough that attackers can't. WhatsApp's recovery model (registration code + two-step PIN + recovery email + waiting period + linked-device cleanup) is the proven playbook, and REC-3/REC-7 explicitly acknowledge the takeover paths that bypass *any* SMS-based control — SIM swap and social engineering — which the platform must surface rather than pretend to solve alone.

---

## 30. Backup Security

Requirements governing restore safety (`ARCHITECTURE.md` §35, `DEVOPS.md` §13, §20).

- **BACK-1 (MUST):** Backups SHALL be encrypted, with keys separate from data keys (in the vault), and stored off-machine.
- **BACK-2 (MUST):** Backup and restore SHALL be exercised monthly: a restore drill SHALL restore a base + replay WAL to a chosen timestamp and validate schema, data counts, and a business flow (a send + read-receipt round trip); timed restore = measured RTO.
- **BACK-3 (MUST):** Backups SHALL NOT be plaintext-readable by anyone with storage access — "a leaked backup is not a leak."
- **BACK-4 (MUST):** DB↔media reconciliation SHALL run as part of the backup pipeline so a restored pair is coherent.
- **BACK-5 (MUST):** Backup failures SHALL page; a silent backup failure is a data-loss incident waiting to happen.
- **BACK-6 (MUST):** Backup storage SHALL be access-controlled and audited; the backup role SHALL be least-privilege (no write to live tables).
- **BACK-7 (MUST):** Retention windows (PG base + WAL, media, audit) SHALL be aligned with compliance (§28) and enforced.
- **BACK-8 (SHOULD):** Backups SHALL be immutable/append-only where the platform stores them, to resist ransomware-style deletion.

**Why:** backups are the data-loss insurance policy, and every RPO/RTO target in `ARCHITECTURE.md` §35 is only real if restore is drilled. The encryption requirement is the flip side of media/at-rest protection: the platform's own backup store must not be the easiest exfiltration path. BACK-2 makes the monthly restore drill a scheduled, measured activity — the standard that separates a real DR posture from a folder of empty promises.

---

## 31. Future End-to-End Encryption Architecture

The platform is **not** E2EE-by-default (server-side search, moderation, retention, and product flows require server access to message content per `ARCHITECTURE.md`). This section specifies the *target architecture* if/when E2EE is introduced — a scoped product decision, not a v1 requirement, and never a bolt-on that weakens moderation or recovery guarantees (`SECURITY.md` §18).

- **E2EE-1 (MUST, if E2EE is adopted):** E2EE SHALL be a distinct, clearly-labeled mode with its own threat model and key-handling design; it SHALL NOT alter the security of the existing non-E2EE mode.
- **E2EE-2 (MUST, if adopted):** One-to-one encryption SHALL follow the Signal protocol baseline: **X3DH** key agreement for asynchronous session establishment + **Double Ratchet** for continuous forward secrecy and break-in recovery (post-compromise security). This is the cryptographic baseline used by Signal, WhatsApp, and iMessage-class ratchet protocols.
- **E2EE-3 (MUST, if adopted):** Group encryption SHALL use per-sender sender keys (Signal/WhatsApp model) for small/medium groups, and **SHALL evaluate MLS (Messaging Layer Security)** for sublinear group operations at scale — the standard effort for scalable, ratcheted group messaging.
- **E2EE-4 (MUST, if adopted):** Post-quantum transition SHALL be planned via the PQXDH extension path that ratchet-family protocols already define, timed to the ecosystem standard.
- **E2EE-5 (MUST, if adopted):** Identity keys SHALL be authenticated out-of-band via safety-number comparison and **SHALL** be backed by key-transparency/public-key auditing to resist MITM at the server.
- **E2EE-6 (MUST, if adopted):** E2EE backups SHALL be keyed by a user-held secret (WhatsApp 64-digit key / Signal recovery key pattern) — the platform SHALL NOT be able to decrypt E2EE backups.
- **E2EE-7 (SHOULD, if adopted):** Delivery metadata SHALL be minimized (sealed-sender pattern) to reduce server-visible social graphs.
- **E2EE-8 (MUST, if adopted):** Device key hierarchy, rotation, revocation, and recovery SHALL be designed and rehearsed before rollout; a lost-key recovery path SHALL exist and be user-tested.
- **E2EE-9 (MUST, if adopted):** The threat model (Appendix A) SHALL be re-baselined: E2EE changes what the server can see and what it must protect, and the moderation/search/product trade-offs SHALL be explicitly documented and user-disclosed.

**Why:** the industry is unambiguous on the cryptographic baseline — the Signal Protocol (X3DH + Double Ratchet + Sender Keys) is what Signal, WhatsApp, and the ratchet family use, and MLS is the forward-looking group standard. The specification deliberately scopes adoption (E2EE-1) because the hardest part of E2EE is not the crypto but the product trade-offs — moderation, search, recovery, and transparency — which must be decided and disclosed, not inherited. This mirrors how Signal and WhatsApp document their E2EE boundaries (what's encrypted, what's metadata, what's recoverable).

---

## 32. Security Roadmap

The roadmap sequences work by business reality — India launch first — and by risk. Each phase has trigger criteria so the work is earned by demand, not scheduled by hope.

**Phase 1 — India launch (GA):**
- All `MUST` requirements in this specification implemented and conformance-verified (Appendix A).
- DPDP posture (§20), two-step verification offered (§29), account recovery flows, incident response runbooks + drills standing, quarterly external pen test started.

**Phase 2 — Growth (post-launch, trigger: sustained user growth + first compliance asks):**
- **Passkeys as default** credential path; OTP progressively demoted to fallback (matches WhatsApp's direction).
- **Key transparency** for any public-key material; device-trust attestation enforced beyond risk-scoring.
- **E2EE opt-in pilot** (1:1 first, per §31) with safety numbers and E2EE backups; moderated by the product decisions E2EE-9 requires.
- SOC 2 Type II readiness (control evidence from the conformance matrix).

**Phase 3 — Global (trigger: EU/GDPR market entry + enterprise demand):**
- GDPR posture operationalized (DPA, EU representative, lawful-basis documentation).
- Full E2EE mode where product decisions allow; PQXDH transition per ecosystem timing.
- Regional data residency; DR across regions per `ARCHITECTURE.md` §35.

**Standing cadence (all phases, non-negotiable):**
- Quarterly external pen test; annual red team; remediation tracked to closure (`DEVOPS.md` §22).
- Key rotation rehearsals (§16); backup restore drills (§30); security tabletop drills (§27).
- Threat-model review at each major feature and each phase change; roadmap re-validated quarterly against regulatory change (DPDP rules, IT Rules evolve).

**Why a roadmap exists:** security investment must follow risk and business sequencing — India-first means DPDP and SIM-swap realities ahead of GDPR; the E2EE decision is gated on the product trade-offs it forces. A triggered, cadence-backed roadmap (the pattern of WhatsApp/Telegram-class teams) keeps security spending proportional and evidence current, so compliance asks are lookups, not projects.

---

## Appendix A — Conformance Matrix

Format: `Requirement ID | Control | Verification | Owner`. Exhaustive index generated from this specification; shown here in abbreviated form for the highest-risk requirements. The full matrix is the security lead's living artifact, regenerated at each release.

| Req | Control | Verification | Owner |
|---|---|---|---|
| AUTH-1..11 | Authentication (passkeys/OTP/password/lockout/step-up) | QA security suite (`QA.md` §14) + audit events | backend |
| AUTHZ-2..10 | Membership/role/serve-time authz + RLS | Authz matrix tests + RLS direct-SQL tests | backend |
| SESS-5..6 | Revocation immediacy + global token version | WS revocation test + logout-all test | backend |
| JWT-1..8 | JWKS, alg policy, TTL, blacklist, rotation | Token test suite + rotation drill | backend |
| REFR-2..5 | Hash storage, rotation, reuse→revoke | Refresh reuse test (`410` path) | backend |
| DEVM-1..7 | Device binding, integrity, client storage | Device tests + client storage audit | backend + Flutter |
| OTP-1..6 | Hashing, TTL, throttling, Turnstile | OTP suite + edge config review | backend + DevOps |
| API-1..8 | Header/validation/idempotency/errors | API contract suite | backend |
| WS-1..8 | Pre-upgrade auth, subscription authz, frame limits | WS functional + load suite | backend |
| RATE-1..7 | Tier enforcement + headers | Rate-limit test per tier | backend |
| DDoS-1..6 | Edge defense + origin lockdown | Config review + stress tests | DevOps |
| ATR-1..6 | At-rest encryption + key separation | Infra scan + config review | DevOps |
| ITR-1..7 | TLS/HSTS/pinning/Full-Strict | TLS scanner + config review | DevOps |
| KEY-1..8 | Vault, separation, rotation, enclaves | Vault audit + rotation drills | security/DevOps |
| MED-1..7 | Signed URLs, membership re-check, quarantine | Media suite + serve-time tests | backend |
| FILE-1..5, MAL-1..7 | Validation, scanning, quarantine health | Upload/scan tests + monitoring | backend/DevOps |
| PRIV-1..8 | Minimization, rights, DPDP posture | Privacy review in release checklist | security/product |
| ABUS-1..5, SPAM-1..5 | Caps, fresh-account, moderation | Abuse telemetry + caps tests | backend |
| BOT-1..6 | Bot mgmt, Turnstile, edge limits | Edge config + signup flow tests | DevOps |
| SECR-1..7 | Vault, no secrets, rotation | gitleaks + vault audit + drills | DevOps |
| AUD-1..7 | Append-only, tamper-evident, access | Audit store test + access review | security |
| MON-1..6 | Auth/abuse/edge monitoring | Dashboards + alert tests | security/SRE |
| IR-1..7 | Response skeleton + drills | Tabletop drills + runbook review | security |
| RET-1..7 | Retention + purge + erasure | Purge tests + retention audit | backend |
| REC-1..7 | Recovery hierarchy + 2SV + waiting period | Recovery flow tests | backend |
| BACK-1..8 | Encrypted backups + restore drills | Monthly restore drill records | DevOps |
| E2EE-1..9 | Future architecture (conditional) | Design review per adoption decision | security/architecture |

## Appendix B — Normative References & Industry Baselines

**Industry practices referenced (normative influence, not contractual):**
- Signal Protocol specifications: X3DH, Double Ratchet, Sender Keys; PQXDH extension (signal.org/docs/specifications)
- WhatsApp security model: two-step verification + recovery email, passkeys, encrypted backups (64-digit key), strict account settings, linked-device recovery
- Telegram/Discord: realtime gateway hardening, moderation/report loops, bot handling
- OWASP: API Security Top 10, JWT best practices, authentication/filed-upload guidance, Mobile Top 10
- NIST: SP 800-63B (password/authn), SP 800-57 (key management), SP 800-61 (incident response)
- Cloud: Cloudflare (WAF/bot/API Shield/Turnstile/DDoS), AWS S3 presigned-URL practice, CIS benchmarks
- Privacy/compliance: DPDP Act 2023 (India), GDPR, IT Rules 2021 (intermediary guidelines), SOC 2 Type II, ISO 27001

**Mapping note:** each specification section's rationale names the specific baseline behind its requirements; this appendix is the index of those baselines for audit and due-diligence lookup.

---

*End of Security & Cryptography Specification. The testable security contract for the platform — backend, Flutter app, and infrastructure alike. Source-of-truth documents win on conflict; raise conflicts as a PR.*
