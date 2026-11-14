# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
WORKDIR /src/backend
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.21 AS api
ADD https://example.com/ca.pem /etc/ssl/certs/extra.pem
COPY --from=builder /out/api /app/api
ENTRYPOINT ["/app/api"]

FROM alpine:3.21 AS worker
COPY --from=builder /out/worker /app/worker
COPY --from=busybox:1.36 /bin/sh /bin/sh
