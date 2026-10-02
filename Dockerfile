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

ENTRYPOINT ["printspy"]
