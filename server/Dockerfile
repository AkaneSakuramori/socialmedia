# syntax=docker/dockerfile:1
# Multi-stage build (DEVOPS.md §5): build in a full toolchain, ship a minimal
# distroless runtime image. Immutable, non-root, read-only, static binary.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache dependencies first (layer reuses on code change).
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/AkaneSakuramori/socialmedia/server/internal/app.Version=${VERSION}" \
    -o /out/api-server ./cmd/api-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api-server /api-server
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/api-server"]
