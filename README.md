# InChat

**Made-in-India social messaging. Private, fast, and built for the Indian internet.**

InChat is a social media and messaging platform designed and engineered for India first — from the ground up. It pairs a modern, offline-first Flutter app with a high-performance Go backend, built to deliver fast, reliable messaging on the real-world devices and networks that India runs on.

> **Status:** Design finalized · Go backend foundation (Sprint 0) shipped · Sprint 1 in progress (registration, login + lockout, refresh rotation with reuse detection, and device/session management with logout landed).
>
> This repository is the **public home of InChat's engineering documentation** — the source of truth for the product: architecture, database, API, backend and Flutter engineering guides, DevOps, quality/release, and security standards. It intentionally contains **documentation only**; production code lives in the **[InChat code repository](https://github.com/AkaneSakuramori/inchat)**.

---

## Why InChat

- **Made in India, for India.** Designed for the Indian market's realities — low-cost Android devices, constrained and unstable networks, and DPDP-first data practices. India-first launch; global scale is a designed path, not a redesign.
- **Privacy and security by default.** Strong authentication (passkeys first), refresh-token rotation with theft detection, signed and short-lived media URLs, encrypted data at rest and in transit, and a published security standard. A transparent roadmap toward end-to-end encryption.
- **Offline-first reliability.** Messages send instantly, sync reliably, and never get lost — built for flaky connectivity, not just ideal Wi-Fi.
- **Engineering discipline, not vibes.** Every decision is written down, versioned, and enforced by CI: testing pyramids, quality gates, canary releases with auto-rollback, and rehearsed disaster recovery.

---

## Features

- **1:1 and group messaging** — messages, edits, delete-for-all/self, media, read receipts, typing indicators, presence
- **Media sharing** — images/videos/files with thumbnails, quotas, and signed short-lived URLs
- **Offline-first sync** — optimistic sends, durable offline queue, deduplicated delta sync, and WS resume with no gaps or duplicates
- **Push notifications** — FCM/APNs with per-device token management
- **Search** — fast, scoped to your conversations
- **Rich authentication** — passkeys (WebAuthn), OTP fallback, password; refresh-token rotation with reuse detection
- **Device management** — see and revoke any session, sign out everywhere
- **Admin & moderation** — SSO-protected console, content reports, takedowns, audit trail
- **Security & privacy** — signed media URLs, PostgreSQL RLS, audit logging, DPDP-aligned data practices, compliance roadmap

---

## Tech Stack

| Layer | Technology |
|---|---|
| Mobile client | Flutter (Dart) — offline-first, SQLCipher-encrypted local DB |
| Backend | Go — modular monolith (domain-driven, clean architecture) |
| Database | PostgreSQL — source of truth, WAL archiving + PITR, RLS safety net |
| Realtime/cache | Redis — cache, presence/typing, job queues, pub/sub |
| Edge | Cloudflare — CDN, WAF, DDoS protection, bot management, TLS |
| Infra | Docker, Terraform (IaC), GitHub Actions CI/CD |
| Observability | Prometheus, Grafana, Loki, Tempo; crash reporting for the app |

---

## Repository Structure

```
├── architecture/            # Finalized engineering documentation (source of truth)
│   ├── ARCHITECTURE.md      # System architecture, modules, data flows, scaling
│   ├── DATABASE.md          # Schema, indexes, transactions, retention
│   ├── API.md               # REST + WebSocket API specification
│   ├── ENGINEERING.md       # Backend engineering guide (Go)
│   ├── FLUTTER.md           # Flutter app engineering guide
│   ├── DEVOPS.md            # DevOps & infrastructure handbook
│   ├── QA.md                # QA, testing & release engineering handbook
│   ├── SECURITY.md          # Security & cryptography handbook
│   └── SECURITY_SPEC.md     # Normative security requirements specification
└── README.md
```

---

## The Documentation Set

The documentation is the single source of truth for the product and is enforced as such — no decision is redesigned after finalization:

| Document | Covers |
|---|---|
| `ARCHITECTURE.md` | System design: modules, authentication, sessions, authorization, media, sync, observability, DR, scaling |
| `DATABASE.md` | PostgreSQL schema, indexes, concurrency, migrations, retention, RPO/RTO |
| `API.md` | Complete REST + WebSocket contract: conventions, errors, auth, rate limits, sync protocol |
| `ENGINEERING.md` | Backend standards: project structure, testing pyramid, release/versioning, performance |
| `FLUTTER.md` | App standards: architecture, offline/sync engine, performance, testing, security |
| `DEVOPS.md` | Operations: environments, CI/CD, monitoring/alerting, backups, DR, release pipeline |
| `QA.md` | Quality gates, test strategy (unit→E2E), performance/load/stress, release & rollback |
| `SECURITY.md` / `SECURITY_SPEC.md` | Security operating model + testable, conformance-checked security requirements |

---

## Security & Privacy

- **Authentication:** passkeys first, OTP fallback, throttling + lockout, refresh-token rotation with reuse detection
- **Transport:** TLS 1.2+/1.3, HSTS, `wss://`, Full-Strict at the edge
- **Data at rest:** encrypted volumes, encrypted PostgreSQL, encrypted backups (key-separated), SQLCipher on device
- **Authorization:** resource-based, membership-checked at API + realtime + media serve time, RLS safety net
- **Media:** signed, short-lived, per-requester URLs with membership re-checks; scanning + quarantine
- **Compliance:** DPDP-aligned from launch (data rights, erasure, breach notification); GDPR and SOC 2 on the roadmap

See `SECURITY.md` and `SECURITY_SPEC.md` for the full standard.

---

## Roadmap

- [x] **Phase 0 — Design:** architecture, database, API, and engineering documentation finalized
- [x] **Sprint 0 — Backend Foundation:** modular Go monolith skeleton — config, DI, logging, RFC 9457 errors, health probes, PostgreSQL/Redis, Docker/Compose, CI/CD
- [ ] **Sprint 1 — Auth & Identity:** registration, login (password/OTP, AUTH-5 lockout), refresh-token rotation with reuse detection, and device/session management with logout (list/rename/revoke/sign-out-all, SESS-3/SESS-6) shipped in `internal/auth`; self-service OTP and account recovery remain, then HTTP delivery (`/v1/auth/*`)
- [ ] **Phase 1 — India launch:** application code (backend + Flutter app), infrastructure, canary releases, staged app rollout
- [ ] **Phase 2 — Scale:** India-scale growth, passkeys as default, key transparency, E2EE pilot
- [ ] **Phase 3 — Global:** multi-region, GDPR posture, end-to-end encryption mode

---

## Contributing

Not yet open for contribution. Engineering standards and review requirements are fully defined in the documentation set (`ENGINEERING.md`, `QA.md`, `SECURITY_SPEC.md`) and will apply to every contributor — human or AI — when code work begins. The code lives at [AkaneSakuramori/inchat](https://github.com/AkaneSakuramori/inchat).

---

## License

Proprietary. All rights reserved.

© 2026 InChat
