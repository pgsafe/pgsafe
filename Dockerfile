# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION}" -o /usr/local/bin/pgsafe ./cmd/pgsafe


# ── Runtime stage ───────────────────────────────────────────────────────────────
# postgres:18 (Debian) provides pg_dump and all client libraries.
# We add compression tools and supercronic on top.
FROM postgres:18

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      gzip \
      pigz \
      bzip2 \
      pbzip2 \
      xz-utils \
      openssl \
    && rm -rf /var/lib/apt/lists/*

# supercronic — drop-in cron for containers (handles signals correctly).
ARG SUPERCRONIC_VERSION=0.2.46
RUN set -eux; \
    case "$(uname -m)" in \
      x86_64)  ARCH=amd64 ;; \
      aarch64) ARCH=arm64 ;; \
      *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /usr/local/bin/supercronic \
      "https://github.com/aptible/supercronic/releases/download/v${SUPERCRONIC_VERSION}/supercronic-linux-${ARCH}"; \
    chmod +x /usr/local/bin/supercronic

COPY --from=builder /usr/local/bin/pgsafe /usr/local/bin/pgsafe
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

# Defaults — override via environment variables.
ENV RUN_MODE=job \
    CRON_SCHEDULE="0 0 * * *" \
    DUMP_FORMAT=custom \
    COMPRESSION_METHOD=none

ENTRYPOINT ["/docker-entrypoint.sh"]
