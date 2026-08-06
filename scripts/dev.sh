#!/usr/bin/env bash
# Runs the api-server locally (DEVOPS.md §4: make dev-api).
# Sources server/.env.local if present (gitignored); otherwise relies on
# defaults from config.Load() which match the compose stack.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

if [[ -f server/.env.local ]]; then
  echo "dev.sh: loading server/.env.local"
  set -a
  # shellcheck disable=SC1091
  source server/.env.local
  set +a
fi

cd server
exec go run ./cmd/api-server
