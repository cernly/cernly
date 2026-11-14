FROM golang:1.24 AS build
COPY . /src
RUN go build -o /api ./cmd/api
FROM gcr.io/distroless/static:nonroot
COPY --from=build /api /api
ENTRYPOINT ["/api"]
