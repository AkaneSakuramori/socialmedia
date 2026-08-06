# Changelog

All notable changes to this project are documented here, starting with Sprint 0.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning follows [SemVer](https://semver.org/).

## [Unreleased]

### Sprint 0 — Go backend foundation (no business features)

#### Added
- **Go module** `github.com/AkaneSakuramori/socialmedia/server` (pinned to Go
  1.24.0, deps: `pgx/v5 v5.8.0`, `go-redis/v9 v9.22.0`).
- **`server/cmd/api-server`** entrypoint with `-healthcheck` flag (used by the
  Docker healthcheck; no shell in the distroless image).
- **`server/config`** — single validated `Config` struct, env-only, fail-fast
  aggregate validation, secret redaction in logs.
- **`server/internal/app`** — composition root: constructor injection, DI,
  process lifecycle (start/stop), and `Healthcheck()`.
- **`server/internal/platform/observability`** — `log/slog` setup (JSON in
  non-dev, text in dev), `X-Request-Id` middleware, access logging.
- **`server/internal/platform/apierr`** — RFC 9457 `application/problem+json`
  errors with stable codes (`VALIDATION_ERROR`, `NOT_FOUND`, `CONFLICT`,
  `RATE_LIMITED`, `PAYLOAD_TOO_LARGE`, `UNAUTHORIZED`, `FORBIDDEN`), writer,
  unit tests.
- **`server/internal/platform/health`** — liveness/readiness registry and
  handlers (`/healthz`, `/readyz`) with PostgreSQL and Redis checks, unit
  tests.
- **`server/internal/platform/httpserver`** — server assembly, middleware
  (recover, request-id, access log), tests.
- **`server/internal/platform/postgres`**, **`redis`** — thin pool/client
  wrappers with startup liveness ping.
- **`server/migrations`** — forward-only migration pipeline
  (`000001_bootstrap` marker).
- **`infra/docker/docker-compose.yml`** — PostgreSQL 16 + Redis 7 + api-server
  services; `tools` profile with the golang-migrate runner.
- **`infra/docker/postgres/init/01-bootstrap.sql`** — distinct `app`/`migrator`
  roles and superuser-owned extensions (`pgcrypto`, `pg_trgm`).
- **`server/Dockerfile`** — multi-stage build to distroless non-root image with
  injected version. `server/.dockerignore`, `server/.golangci.yml`,
  `server/.env.example` (only env template committed).
- **`Makefile`** — venv-bound `dev-up/dev-down/dev-api/migrate/test/lint/build/ci`
  contract.
- **`.github/workflows/ci.yml`** (vet → lint → race+coverage → build →
  govulncheck → gitleaks → trivy) and **`.github/workflows/cd.yml`**
  (tag-triggered skeleton).
- **`scripts/dev.sh`**, **`check.sh`**, **`smoke.py`** — local dev/check/smoke
  tooling (Python via project venv).
- **Docs** — root `README.md` (status, quickstart, repo tree, roadmap),
  `server/README.md` (layout, config, endpoints, dev, quality), `AGENTS.md`
  (repo guide), `.gitignore`.

#### Changed
- Root `AGENTS.md` updated for the new structure (backend work lives under
  `server/`; `make ci` gate).
- Go dependency set pinned to Go 1.24-compatible releases (`pgx v5.8.0`).

#### Fixed
- Extensions (`pgcrypto`, `pg_trgm`) moved to container bootstrap (superuser)
  — the unprivileged `migrator` role cannot install non-trusted extensions.
- Readiness endpoint returns `"status": "ready"` when all checks pass
  (matches DEVOPS.md §5 semantics and the smoke contract).
- `docker compose migrate` correctly activates the `tools` profile.

### Sprint 1 — Authentication & Identity (milestone 1: registration)

#### Added
- **`server/pkg/tx`** — transaction boundary abstraction (`Tx`, `Beginner`)
  so use-cases stay DB-agnostic.
- **`server/pkg/clock`** — injectable `Clock` for deterministic time in tests.
- **`server/internal/platform/idgen`** — snowflake id generator
  (41-bit ms | 10-bit node | 12-bit sequence, epoch 2020-01-01), monotonicity,
  uniqueness-under-concurrency, clock-skew and overflow tests.
- **`server/migrations/000002_auth`** — `users`, `user_credentials`,
  `user_sessions` tables (snowflake `bigint` PKs, CHECK-enummed state columns,
  `set_updated_at()` trigger, trimmed GIN index, partial unique username on
  non-deleted rows) plus the reverse-order `down.sql`.
- **`server/internal/user/domain`** — user aggregate with account-state
  machine, `Username`/`DisplayName` value objects (length, charset, reserved
  list), `UserRepository` ports (`Create`/`FindBy*`/`*Taken`), domain errors.
- **`server/internal/auth/domain`** — normalized `Identifier` (E.164 phone /
  email), password rules (`SECURITY_SPEC.md` PASS-2), PHC `PasswordHash`,
  SHA-256 opaque-token hashing (REFR-2), `Credential`, `Session`/`DeviceInfo`
  (state machine, platforms), and the auth port set (`CredentialRepository`,
  `SessionRepository`, `PasswordHasher`, `TokenIssuer`, `OTPVerifier`,
  `IDGenerator`).
- **`server/internal/auth/infra/security/argon2id.go`** — Argon2id with
  unique random salt, PHC strings, parameter drift tolerance and constant-time
  comparison (PASS-1).
- **`server/internal/auth/infra/security/token.go`** — Ed25519 JWT access
  tokens (claims `sid`, `dev`, `scopes`, `sub`, `iss`, `aud`, `exp`, `jti`) and
  256-bit opaque refresh tokens (ARCHITECTURE.md §10.2), with verification
  pinned to method/issuer/audience/expiry.
- **`server/internal/auth/application`** — `Service` + `Register` use-case:
  OTP verification, uniqueness checks, then user/credential/session + token
  pair in one transaction (DATABASE.md §10). `service.go` carries the port
  wiring (`Deps`) for the remaining Sprint 1 use-cases.
- **`server/config`** — JWT issuer/audience/key, access/refresh TTLs, Argon2id
  params, and snowflake node id configuration with cross-field validation
  (`access_ttl < refresh_ttl`, Argon2id memory/threads floor, node range,
  staging/prod require a signing key).
- **`server/.env.example`** — the new auth configuration variables.
- **Docs** — `server/README.md` (layout, config table, Sprint 1 status) and
  `CHANGELOG.md` (this section).

#### Changed
- `server/go.mod` — added `golang.org/x/crypto` (Argon2id) and
  `github.com/golang-jwt/jwt/v5` (JWT), with transitive deps bumped to
  Go 1.24-compatible versions.

#### Applied
- Migration `000002_auth` applied to the dev stack; verified tables,
  constraints, indexes and the `set_updated_at` trigger in PostgreSQL.

### Sprint 1 — Authentication & Identity (milestone 2: login & lockout)

#### Added
- **`server/internal/auth/domain/errors.go`** — auth domain errors:
  `ErrInvalidCredentials`, `ErrAccountSuspended`, `ErrUnsupportedLoginMethod`,
  `ErrSessionNotFound`, and `AccountLockedError` (AUTH-5, carries the remaining
  lockout for `Retry-After`).
- **`server/internal/auth/domain/ports.go`** — `LoginMethod`
  (password/otp/passkey), `LoginPolicy` + `DefaultLoginPolicy` (AUTH-5:
  5 fails → 5 min), `LoginThrottle` (per-identifier failure counter shared by
  all credential methods, PASS-6/OTP-3), `AuditLogger`/`AuditEvent` (AUTH-7),
  and `SessionRepository.FindByDeviceID`/`Update` for device session upsert.
- **`server/internal/auth/domain/session.go`** — `ValidateDeviceID`
  (DEVM-1: required, ≤64 chars, restricted charset).
- **`server/internal/auth/domain/credential.go`** — `Credential.PasswordHash()`
  to read the PHC payload from `credential_data`.
- **`server/internal/auth/application/login.go`** — `Login` use-case
  (API.md §4.3): lockout gate → account lookup (suspended blocked, deleted
  non-enumerated) → credential verify (Argon2id constant-time / OTP) →
  per-device session upsert/rotate with token pair in one transaction.
  Timing-equalization dummy verify prevents account enumeration through
  response time. Best-effort audit of `auth.login*` events (AUTH-7).
- **`server/internal/auth/infra/throttle/redis.go`** — Redis-backed
  `LoginThrottle`: failure counter with TTL = lockout window, so the lockout
  expires naturally; concurrency-safe via atomic INCR.
- **`server/config`** — `APP_LOGIN_MAX_FAILURES` (default 5) and
  `APP_LOGIN_LOCKOUT_DURATION` (default 5m) with validation; documented in
  `server/.env.example` and `server/README.md`.

#### Changed
- `Service` interface now exposes `Login(ctx, LoginCommand)`; `Deps` gained
  `Throttle`, `Policy`, and `Audit`.

#### Verified
- `make ci` green; Redis throttle integration-checked against the live stack
  (3 fails → locked → window expiry → clear).
