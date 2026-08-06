# AGENTS.md — InChat Repository Guide

This repository holds the **InChat** messaging platform (India-first) and its
complete engineering documentation. There is no application code here yet —
Phase 0 (design) is complete; the Go backend, Flutter app, and TypeScript web
client are built from these documents.

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
