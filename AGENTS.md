# AGENTS.md — InChat Repository Guide

This repository is the home of the **InChat** messaging platform (India-first):
the finalized engineering documentation (`architecture/`) and, from Sprint 0,
the **Go backend foundation** (`server/`). The Flutter mobile app and the
TypeScript web client (in its own repository) are built from these documents.

## Before you write anything

1. Read `architecture/ENGINEERING_RULES.md` first — it is the **house law** for
   every engineer and every AI agent. It contains the MUST/SHOULD/MUST NOT rules,
   module boundaries, naming, error/logging/security standards, git/PR/commit
   conventions, and the non-negotiable AI coding rules.
2. The source-of-truth documents live in `architecture/`:

   | Document | Owns |
   | --- | --- |
   | `ARCHITECTURE.md` | System architecture, realtime (WebSockets), SLOs |
   | `DATABASE.md` | PostgreSQL schema and data model |
   | `API.md` | REST/WebSocket contract, error envelope |
   | `ENGINEERING.md` | Backend structure, layers, logging, errors, testing, git |
   | `FLUTTER.md` | Flutter app architecture and UI |
   | `DEVOPS.md` | Docker, Terraform, CI/CD, deployments |
   | `QA.md` | Test strategy, quality gates, releases |
   | `SECURITY.md` / `SECURITY_SPEC.md` | Security program and testable requirements |
   | `ENGINEERING_RULES.md` | Rules of engagement (this is the one that binds you) |

3. The **stack is fixed**: Go (server) · Flutter (mobile) · TypeScript web
   client · PostgreSQL · Redis · Docker · Terraform · Cloudflare · WebSockets.
   Never propose a replacement without justification.
4. Backend work happens under `server/`. Follow its structure (`cmd/`,
   `internal/` domains with `delivery/application/domain/infra` four-layer
   convention, `internal/platform/` as a dependency-free leaf, `config/` never
   imported by business code) — `ENGINEERING.md` §2–§3. Run `make ci` before
   finishing any backend change.

## Non-negotiable for agents

- Never violate the architecture or break module boundaries.
- Never introduce a dependency without justification.
- Never duplicate business logic that already exists.
- Always update tests when functionality changes; update docs when architecture changes.
- Follow the existing coding style; prefer readability over cleverness.
- Explain significant architectural decisions in the PR/commit.
- Run the repo's formatters, linters, and tests and leave them green.
- Never commit secrets or tokens.

See `architecture/ENGINEERING_RULES.md` §28 for the full AI coding rules.
