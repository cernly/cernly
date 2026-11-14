# The cernly CLI and MCP server in a small image. git is needed at run time:
# cernly asks git which files are tracked and reads them from its objects.
#   docker run --rm -v "$PWD:/repo:ro" ghcr.io/cernly/cernly scan /repo
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cernly ./cmd/cernly

FROM alpine:3.22
# The mounted checkout belongs to another user: trust /repo and below only.
RUN apk add --no-cache git \
 && git config --system --add safe.directory /repo \
 && git config --system --add safe.directory '/repo/*' \
 && adduser -D -u 10001 cernly
COPY --from=build /out/cernly /usr/local/bin/cernly
USER cernly
LABEL org.opencontainers.image.source="https://github.com/cernly/cernly" \
      org.opencontainers.image.description="Architecture facts from a git repository, with file and line evidence" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.modelcontextprotocol.server.name="io.github.cernly/cernly"
ENTRYPOINT ["cernly"]
