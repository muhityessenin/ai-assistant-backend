# syntax=docker/dockerfile:1.7
FROM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget && addgroup -S app && adduser -S -G app -u 10001 app
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY db/migrations /app/db/migrations
COPY docs/openapi.yaml /app/docs/openapi.yaml
RUN mkdir -p /app/data/uploads && chown -R app:app /app
USER app
EXPOSE 18473
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=5 CMD wget -qO- "http://127.0.0.1:${APP_PORT:-18473}/health" >/dev/null || exit 1
ENTRYPOINT ["/app/api"]
