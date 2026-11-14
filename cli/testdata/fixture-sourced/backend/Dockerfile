FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN go build -o /api ./cmd/api

FROM alpine:3.20
COPY --from=build /api /api
ENTRYPOINT ["/api"]
