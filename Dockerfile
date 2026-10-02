FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=dev
# -mod=readonly: build exactly what the committed go.mod/go.sum describe
# (no tidy at build time re-resolving versions go.sum never verified).
RUN CGO_ENABLED=1 go build -mod=readonly -o printspy -ldflags="-s -w -X main.version=${VERSION}" .
RUN CGO_ENABLED=1 go build -mod=readonly -o backfill-history ./cmd/backfill-history

FROM alpine:3.21
RUN apk add --no-cache sqlite sqlite-libs ca-certificates
COPY --from=builder /build/printspy /usr/local/bin/
COPY --from=builder /build/backfill-history /usr/local/bin/
COPY --from=builder /build/web /usr/local/share/printspy/web

VOLUME /data
EXPOSE 8080

# /login is the one page that is public (200) both before and after setup;
# /api/version is behind the auth gate and would always report unhealthy.
# busybox wget exits non-zero on connection failure and non-2xx.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PRINTSPY_PORT:-8080}/login" || exit 1

ENTRYPOINT ["printspy"]
