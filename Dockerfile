FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go mod tidy && CGO_ENABLED=1 go build -o printspy -ldflags="-s -w -X main.version=${VERSION}" .

FROM alpine:3.21
RUN apk add --no-cache sqlite sqlite-libs ca-certificates
COPY --from=builder /build/printspy /usr/local/bin/
COPY --from=builder /build/web /usr/local/share/printspy/web

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["printspy"]
