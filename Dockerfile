# syntax=docker/dockerfile:1

# ── Stage 1: build the static binary ─────────────────────────────────
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hakka ./cmd/hakka

# ── Stage 2: minimal runtime image ───────────────────────────────────
FROM alpine:3.24

# ca-certificates: TLS for LLM APIs · ripgrep: the `search` tool · tzdata: timezones
RUN apk add --no-cache ca-certificates ripgrep tzdata \
    && addgroup -g 1001 hakka \
    && adduser -D -u 1001 -G hakka -h /home/hakka hakka

COPY --from=build /out/hakka /usr/local/bin/hakka

# External volume: model config (hakka.json) + SQLite database (hakka.db).
# chown'd so fresh named volumes inherit writable ownership for the hakka
# user; bind mounts need the same ownership on the host side (see README).
RUN mkdir -p /data && chown hakka:hakka /data
VOLUME ["/data"]

# The hakka user's writable home dir doubles as the default session cwd
# (new sessions inherit os.Getwd(), see agent/session.go).
WORKDIR /home/hakka
USER hakka

# Web UI + WebSocket HTTP port.
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1

ENTRYPOINT ["hakka"]
CMD ["--config", "/data/hakka.json", \
     "--db", "/data/hakka.db", \
     "--web-addr", ":8080", \
     "--ws-addr", "", \
     "--log-level", "info"]
