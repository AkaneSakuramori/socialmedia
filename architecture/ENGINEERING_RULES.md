# InChat — Engineering Rules & AI Development Guide

| | |
|---|---|
| **Document** | Engineering Rules & AI Development Guide v1.0 |
| **Audience** | Every engineer and every AI coding agent working on the repository |
| **Status** | **Official rules of engagement. Binding on all contributors, human and automated.** |
| **Source of Truth** | `ARCHITECTURE.md` · `DATABASE.md` · `API.md` · `ENGINEERING.md` · `FLUTTER.md` · `DEVOPS.md` · `QA.md` · `SECURITY.md` · `SECURITY_SPEC.md` → this guide. Do not redesign. |
| **Stack (fixed)** | Go · Flutter (mobile) · TypeScript (web client) · PostgreSQL · Redis · Docker · Terraform · Cloudflare · WebSockets |
| **Launch** | India first (single region) → global scale later |
| **Scope** | Rules, standards, and process. No product or architecture decisions are restated here. |

> This guide is the **house law** for the repository. It tells every human engineer and every AI coding agent exactly how to work: what is **MUST**, what is **SHOULD**, and what is **MUST NOT**. Where a product decision must be made, the source-of-truth documents win and this guide never overrides them; where a rule is stated here, it overrides any habit, tool, or generated output that contradicts it. AI agents that follow these rules produce mergable work; agents that ignore them produce rejected PRs.

---

## Table of Contents

