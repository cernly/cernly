FROM golang:1.27.3-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /out/app ./cmd/app

FROM alpine:3.21
COPY --from=builder /out/app /app
