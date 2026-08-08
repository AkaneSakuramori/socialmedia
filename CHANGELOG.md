# Changelog

All notable changes to this project are documented here, starting with Sprint 0.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning follows [SemVer](https://semver.org/). Newest entries appear first.

## [Unreleased]

### Sprint 2 - Core Messaging (milestone 2: message domain, persistence, receipts, and reactions)

#### Added
- **`server/migrations/000007_messages`** - `messages` with per-conversation
  sequence primary key, global delta sequence, per-sender client-message
  idempotency guard, mentions/reply metadata, edit/tombstone state, plus
  `message_edits` and `message_reactions`; matching `down.sql`.
- **Message domain and persistence** - message/reaction/receipt aggregates and
  ports; PostgreSQL message/reaction repositories; Redis `INCR` sequence hot
  path with PostgreSQL durable floor/fallback and composite-PK collision guard.
- **Message application use-cases** - atomic send and idempotent replay,
  history/delta pagination, get/edit/delete, reactions, read/delivery receipts,
  sender/read-status enrichment, and transactional `change_log` events for
  every write.
- **Message HTTP delivery** - REST routes for API sections 8 and 10: message
  list/send/get/edit/delete, reaction add/remove/list, and receipt update/list;
  field validation and `MESSAGE_NOT_FOUND` error mapping.
- **Test coverage** - domain, application, and HTTP handler unit suites; tagged
  PostgreSQL/Redis integration tests for repository invariants, keyset/delta
  ordering, idempotency, edit/tombstone behavior, reaction lifecycle,
  monotonic receipts, Redis loss/fallback, concurrent sends/retries, and
  send/receipt/edit/delete/reaction stress scenarios.
- **`architecture/SECURITY_OPERATIONS.md`** - operator-owned incident,
  database/PITR recovery, backup verification, credential/signing/encryption
  key rotation, disaster recovery, deployment, container hardening,
  supply-chain, monitoring, and release-evidence runbooks.

#### Changed
- **Go toolchain alignment** - Docker and GitHub Actions now use Go 1.25,
  matching `server/go.mod`; project setup documentation reflects the required
  toolchain so local, CI, and release builds cannot drift.
- **Transaction-safe fan-out reads** - added the dependency-free `tx.Querier`
  read surface and pool adapter. Transactional member/user/idempotency lookups
  now use the open transaction instead of taking additional pool connections;
  pre-transaction and post-commit reads use the injected pool querier.
- **Receipt cursor consistency** - marking a message read atomically advances
  delivery to at least the read sequence, matching the existing database
  constraint that delivery cannot trail read; both cursors remain monotonic.
- **Composition root** - the API now wires `MessageRepo`, `ReactionRepo`, the
  PostgreSQL/Redis sequence source, and the pool-backed read querier into chat.

#### Fixed
- **Domain route startup** - the platform HTTP server mounts feature routes at
  `/v1/` instead of registering the root pattern twice; a regression test now
  constructs the server with domain routes and verifies fallback 404 handling.
- **Durable send replay** - `MessageRepo.Insert` now uses `RETURNING id`; a
  partial-unique conflict correctly reports `inserted=false` instead of
  emitting a duplicate `message.created` event for a row not inserted.
- **Bounded-pool deadlock** - removed nested pool acquisition while write
  transactions held conversation/sequence locks, preventing concurrent sends
  from circularly waiting on the connection pool.
- **Read receipt writes** - replaced the two-step cursor update whose
  intermediate state violated `conversation_members` consistency checks with
  one atomic max-merge.

### Sprint 2 — Core Messaging (milestone 1: conversation domain, delivery wiring & database recovery)

#### Added
- **`server/migrations/000006_conversations`** — `conversations` (type CHECK,
  group-title CHECK, settings JSONB, retention_days, denormalized
  `last_message_*` with consistency CHECK), `conversation_members` (roles,
  monotonic `last_read_seq`/`last_delivered_seq` cursors, mute/pin/archive
  prefs, invite_state), `conversation_sequences` (durable per-conversation
  counter, DATABASE.md §5.4) and `change_log` (transactional outbox / sync feed
  per DATABASE.md §7.1: event_type CHECK, global_seq, affected_user_ids GIN);
  matching `down.sql`.
- **`server/internal/chat/domain`** — `Conversation`/`Membership` aggregates,
  `ConversationRepository`, `MembershipRepository`, `SequenceRepository`,
  `ChangeLogRepository`, ID generator port, sentinel errors + field-level
  `ValidationError` (API.md §2.5), settings parsing with defaults.
- **`server/internal/chat/application`** — the conversation use-cases: create
  (direct dedupe returns the existing conversation, §7.2), list (chat list with
  derived unread, filters, keyset pagination, §7.1), get detail (§7.3), group
  settings update (§7.4), member list/add/remove with role checks (§7.5–§7.7),
  role change (§7.8), mute/pin/archive prefs (§7.9–§7.11). Every write commits
  conversation + memberships + sequence row + `change_log` outbox atomically.
- **`server/internal/chat/delivery/http`** — REST handlers + routes for
  §7.1–§7.11 with bearer auth and Idempotency-Key gating on unsafe writes;
  `draft_message` explicitly rejected 422 `not_supported` (never silently
  dropped).
- **`server/internal/platform/httpapi`** — shared delivery helpers: bearer
  gateway, Idempotency-Key middleware (Stripe-style, per-user scope, Redis
  cache + concurrency lock, 5xx never cached), JSON list envelope, bounded
  decoding, id/limit parsing; `internal/platform/httpserver.New` gained an
  `extra http.Handler` mount.
- **Composition root wiring** — `server/internal/app/app.go` now wires the full
  stack: real postgres + redis adapters for auth OTP/`SessionRepo` and chat,
  Ed25519 `loadTokenFactory` (ephemeral dev key with warning, required
  `APP_JWT_PRIVATE_KEY` outside dev), all six migrations applied.
- **Tests** — unit suites for `httpapi` (auth gateway, error mapping, parsing,
  idempotency replay/scope/409-lock/corrupt-cache) and the chat delivery layer
  (round-trips for §7.1–§7.11); `//go:build integration` postgres suite for the
  chat repository (CRUD, group-title CHECK, direct-pair dedup, list
  ordering/pagination/filters, membership lifecycle + ON CONFLICT resurrect,
  prefs, sequence init, change_log append) and the auth session repo (now
  requires `APP_PG_DSN`; no committed credentials); `make test-integration`.
- **Database recovery** — postgres container confirmed compromised (miner
  toolchain, `/tmp/.xdiag`, busybox rootkit, C2 payload, cron persistence);
  scope verified (host + redis + api-server clean); compromised container +
  volumes destroyed; fresh official `postgres:16-alpine` + `redis:7-alpine`
  from pinned digests; all credentials rotated into gitignored
  `infra/docker/.env` (mode 600) with `${...}` env interpolation in compose and
  `01-bootstrap.sql` → `01-bootstrap.sh`; DB rebuilt from migrations only and
  verified clean; full runbook documented in `architecture/DEVOPS.md` §27.

#### Changed
- **`server/internal/auth/infra/postgres/session_repo.go`** — added
  `SuspendAllByUserID` / `SuspendOthersByUserID` so the session repository
  satisfies the domain port.
- **`server/internal/auth/infra/otp`** — real Redis-backed OTP verifier
  (SHA-256 hashed codes, atomic single-use GETDEL, 300s TTL, constant-time
  compare).
- **`server/go.mod`** — `miniredis` v2.38.0 added as a test-only dependency.

### Sprint 1 — Authentication & Identity (milestone 5: account security, recovery & production hardening)

#### Added
- **`server/migrations/000005_account_security`** — `auth_tokens` (single-use
  recovery/verification tokens: purpose CHECK, hashed token, JSONB data,
  expires_at/used_at; REC-6), `login_history` (per-login security-review trail;
  unknown-identifier failures carry a NULL user_id, MON-5) and `audit_logs`
  (immutable trail per DATABASE.md §8.5, AUTH-7/AUD-1) with their indexes;
  matching `down.sql`.
- **`server/internal/auth/application/account_security_flows.go`** — the
  account-security use-cases: `RequestPasswordReset` (forgot password; uniform
  response for known/unknown/deleted/suspended identifiers so the endpoint does
  not enumerate accounts, OWASP A07), `ResetPassword` (single-use token →
  new password, suspend every session and bump `token_version`; REC-1/4,
  PASS-4, SESS-4), `ChangePassword` (step-up re-auth AUTH-9, keeps the current
  session, suspends all others), `Request/ConfirmEmailChange` and
  `Request/ConfirmPhoneChange` (step-up + uniqueness check, verification token
  bound to the pending identifier, unique index arbitrates races),
  `DeleteAccount` (step-up, soft delete + session revocation + token bump,
  API.md §5.5), `RestoreAccount` (OTP-gated within the grace window, REC-1),
  `PurgeDeletedAccounts` (retention worker, DATABASE.md §4.1) and
  `ListLoginHistory` (own events only, newest first). Audit events
  `auth.password_reset_requested`/`auth.password_reset`/
  `auth.password_changed`/`auth.email_change_requested`/`auth.email_changed`/
  `auth.phone_change_requested`/`auth.phone_changed`/`account.deleted`/
  `account.restored`; best-effort security notifications (password change,
  new-device login, identifier change, account deletion).
- **`server/internal/auth/domain`** — `AuthToken` + `AuthTokenRepository`
  (Create/Consume; Consume is one atomic UPDATE, single-use and TTL-bounded,
  all invalid states reported identically as `ErrRecoveryTokenInvalid`),
  `GenerateOpaqueToken` (256-bit base64url, stored only as SHA-256 — the REFR-2
  pattern), `LoginEvent` + `LoginHistoryRepository` (best-effort writes),
  `RiskEvaluator`/`RiskDecision` (AUTH-11 risk-based escalation hook;
  `PermissiveRisk` default), `SecurityNotifier` (`NoopNotifier` default),
  `SessionRepository.SuspendOthersByUserID`/`SuspendAllByUserID`,
  `CredentialRepository.ReplacePassword`, `AuditEvent.ResourceType`, and the
  errors `ErrRecoveryTokenInvalid`, `ErrStepUpRequired`, `ErrAccountAlreadyDeleted`,
  `ErrAccountRestoreExpired`.
- **`server/internal/user/domain`** — `UserRepository.SetEmail`/`SetPhone`
  (race-safe via the unique index → `ErrIdentifierTaken`), `MarkDeleted`
  (soft delete), `Restore` (grace window), `FindDeletedByPhone`/`FindDeletedByEmail`
  (recovery lookups) and `PurgeDeleted` (hard-deletes the account and its
  dependent rows — credentials, sessions, recovery tokens — in one transaction).
- **`server/internal/auth/infra/postgres`** — `UserRepo` lifecycle methods,
  `AuthTokenRepo` (atomic `UPDATE … RETURNING` consume),
  `LoginHistoryRepo` and the best-effort `AuditLog` adapter.
- **`server/config`** — `APP_PASSWORD_RESET_TOKEN_TTL` (default 30m),
  `APP_CHANGE_VERIFICATION_TOKEN_TTL` (default 15m) and
  `APP_DELETION_GRACE_PERIOD` (default 30d), validated > 0.
- **Login wiring (AUTH-11)** — `Login` now evaluates the risk hook
  (new-device signal + IP/UA/method); a `StepUp` verdict returns
  `ErrStepUpRequired` without creating a session, a `Notify` verdict surfaces
  a new-device notification; every attempt records a `login_history` row and
  the success/failure audit entries carry `new_device`.
- **Tests** — ~24 application unit tests (reset no-enumeration + timing,
  single-use + expired + wrong-purpose + malformed tokens, reset suspends all
  sessions, change-password keeps current + suspends others + step-up,
  email/phone change round-trips + taken/unchanged + token reuse, delete +
  already-deleted + step-up, restore within grace + wrong OTP + expired grace,
  purge, login history ownership/order, risk step-up blocks the session) and 6
  new build-tagged integration tests against live PostgreSQL (SetPhone/SetEmail
  + unique-index arbitration, MarkDeleted/Restore/PurgeDeleted + grace window,
  token create/single-consume/uniform errors/expiry, login-history record+list,
  audit persistence).

#### Changed
- `Service` exposes the milestone-5 use-cases; `Deps` gained `AuthTokens`,
  `LoginHistory`, `Risk`, `Notifier` and the three recovery TTL/grace durations.
- `ValidatePassword` no longer reports `contains_identifier` when no identifier
  context exists (password change/reset have no identifier to compare against).
- Integration test seeding now also wipes `auth_tokens`/`login_history`/
  `audit_logs` leftovers so killed runs never break a rerun.

#### Verified
- Migration `000005_account_security` applied; `auth_tokens`, `login_history`
  and `audit_logs` verified in PostgreSQL. `make ci` green including the
  integration tests against the dev stack.

#### Added
- **`server/migrations/000004_sessions`** — `users.token_version` (global
  token version backing SESS-6 logout-all) and `user_sessions_state_updated_idx`
  on `(state, updated_at)` for the retention sweep; matching `down.sql`.
- **`server/internal/auth/application/sessions.go`** — device & session
  management use-cases (API.md §4.5–§4.8, SECURITY_SPEC SESS-3/SESS-6/SESS-9):
  `ListSessions` (own devices, newest-first, `current` flag), `RenameSession`
  (device name 1–64 chars, only own active sessions → `ErrSessionNotOwned`),
  `Logout` (current session revoked from token identity), `LogoutSession`
  (selected device, 404/403 semantics), `LogoutOtherSessions`,
  `LogoutAll` (bumps `users.token_version` and revokes every session in one
  transaction so all previously issued access tokens fail the `ver` check at
  gateways, JWT-5), `ExpireIdleSessions` (SESS-9 sliding idle timeout) and
  `PurgeRevokedSessions` (DATABASE.md §4.4 90-day retention). Audit events
  `auth.session_renamed` / `auth.logout` / `auth.session_revoked` /
  `auth.logout_others` / `auth.logout_all` / `auth.sessions_expired` /
  `auth.sessions_purged`.
- **`server/internal/auth/domain`** — `ErrSessionNotOwned`; `ValidateDeviceName`
  (1–64 chars); `SessionRepository` ports `ListByUser`, `FindByID` (FOR
  UPDATE), `RevokeByID` (ownership scoped in the WHERE clause, SESS-3),
  `RevokeOthersByUserID`, `Rename`, `ExpireIdle`, `Purge`; `TokenIssuer`
  `IssuePair` now embeds the caller's `token_version` as the `ver` access-token
  claim; `users.token_version` + `UserRepository.BumpTokenVersion`.
- **`server/config`** — `APP_SESSION_IDLE_TIMEOUT` (default 30d, validated ≤
  refresh TTL) and `APP_SESSION_RETENTION` (default 90d).
- **Tests** — ~16 application unit tests (list/order/current flag, rename
  validation + ownership + revoked, logout current/selected/foreign/others/all
  incl. tx-atomic token-version bump and rollback, idle expiry window, retention
  purge) and 5 new build-tagged integration tests against live PostgreSQL
  (list+rename, ownership-scoped revoke, keep-current on logout-others,
  idle-expire + retention purge, SESS-6 token-version bump atomicity).

#### Changed
- `IssuePair` gained a `tokenVersion` parameter (`ver` claim, JWT-5); register,
  login and refresh stamp the version read at issue time.
- `Service` interface exposes `ListSessions`, `RenameSession`, `Logout`,
  `LogoutSession`, `LogoutOtherSessions`, `LogoutAll`, `ExpireIdleSessions`,
  `PurgeRevokedSessions`; `Deps` gained `SessionIdleTimeout`/`SessionRetention`.
- Integration tests seed users and sessions with ids in disposable ranges and
  wipe leftovers at process start (a killed run can never break a rerun).

#### Verified
- Migration `000004_sessions` applied; `token_version` and the state/updated
  index verified in PostgreSQL. `make ci` green including the 9 integration
  tests against the dev stack.

### Sprint 1 — Authentication & Identity (milestone 3: refresh & reuse detection)

#### Added
- **`server/pkg/tx`** — the transaction abstraction gained a query surface
  (`Row`, `Rows`, `Exec`, `QueryRow`, `Query`) so repositories can run
  parameterized SQL through the same `Tx` boundary.
- **`server/internal/platform/postgres/tx.go`** — `pgxTx` adapter and
  `NewBeginner` mapping the pkg/tx interface onto `pgx` transactions.
- **`server/migrations/000003_refresh`** — `refresh_token_previous_hash` and
  `refresh_expires_at` on `user_sessions`, with btree indexes on both hash
  columns (previous-hash lookups power reuse detection) and the reverse-order
  `down.sql`.
- **`server/internal/auth/domain`** — `Session.RefreshTokenPreviousHash` and
  `RefreshExpiresAt`; errors `ErrRefreshTokenInvalid` (API.md §4.4, 401) and
  `ErrRefreshTokenReuse` (410); `SessionRepository` ports `FindByHash`,
  `FindByPreviousHash`, `Rotate` (compare-and-swap), `RevokeAllByUserID`.
- **`server/internal/auth/application/refresh.go`** — `Refresh` use-case
  (API.md §4.4, ARCHITECTURE.md §10.2): opaque shape gate before any storage
  (REFR-1), single-use rotation with atomic CAS so concurrent refreshes yield
  exactly one winner (REFR-4), reuse of an already-rotated token revokes every
  session of the user and surfaces `ErrRefreshTokenReuse` (REFR-5), sliding
  expiry (REFR-6), session-state and user-state gating (suspended →
  `ErrAccountSuspended`, deleted → invalid without state leak, AUTH-8).
  Best-effort audit of `auth.token_refresh` / `auth.token_reuse` /
  `auth.token_refresh_failed` (AUTH-7).
- **`server/internal/auth/infra/postgres/session_repo.go`** — real PostgreSQL
  `SessionRepository`: `FOR UPDATE` row locks, atomic CAS `Rotate` (family
  counter incremented in SQL), `RevokeAllByUserID`, null-aware hashes.
- **Tests** — ~15 application unit tests (rotation, chained rotations,
  reuse→revoke-all, malformed/unknown/expired/revoked tokens, suspended/deleted
  users, concurrent single-winner, rollback on infra failure) and 4 build-tagged
  integration tests against live PostgreSQL (roundtrip, atomic CAS, revoke-all,
  6-way concurrent rotation with exactly one winner).

#### Changed
- `Register` and `Login` now stamp `refresh_expires_at` on new sessions; `Login`
  rotation of an existing device session records the rotated-out token as
  `refresh_token_previous_hash`.
- `Service` interface exposes `Refresh(ctx, RefreshCommand)`; `Deps` unchanged.
- `infra/docker/postgres/init/01-bootstrap.sql` — default privileges for role
  `migrator` grant `app` full DML on tables/sequences migrator creates
  (migrations run as `migrator`; previously the runtime role could not write
  migrated tables). Applied retroactively to the live dev DB.

#### Verified
- Migration `000003_refresh` applied; columns and indexes verified in
  PostgreSQL. Integration tests green against the dev stack; `make ci` green.

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
