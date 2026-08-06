# InChat — Backend (Go)

The InChat backend is a **modular monolith** (Go 1.24) following the finalized
`architecture/` documents — `ENGINEERING.md` is the backend source of truth,
`ARCHITECTURE.md` and `API.md` the contract, `ENGINEERING_RULES.md` the house law.

> **Status:** Sprint 0 — foundation only. No authentication, no messaging.
> Everything here is infrastructure every future feature builds on.

## Stack

| Layer | Technology |
|---|---|
| Language | Go 1.24 (`log/slog`, stdlib `net/http`) |
| Database | PostgreSQL 16 (pgx/v5 pool) |
| Cache/realtime | Redis 7 (go-redis/v9) |
| Migrations | golang-migrate v4 (via `migrate/migrate` Docker image) |
| Packaging | Multi-stage Docker → distroless non-root |
| CI | GitHub Actions (`vet`, golangci-lint, race tests, vuln/secret scans) |

## Layout (ENGINEERING.md §2–§3)

```
server/
├── cmd/api-server/        # thin entrypoint: flags + app.Run / app.Healthcheck
├── config/                # typed, validated, env-only config (never imported by business code)
├── internal/
│   ├── app/               # composition root: DI, lifecycle, graceful shutdown
│   └── platform/          # leaf infrastructure, depends on nothing
│       ├── apierr/        # RFC 9457 error contract (API.md §2.5, Appendix A)
│       ├── observability/ # slog setup + request-id/access-log middleware
│       ├── health/        # /healthz + /readyz probe registry
│       ├── httpserver/    # HTTP server bootstrap + middleware chain
│       ├── postgres/      # pgx pool wrapper
│       └── redis/         # go-redis client wrapper
├── migrations/            # numbered, forward-only SQL migrations
├── .env.example           # committed template (real env files are gitignored)
├── Dockerfile
└── .golangci.yml
```

Future domains (`internal/user`, `internal/message`, …) follow the mandatory
`delivery / application / domain / infra` four-layer convention from
`ENGINEERING.md` §3 — nothing new lands outside it.

## Configuration

12-factor, environment-only (`ENGINEERING.md` §11–§12). Loaded once at startup,
validated, and never passed through call stacks — consumers get concrete values.

| Variable | Default | Description |
|---|---|---|
| `APP_ENV` | `dev` | `dev \| staging \| prod` (selects log format/level) |
| `APP_HTTP_PORT` | `8080` | HTTP listen port |
| `APP_HTTP_READ_HEADER_TIMEOUT` | `5s` | slowloris guard |
| `APP_SHUTDOWN_TIMEOUT` | `15s` | graceful drain budget |
| `APP_PG_DSN` | (dev default) | runtime PostgreSQL DSN |
| `APP_PG_MAX_CONNS` | `10` | pool size |
| `APP_REDIS_ADDR` | `localhost:6379` | Redis address |
| `APP_REDIS_PASSWORD` | — | Redis password (empty when unset) |
| `APP_REDIS_DB` | `0` | Redis logical DB |

Copy `server/.env.example` → `server/.env.local` for local overrides; `dev.sh`
sources it. `config.String()` redacts passwords for safe logging.

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness — process alive (independent of dependencies) |
| `GET /readyz` | Readiness — PostgreSQL + Redis reachable; 503 while failing |

Everything else returns the RFC 9457 `application/problem+json` 404 envelope
(`code`, `title`, `status`, `detail`, `instance`, `request_id`, `retryable`).

Every response carries `X-Request-Id`; every log line carries `request_id`.

## Local Development

```sh
make dev-up      # PostgreSQL + Redis via infra/docker/docker-compose.yml
make migrate     # apply migrations (runs as the migrator role, not app)
make dev-api     # run api-server locally on :8080
make health      # smoke /healthz + /readyz via venv python
make ci          # vet + race tests + build (the local gate)
```

## Docker

- `server/Dockerfile` — multi-stage: `golang:1.24-alpine` build → distroless
  static non-root runtime; `CGO_ENABLED=0` static binary; `VERSION` ldflag.
- `infra/docker/docker-compose.yml` — postgres (with role-bootstrap init SQL),
  redis (appendonly), api-server (healthchecked), migrate (on-demand, `tools` profile).
- Compose healthcheck uses the binary itself: `/api-server -healthcheck`
  (pings PG + Redis, exits 0/1) — no shell/wget in the runtime image.

## Testing & Quality

- Unit tests live beside their packages (`*_test.go`), hermetic and
  deterministic; `go test -race ./...` is the CI gate.
- Integration tests that need PostgreSQL/Redis are future sprints (build-tagged).
- Lint: `golangci-lint run ./...` (config in `.golangci.yml`), fallback `go vet`.
- CI (`.github/workflows/ci.yml`): vet → golangci-lint → race+coverage → build →
  govulncheck → gitleaks → trivy.

## Conventions

Follow `architecture/ENGINEERING_RULES.md`. Highlights: dependency direction is
always inward (`delivery → application → domain`, `infra` implements ports,
`platform` is a leaf); errors are wrapped with `%w`, classified, and translated
to the API contract exactly once at the boundary; no secrets in code or logs;
new dependencies need written justification.
