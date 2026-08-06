#!/usr/bin/env bash
# Quick local gate: vet + race tests + build (same shape as `make ci`).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/server"

echo "==> go vet"
go vet ./...

echo "==> go test -race"
go test -race ./...

echo "==> go build"
go build -trimpath -o bin/api-server ./cmd/api-server

echo "==> OK"
