# syntax=docker/dockerfile:1.7
# MangaVeyra server image: one static binary with embedded web assets.
#   docker build -f deploy/Dockerfile -t mangaveyra .
# Build context is the repository root (deploy/Dockerfile.dockerignore trims it).

ARG GO_VERSION=1.27.1
ARG NODE_VERSION=24
ARG ALPINE_VERSION=3.24

# 1. Frontend assets (skipped when web/ does not exist yet).
FROM node:${NODE_VERSION}-alpine AS web
WORKDIR /src
COPY . .
RUN mkdir -p internal/web/static/dist && \
    if [ -f web/package.json ]; then cd web && npm ci --no-audit --no-fund && npm run build; \
    else echo "web/ not present, skipping frontend build"; fi

# 2. Go binary (CGO disabled: SQLite is pure Go).
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/internal/web/static/dist ./internal/web/static/dist
ARG VERSION=1.0.0
ARG COMMIT=
ARG BUILD_DATE=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w \
      -X github.com/MaramHarsha/MangaVeyra/internal/version.Version=${VERSION} \
      -X github.com/MaramHarsha/MangaVeyra/internal/version.Commit=${COMMIT} \
      -X github.com/MaramHarsha/MangaVeyra/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/mangaveyra ./cmd/mangaveyra

# 3. Runtime: FFmpeg for animate clips, pdftoppm for PDF import, non-root user, /data volume.
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata ffmpeg poppler-utils wget && \
    addgroup -S -g 10001 mangaveyra && \
    adduser -S -D -H -u 10001 -G mangaveyra -h /data mangaveyra && \
    mkdir -p /data && chown mangaveyra:mangaveyra /data
COPY --from=build /out/mangaveyra /usr/local/bin/mangaveyra
ENV MV_DATA_DIR=/data \
    MV_ADDR=:8080
USER mangaveyra:mangaveyra
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
LABEL org.opencontainers.image.title="MangaVeyra" \
      org.opencontainers.image.description="Open-source, self-hosted AI manga studio, reader and community" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/MaramHarsha/MangaVeyra"
ENTRYPOINT ["mangaveyra"]
CMD ["serve"]
