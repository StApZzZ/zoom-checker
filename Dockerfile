# Zoom Checker container image: one static binary on distroless (non-root).
#   docker build -t zoom-checker --build-arg VERSION=1.0.0 --build-arg COMMIT=$(git rev-parse --short HEAD) .
# See deploy/README-docker.md and deploy/docker-compose.yml.

# ── 1. frontend ──────────────────────────────────────────────────────────────
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ── 2. backend (static, CGO off; the SPA is embedded via web/embed.go) ───────
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=
ARG TARGETOS
ARG TARGETARCH
# TARGETOS/TARGETARCH come from BuildKit (docker buildx --platform); the legacy
# builder and Podman leave them empty, which means "this machine".
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X zoom-checker/internal/version.Version=${VERSION} -X zoom-checker/internal/version.Commit=${COMMIT}" \
      -o /out/zoom-checker ./cmd/zoom-checker \
 && mkdir -p /out/data

# ── 3. runtime ───────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zoom-checker /zoom-checker
# An empty /data owned by the non-root user, so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV DATA_DIR=/data \
    LISTEN_ADDR=:8080
VOLUME /data
# 8080: TLS_MODE=off (behind a proxy); 80 + 443: TLS_MODE=auto (Let's Encrypt).
EXPOSE 8080 80 443
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s --retries=3 \
  CMD ["/zoom-checker", "healthcheck"]
ENTRYPOINT ["/zoom-checker"]
CMD ["serve"]
