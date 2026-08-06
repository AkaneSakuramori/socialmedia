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