1. [Purpose & Authority](#1-purpose--authority)
2. [Engineering Principles](#2-engineering-principles)
3. [Architecture Rules](#3-architecture-rules)
4. [Project Structure Rules](#4-project-structure-rules)
5. [Module Boundaries & Package Rules](#5-module-boundaries--package-rules)
6. [Coding Standards — Go](#6-coding-standards--go)
7. [Coding Standards — Flutter / Dart](#7-coding-standards--flutter--dart)
8. [Coding Standards — TypeScript (Web)](#8-coding-standards--typescript-web)
9. [Naming Conventions](#9-naming-conventions)
10. [Error Handling Rules](#10-error-handling-rules)
11. [Logging Standards](#11-logging-standards)
12. [Security Rules](#12-security-rules)
13. [Dependency Rules](#13-dependency-rules)
14. [Code Review Checklist](#14-code-review-checklist)
15. [Testing Requirements](#15-testing-requirements)
16. [Documentation Requirements](#16-documentation-requirements)
17. [API Implementation Rules](#17-api-implementation-rules)
18. [Database Migration Rules](#18-database-migration-rules)
19. [Git Workflow & Branch Naming](#19-git-workflow--branch-naming)
20. [Commit Message Conventions](#20-commit-message-conventions)
21. [Pull Request Standards](#21-pull-request-standards)
22. [Performance Requirements](#22-performance-requirements)
23. [Scalability Principles](#23-scalability-principles)
24. [Refactoring Rules](#24-refactoring-rules)
25. [Deprecation Policy](#25-deprecation-policy)
26. [Versioning Policy](#26-versioning-policy)
27. [Release Workflow](#27-release-workflow)
28. [AI Coding Rules](#28-ai-coding-rules)
29. [Appendix A — Precedence & Terminology](#appendix-a--precedence--terminology)
30. [Appendix B — The Non-Negotiable AI Rules (Quick Card)](#appendix-b--the-non-negotiable-ai-rules-quick-card)
31. [Appendix C — Source-of-Truth Index](#appendix-c--source-of-truth-index)

---

## 1. Purpose & Authority

**1.1** This guide is the single set of engineering rules for the repository. It applies to *all* commits, PRs, and shipped code — written by a human or an AI agent. Generated output is **not** exempt; it must conform or it is rejected.

- **MUST:** Read this guide (and `AGENTS.md`) before the first commit. AI agents MUST load it as context for every task.
- **MUST:** Follow the *process* rules in this guide even when they conflict with the tool's or agent's defaults (formatters, linters, and git defaults are configured to match this guide; do not disable them).
- **MUST NOT:** Rely on "this is how the generator wrote it." Everything generated is reviewed and corrected against these rules.
- **SHOULD:** Treat a rule violation you discover as a bug to fix, not a precedent to copy.

**Precedence:** Product/architecture decisions → source-of-truth documents. Process, style, and standards → this guide. On conflict between the two, the source-of-truth document wins *only* for product meaning; this guide always governs *how* it is built. Raise any genuine conflict as a PR, never silently pick a side.

---

## 2. Engineering Principles

**2.1** Six principles, in priority order:

1. **Correctness** — the system must be provably right before it is fast, clever, or pretty.
2. **Simplicity** — choose the smallest solution that satisfies the requirement (`ENGINEERING.md` §1).
3. **Readability** — code is read ten times more often than it is written. Readability is a feature.
4. **Testability** — anything that cannot be tested is design debt. Design for tests (`QA.md` §2).
5. **Observability** — every subsystem can be inspected in production (`ARCHITECTURE.md` §29).
6. **Security** — secure by default, everywhere (`SECURITY_SPEC.md`).

**2.2** Derived rules:

- **MUST:** Prefer boring, well-understood technology over novelty. The stack is fixed; do not propose replacements for flair.
- **MUST:** Make the smallest change that works. YAGNI.
- **MUST:** Duplicate **presentation**, never **logic**. Business logic exists once, in the service layer (`ENGINEERING.md` §7, §4).
- **MUST:** Prefer readability over cleverness. If a reviewer must ask "how does this work?", rewrite it.
- **MUST:** Fail loudly at the boundaries, degrade gracefully inside (`ENGINEERING.md` §14).
- **MUST NOT:** Optimize before measuring. Profile first, then optimize, then re-test.

---

## 3. Architecture Rules

**3.1** The architecture is defined by `ARCHITECTURE.md` and `ENGINEERING.md`. It is a **modular monolith** — one deployable Go service with strict internal modules — that may later decompose (`ENGINEERING.md` §45).

- **MUST:** Follow the layered flow `handler → service → repository → store` (`ENGINEERING.md` §5). No layer skips to another module's internals.
- **MUST:** Preserve the fixed stack: Go backend, Flutter mobile client, TypeScript web client, PostgreSQL, Redis, Docker, Terraform, Cloudflare, WebSockets.
- **MUST:** Keep the realtime path on WebSockets as specified in `ENGINEERING.md` §18 and `ARCHITECTURE.md`.
- **MUST:** Treat `API.md` as the API contract. Client and server MUST implement it; neither may drift.
- **MUST:** Get architectural decisions reviewed by at least one architect-aware reviewer.
- **MUST NOT:** Introduce microservices, message brokers, new data stores, or new languages without a written architecture change and its own review.
- **MUST NOT:** Fork the architecture in a feature branch "just to try it."

---

## 4. Project Structure Rules

**4.1** The repository is a monorepo. The top-level layout is fixed:

```
server/            # Go backend (modular monolith)
app/               # Flutter mobile client
web/               # TypeScript web client (TanStack Start)
infra/             # Terraform, Dockerfiles, CI/CD, deploy configs
architecture/      # Source-of-truth documentation (this guide's peers)
docs/              # Team documentation, ADRs, runbooks
README.md
AGENTS.md
```

- **MUST:** Put every file in its designated tree. A new file outside its tree is a rejected PR.
- **MUST:** Mirror the Go package layout defined in `ENGINEERING.md` §2–§3 (cmd/ vs internal/ vs pkg/).
- **MUST:** Keep the web client under its own subdirectory with its own lockfile, as in the existing web client repo.
- **SHOULD:** Keep directory names lowercase; singular for packages, plural for feature folders only where the existing layout already does.
- **MUST NOT:** Create `src/pages/`, `app/layout.tsx`, or other foreign-framework conventions in the TanStack web client; it uses file-based routes under `src/routes/` (`web/AGENTS.md`).

---

## 5. Module Boundaries & Package Rules

**5.1** A **module** is a domain feature with a public API and no public internals. Boundaries are enforced by review and CI (import linting).

- **MUST:** Follow the domain package boundaries and dependency direction in `ENGINEERING.md` §4–§6. Domains do not import each other's internals; they communicate through the service layer's public interface.
- **MUST:** Keep `internal/` packages strictly internal. Nothing outside the module imports them.
- **MUST:** Keep the repository pattern at the boundary (`ENGINEERING.md` §6); the service layer is the **only** owner of business logic.
- **MUST:** Prefer small packages with a single responsibility; split when a package exceeds one clear concern.
- **MUST NOT:** Import infrastructure (database, cache, transport) inside domain packages — depend on the abstraction, not the implementation.
- **MUST NOT:** Create import cycles. CI blocks them; design interfaces instead.
- **MUST NOT:** Reach across a module boundary into a sibling's unexported state.

---

## 6. Coding Standards — Go

- **MUST:** Run `gofmt` + `go vet` before commit; CI runs `gofmt -l`, `go vet`, and the project linters. Formatting drift fails the build (`ENGINEERING.md` §36).
- **MUST:** Use `context.Context` as the first parameter of every function that touches I/O, propagate it, and never store it in structs (`ENGINEERING.md` §25).
- **MUST:** Wrap errors with `%w` to preserve the chain; never discard the cause (`ENGINEERING.md` §14).
- **MUST:** Prefer the standard library first, then the pinned project libraries; add nothing new without §13.
- **MUST:** Keep handlers thin, services explicit, repositories persistence-only (`ENGINEERING.md` §7–§8).
- **MUST:** Use the project's dependency injection wiring (`ENGINEERING.md` §10) — no ad-hoc singletons or globals.
- **MUST:** Follow the concurrency rules in `ENGINEERING.md` §24 (goroutine ownership, no leaks, no mutable shared state without sync).
- **MUST NOT:** Use `panic` for error control; return errors.
- **MUST NOT:** Store secrets in code or env files committed to the repo (`ENGINEERING.md` §31, `SECURITY.md`).
- **MUST NOT:** Ignore linter findings with `nolint` unless the justification is written inline and approved.

---

## 7. Coding Standards — Flutter / Dart

- **MUST:** Enable `flutter_lints` / the project's `analysis_options.yaml` and keep the analyzer clean; CI enforces it.
- **MUST:** Use the Material 3 design system, theme tokens, and reusable widgets from the design system — no ad-hoc colors, radii, or spacing (`FLUTTER.md`).
- **MUST:** Follow `FLUTTER.md` §20–§22 for lifecycle, offline, and sync behavior — the app's core reliability claims.
- **MUST:** Use immutable models; state changes go through `setState`, providers, or the state-management solution chosen in `FLUTTER.md` — never mutate in place.
- **MUST:** Use `async/await`; check `context.mounted` (or capture state) after every await before touching the widget tree.
- **MUST:** Dispose controllers, timers, and subscriptions; no resource leaks.
- **MUST:** Localize user-facing strings through the i18n pipeline; no hard-coded text in widgets.
- **MUST NOT:** Put business logic in widgets; widgets render, services decide.
- **MUST NOT:** Import backend SDKs or make network calls directly in the widget tree; use the repository/service layer (`FLUTTER.md` §architecture).

---

## 8. Coding Standards — TypeScript (Web)

- **MUST:** Compile with `strict` TypeScript. No `any`, no `@ts-ignore`, no unused or implicit `any` escaping to the public API.
- **MUST:** Use the TanStack Start conventions of the web client: file-based routes, route loaders, TanStack Query for server state, typed fixtures replaced by API hooks.
- **MUST:** Keep the ESLint + Prettier configs as configured; `npm run lint` and `npm run format` are part of the merge gate.
- **MUST:** Use Tailwind design tokens from the shared theme — no arbitrary hex values in markup.
- **MUST:** Keep components presentational; data access lives in hooks/loaders, not inside JSX.
- **MUST NOT:** Add state-management libraries beyond those already pinned (React Query + TanStack Router) without §13.
- **MUST NOT:** Re-create business rules in the web client; the API is the single source of truth, mirrored from `API.md`.
- **SHOULD:** Prefer typed schema validation (e.g., zod) at API boundaries over hand-rolled checks.

---

## 9. Naming Conventions

**9.1** Follow `ENGINEERING.md` §37 exactly. Summary by language:

- **Go:** `camelCase` unexported, `PascalCase` exported; package names short, single word, lowercase, equal to the last directory segment; interfaces named for behavior (`Reader`, `Sender`), not `IXxx`.
- **Dart/Flutter:** `lowerCamelCase` for functions/variables, `PascalCase` for classes/types, `snake_case` for files, `SCREAMING_SNAKE` for constants.
- **TypeScript:** `camelCase` functions/variables, `PascalCase` types/classes/components, `kebab-case` route file names, `camelCase` API types.

**9.2** Universal:

- **MUST:** Name things for *what they are*, not how they are built. No `DataManagerUtil`, `helper.cpp`, `temp`, `foo`.
- **MUST:** Boolean-prefix predicates: `is*`, `has*`, `can*`, `should*`.
- **MUST:** Match existing names when extending behavior. Renaming for taste is a refactor and needs its own PR.
- **MUST NOT:** Use abbreviations not already used in the codebase (`usr`, `cfg`, `msg` are banned unless established).

---

## 10. Error Handling Rules

**10.1** Errors are typed, wrapped, and translated exactly once — at the API boundary.

- **MUST:** Use the project's error taxonomy: `validation`, `auth`, `authz`, `not_found`, `conflict`, `rate_limited`, `internal`. Every error maps to one of these.
- **MUST:** Wrap with `%w` at each layer and log with the full cause chain (`ENGINEERING.md` §14).
- **MUST:** Translate errors to the stable API error envelope (`API.md`) in the handler — nothing else emits wire errors.
- **MUST:** Return typed errors from services; services never write HTTP or WebSocket responses.
- **MUST:** Handle every error path in clients (Flutter and web): timeout, offline, conflict, 429, 5xx — each has a defined UX (`FLUTTER.md`, `QA.md` §18).
- **MUST NOT:** Swallow errors (`_ = fn()`, empty catch, bare `defer close()` without handling).
- **MUST NOT:** Leak internal details (stack traces, SQL, package paths) to clients.
- **MUST NOT:** Recover-and-continue across unknown failures silently; log loudly.

---

## 11. Logging Standards

**11.1** Per `ENGINEERING.md` §13.

- **MUST:** Log structured JSON to stdout; no hand-rolled text logging in application code.
- **MUST:** Use the project's leveled logger (`debug/info/warn/error`); default to `info`, not `debug`, for events that ship.
- **MUST:** Propagate `request_id` / `connection_id` / `user_id` through context and include them in every log line for a request (`ENGINEERING.md` §13, §25).
- **MUST:** Log at boundaries (handlers, workers, WebSocket events), not inside hot loops; rate-limit per-connection logs.
- **MUST:** Log security-relevant events (auth failures, permission denials, password resets, admin actions) to the security channel (`SECURITY_SPEC.md`).
- **MUST NOT:** Log PII, message bodies, tokens, or secrets — ever. Redact or drop.
- **MUST NOT:** Log at `error` for expected/recoverable conditions (retryable failures are `warn` at most).

---

## 12. Security Rules

**12.1** `SECURITY.md` and `SECURITY_SPEC.md` are binding and testable. Key rules:

- **MUST:** Apply secure-by-default: least privilege, fail-closed, encrypted in transit (TLS) and at rest (`SECURITY_SPEC.md`).
- **MUST:** Validate every input at the boundary (server-side) — never trust client or WebSocket payloads.
- **MUST:** Use the pinned authentication/authorization middleware (`ENGINEERING.md` §16–§17) — never roll custom auth.
- **MUST:** Store only hashed, salted passwords; use the E2EE/crypto primitives specified in `SECURITY_SPEC.md` — never invent cryptography.
- **MUST:** Follow OWASP Top 10 controls for the API and web client (XSS, CSRF, injection, SSRF, broken access control).
- **MUST:** Keep secrets in the secrets manager / env pipeline from `DEVOPS.md` — never in source, lockfiles, or logs.
- **MUST:** Run `go vulncheck` / `npm audit` / dependency audits on a schedule; fix criticals before merge.
- **MUST NOT:** Disable CSP, TLS, or CSRF protection to "make it work locally."
- **MUST NOT:** Add a new cryptographic primitive or protocol without the security engineer's sign-off.

---

## 13. Dependency Rules

- **MUST:** Get written justification for **every new dependency** — human or AI added — in the PR description: what it solves, alternatives considered, why not stdlib, license, maintenance status, and supply-chain check result.
- **MUST:** Prefer stdlib (Go) and the existing pinned libraries first. Reuse what is already in the repo.
- **MUST:** Pin versions (go.mod / lockfiles) and commit lockfiles. Never "latest", never floating.
- **MUST:** Prefer small, focused, actively maintained libraries over frameworks and kitchen sinks.
- **MUST:** Review new transitive dependencies for scope and risk in the same PR.
- **SHOULD:** Run regular dependency audits; upgrade in dedicated PRs, not bundled with features.
- **MUST NOT:** Add a dependency to satisfy a one-liner that the language already covers.
- **MUST NOT:** Copy third-party code into the repo without license-compatible attribution.
- **MUST NOT:** Add Lovable or AI-builder SDKs, telemetry, or runtime scaffolding into application code (the web client was explicitly de-coupled; keep it that way).

---

## 14. Code Review Checklist

Every PR is checked against all of the following before approval:

- **Architecture:** does it respect the layered flow and module boundaries (§3, §5)?
- **Contract:** does it match `API.md` / `DATABASE.md` — no silent drift?
- **Behavior:** does it do what the linked issue/requirement says, nothing more?
- **Security:** inputs validated, authz enforced, no secrets, no crypto invention (§12)?
- **Errors:** typed, wrapped, translated at the boundary, never swallowed (§10)?
- **Logging:** structured, contextual, no PII (§11)?
- **Tests:** tests added/updated for the change; new behavior proven; bug fixes carry a regression test (§15)?
- **Docs:** architecture-adjacent changes updated the source-of-truth docs (§16)?
- **Naming & style:** matches §9 and the language linters?
- **Dependencies:** any new dependency justified (§13)?
- **Dead code / duplication:** no copied logic, no leftover scaffolding, no commented-out code?
- **Performance:** no N+1, no hot-loop I/O, no obvious regressions (§22)?

- **MUST NOT:** Approve a PR that fails any *must* item, even if the author is an AI agent.

---

## 15. Testing Requirements

**15.1** Per `QA.md` (the testing standard) and `ENGINEERING.md` §32–§35.

- **MUST:** Follow the test pyramid — many fast unit tests, fewer integration/API tests, fewest E2E. Most coverage is unit (`QA.md` §2).
- **MUST:** Write tests for the hot paths: send, read receipts, sync, realtime dispatch, offline recovery.
- **MUST:** Test behavior, not implementation; use fakes, not brittle mocks (`ENGINEERING.md` §35).
- **MUST:** Keep unit tests hermetic and deterministic — no wall-clock flakiness, no network in unit tests.
- **MUST:** Provide a regression test with every bug fix.
- **MUST:** Keep the CI suite green. A failing test blocks merge; a flaky test is a bug (`QA.md` §22).
- **MUST:** Cover the Flutter client (`FLUTTER.md` §22), the web client (component + Playwright E2E), and the API/WebSocket layers.
- **MUST:** Gate releases on the quality gates in `QA.md` §28 (coverage thresholds, linters, security scan).
- **SHOULD:** Test failure paths and edge cases as seriously as happy paths.
- **MUST NOT:** Delete or weaken tests to pass; disable tests only with an approved, tracked exception.

---

## 16. Documentation Requirements

- **MUST:** Keep the source-of-truth documents (`architecture/`) synchronized with the code. An architectural change without a doc update is not mergeable.
- **MUST:** Document non-obvious design decisions in an ADR in `docs/` — context, decision, consequences. AI agents MUST record any decision they make that affects more than the file they edited.
- **MUST:** Update `README.md` when user-facing capabilities, commands, or structure change.
- **MUST:** Write `AGENTS.md` guidance per project tree so AI agents get project-specific context on load.
- **SHOULD:** Document APIs as they land, in the API reference, not only in code comments.
- **MUST NOT:** Leave "TODO/FIXME" in merged code; convert to issues.
- **MUST NOT:** Write documentation that contradicts the code; docs that lie are worse than no docs.

---

## 17. API Implementation Rules

**17.1** The API is defined by `API.md`. Implementation must match it byte-for-byte in contract.

- **MUST:** Version the API (`/v1/...`); breaking changes require a new version, never a silent break (`ENGINEERING.md` §41, §45).
- **MUST:** Use JSON for request/response; consistent camelCase field names; timestamps in ISO-8601 UTC.
- **MUST:** Return the stable error envelope with a stable error code on every failure (`API.md`).
- **MUST:** Paginate list endpoints (cursor-based) with stable defaults; no unbounded lists.
- **MUST:** Support idempotency keys on state-changing endpoints where specified (`ENGINEERING.md` §29).
- **MUST:** Enforce rate limits (`ENGINEERING.md` §28) and return `429` with `Retry-After`; clients MUST honor it.
- **MUST:** Validate with the schema/struct rules of the language; return `422`/`400` with field-level messages.
- **MUST:** Implement WebSocket events and message shapes exactly per `ARCHITECTURE.md` and `API.md` — events are part of the public contract.
- **MUST NOT:** Invent ad-hoc endpoints instead of extending the documented contract.

---

## 18. Database Migration Rules

**18.1** Per `DATABASE.md` and `DEVOPS.md`.

- **MUST:** Write every schema change as a numbered migration committed with the code that needs it.
- **MUST:** Make migrations immutable once merged. Never edit a shipped migration; add a new one.
- **MUST:** Keep migrations forward-only in production. Destructive changes (drops, renames) ship in their own, separate release window.
- **MUST:** Make each migration reversible in review (a `down` for safety during review), but the production policy is forward-only.
- **MUST:** Review migrations in a PR like code — index strategy, lock behavior, backfill plan, and rollback plan included.
- **MUST:** Test migrations against a copy of production data before release.
- **MUST:** Prefer additive, online-safe changes (new columns nullable or defaulted, new tables, batched backfills) — no long table locks.
- **MUST NOT:** Embed credentials or environment specifics in migration files.

---

## 19. Git Workflow & Branch Naming

**19.1** Trunk-based with short-lived branches (`ENGINEERING.md` §38).

- **MUST:** Keep `main` always green and deployable. Merge only via PR.
- **MUST:** Branch from `main`; name branches per convention:
  - `feature/<slug>` — new capability
  - `fix/<slug>` — bug fix
  - `chore/<slug>` — tooling, refactor, docs
  - `release/vX.Y.Z` — release prep
  - `hotfix/<slug>` — urgent production fix
- **MUST:** Keep branches short-lived (days, not weeks); rebase or merge `main` in to stay current.
- **MUST NOT:** Commit directly to `main`.
- **MUST NOT:** Rewrite published history (no force-push, no rebase of pushed branches) — this is especially critical now that the web client's repository is shared.
- **MUST NOT:** Leave stale branches; delete after merge.

---

## 20. Commit Message Conventions

**20.1** Use Conventional Commits (`ENGINEERING.md` §38):

```
<type>(<scope>): <imperative summary>

<body — why, not what>

<optional footer: refs / breaking change notice>
```

- **Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
- **MUST:** Write the summary in imperative ("add", "fix", "remove"), ≤ 72 chars, lowercase, no period.
- **MUST:** One logical change per commit; split unrelated changes.
- **MUST:** Use `feat!`/`BREAKING CHANGE:` when the commit changes the public contract.
- **SHOULD:** Reference the issue/PR number in the footer.

---

## 21. Pull Request Standards

- **MUST:** Open a PR for every change; no direct pushes to `main`.
- **MUST:** Keep PRs small and focused (a rule of thumb: reviewable in one sitting). Split large features.
- **MUST:** Fill the PR template: summary, motivation, contract impact, test evidence, screenshots/videos for UI, doc updates.
- **MUST:** Pass all CI checks (lint, tests, build, security scan, formatting) before requesting review.
- **MUST:** Receive at least one approval from a reviewer able to judge the change; AI-written PRs are reviewed with the same rigor.
- **MUST:** Address every review comment; a resolved thread with no answer is not closed.
- **SHOULD:** Mark WIP/draft until the change is ready; request re-review after substantive changes.
- **MUST NOT:** Self-merge without approval (except trivial `chore` merges explicitly allowed by the team).
- **MUST NOT:** Bundle an unrelated refactor with a feature in the same PR.

---

## 22. Performance Requirements

**22.1** Latency budgets and SLOs come from `ARCHITECTURE.md` §29, §32 and `ENGINEERING.md` §43.

- **MUST:** Meet the documented P95 latency budgets for send, read, sync, and WebSocket connect.
- **MUST:** Avoid N+1 queries; batch and index per `ENGINEERING.md` §43 and `DATABASE.md`.
- **MUST:** Use Redis only for the defined cache/queues (`ENGINEERING.md` §22); no hot-path I/O without a cache-first plan.
- **MUST:** Keep the web client bundle lean (route-level code splitting is already enforced by the router); no heavy libs on the critical path.
- **MUST:** Measure before and after any performance change; a "fast" rewrite without numbers is not accepted.
- **SHOULD:** Keep endpoints idempotent and safe to retry so failures don't multiply load.
- **MUST NOT:** Ship known O(n²) paths, unbounded memory buffers, or unbounded goroutine/worker spawning.

---

## 23. Scalability Principles

**23.1** Per `ENGINEERING.md` §44 and `ARCHITECTURE.md`.

- **MUST:** Design for horizontal scaling: stateless application services; state in PostgreSQL/Redis, never in process memory.
- **MUST:** Scale reads with Redis; scale realtime with horizontal WebSocket gateways that share presence/subscription state via Redis pub/sub.
- **MUST:** Fan out message delivery asynchronously (worker queues), not inline in the request path.
- **MUST:** Use exponential backoff + jitter for all retries (`ENGINEERING.md` §26).
- **MUST:** Define per-user and global rate limits (`ENGINEERING.md` §28).
- **SHOULD:** Degrade gracefully under pressure (reduced features, backpressure) rather than fail hard.
- **MUST NOT:** Assume a single instance can hold conversations in memory; the modular monolith scales by running more instances, not by growing one.

---

## 24. Refactoring Rules

- **MUST:** Refactor only with a green test suite; run the suite before and after.
- **MUST:** Keep refactors behavior-preserving; a refactor PR contains no feature changes.
- **MUST:** State intent in the PR ("extract X", "remove dead branch Y") and link the ADR if structure changes.
- **MUST:** Remove dead code when you find it, in its own small change.
- **SHOULD:** Refactor opportunistically but incrementally; avoid large sweeping rewrites.
- **MUST NOT:** Refactor and add a feature in the same PR.
- **MUST NOT:** Rewrite working code for taste; only refactor when it improves maintainability measurably.

---

## 25. Deprecation Policy

- **MUST:** Deprecate publicly visible behavior (API endpoints, fields, env vars, config keys) for a minimum of **two releases** with a deprecation notice before removal.
- **MUST:** Log a deprecation warning server-side and document it in the changelog when a deprecated feature is used.
- **MUST:** Remove deprecated code only in a **major** version bump.
- **SHOULD:** Provide a migration path in the deprecation notice (what to use instead).
- **MUST NOT:** Remove something the docs still advertise.

---

## 26. Versioning Policy

**26.1** Per `ENGINEERING.md` §41.

- **MUST:** Use semantic versioning for the platform and its packages: `MAJOR.MINOR.PATCH`.
- **MUST:** Version the API separately (`/v1`, `/v2`) — breaking API changes increment the API version, not silently.
- **MUST:** Maintain a changelog that documents every user-visible change with its PR reference.
- **MUST:** Align app version numbers across Flutter and web builds with the release they ship in.
- **MUST NOT:** Break the compatibility window (`QA.md` §24) without a documented migration.

---

## 27. Release Workflow

**27.1** Per `DEVOPS.md` §8 and `QA.md` §23–§26.

- **MUST:** Ship through the gated pipeline: build → test → security scan → quality gates → image → deploy.
- **MUST:** Deploy in stages (canary → controlled % → full), with automatic rollback on SLO/crash breach.
- **MUST:** Cut a `release/vX.Y.Z` branch, tag the commit, and write release notes from the changelog.
- **MUST:** Treat every release as release + watch: dashboards, SLO alerts, crash reporting monitored during the post-release window.
- **MUST:** Revert with a `hotfix/` PR (or re-release), never by editing `main` history.
- **MUST NOT:** Deploy to production from a feature branch or a local machine.
- **MUST NOT:** Deploy without the release's tests having passed on the exact artifact being shipped.

---

## 28. AI Coding Rules

**28.1** The following rules bind every AI coding agent (and every human applying AI assistance) on this repository. They are non-negotiable.

### 28.2 MUST

- **MUST** treat the source-of-truth documents and this guide as binding context for every task; when in doubt, read the docs first.
- **MUST** preserve the architecture: the layered flow, module boundaries, fixed stack, and API contract. Never restructure "creatively."
- **MUST** put code in the existing structure and follow existing conventions — inspect neighboring files and match their style exactly.
- **MUST** justify every new dependency in the change description (what/why/alternatives/license/scope) before adding it.
- **MUST** reuse existing services, repositories, utilities, and design tokens instead of duplicating them.
- **MUST** update tests whenever behavior changes, and add a regression test for any bug fixed.
- **MUST** update documentation whenever an architectural or contract-level behavior changes (ADR for decisions).
- **MUST** keep functions small, files focused, and changes minimal; stop at the smallest correct change.
- **MUST** prefer readable, obvious code over clever constructs; name for intent.
- **MUST** explain significant architectural decisions in the PR description or ADR — a reviewer must be able to see the reasoning.
- **MUST** run the repository's formatters, linters, and tests and leave them green before finishing.

### 28.3 SHOULD

- **SHOULD** ask before acting when a task conflicts with a documented rule or is genuinely ambiguous, instead of guessing.
- **SHOULD** surface dead code, duplication, or risks found incidentally — as a note, not by silently expanding scope.
- **SHOULD** prefer the boring solution; propose the novel one only with evidence.

### 28.4 MUST NOT

- **MUST NOT** violate the architecture, break module boundaries, or bypass the service layer to "get it done."
- **MUST NOT** introduce new dependencies without justification.
- **MUST NOT** duplicate business logic that already exists in the service layer or shared modules.
- **MUST NOT** ship a change with failing or unupdated tests.
- **MUST NOT** commit secrets, tokens, or credentials.
- **MUST NOT** rewrite working code, reformat untouched files, or "clean up" beyond the task's scope.
- **MUST NOT** invent APIs, endpoints, schema fields, or env vars that the docs do not define.
- **MUST NOT** silence linters or bypass type checks to make code pass.
- **MUST NOT** claim a change is complete without running the verification commands the repo provides.

---

## Appendix A — Precedence & Terminology

**Precedence order (lowest → highest):**

1. This guide's *preferences* (SHOULD).
2. This guide's *rules* (MUST / MUST NOT) — binding.
3. Source-of-truth documents for **meaning** of the product/architecture (`ARCHITECTURE.md`, `DATABASE.md`, `API.md`, `ENGINEERING.md`, `FLUTTER.md`, `DEVOPS.md`, `QA.md`, `SECURITY.md`, `SECURITY_SPEC.md`).
4. Product intent (`PRD`, `UI/UX`, market research) for what to build and why.

**Terminology**

- **MUST** — unconditional requirement; violation blocks merge/release.
- **SHOULD** — strong recommendation; deviation requires a stated, reviewed reason.
- **MUST NOT** — absolute prohibition; violation is a defect.
- **Source of truth** — the document that owns a decision; other artifacts derive from it and must not contradict it.
- **Module** — a domain feature with a public interface and private internals (§5).
- **Contract** — the documented API/WebSocket/database surface that clients and server agree on.

---

## Appendix B — The Non-Negotiable AI Rules (Quick Card)

> Print this card into every agent's context. A single violation = reject the PR.

1. Never violate the architecture.
2. Never introduce a new dependency without justification.
3. Never duplicate business logic.
4. Never break module boundaries.
5. Always update tests when changing functionality.
6. Always update documentation when changing architecture.
7. Follow the existing coding style.
8. Keep functions and files maintainable.
9. Prefer readability over cleverness.
10. Explain significant architectural decisions.

---

## Appendix C — Source-of-Truth Index

| Document | Owns |
| --- | --- |
| `ARCHITECTURE.md` | System architecture, realtime, SLOs, latency budgets |
| `DATABASE.md` | Schema, data model, indexing, migrations |
| `API.md` | REST/WebSocket contract, error envelope, pagination |
| `ENGINEERING.md` | Backend structure, layers, DDD, logging, errors, testing, git, versioning |
| `FLUTTER.md` | Flutter architecture, UI, offline/sync, app testing |
| `DEVOPS.md` | CI/CD, Docker, Terraform, deploy, observability, rollback |
| `QA.md` | Test strategy, quality gates, release process |
| `SECURITY.md` | Security program, operations, response |
| `SECURITY_SPEC.md` | Testable security & cryptography requirements |
| `ENGINEERING_RULES.md` (this guide) | Rules of engagement: process, style, AI behavior |

---

*End of Engineering Rules & AI Development Guide. The house law for the repository — for every human engineer and every AI coding agent. Where a rule is stated here, it wins; where a decision must be made, the source-of-truth documents win. Raise conflicts as a PR, never silently.*
