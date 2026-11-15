#!/usr/bin/env bash
# Builds the local viewer: one offline HTML file and its SHA256SUMS, by default
# in dist/viewer under the open core's root. The WebAssembly module, the page
# and the fonts are folded into the file; its Content-Security-Policy lists each
# inline script by hash and allows no request. The same toolchain and lockfile
# give the same bytes, so anyone can check the published hash.
#
#   web/viewer/build.sh [out-dir]
set -euo pipefail
export GOTOOLCHAIN=go1.26.10

core="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
out="$(mkdir -p "${1:-$core/dist/viewer}" && cd "${1:-$core/dist/viewer}" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cd "$core"
GOOS=js GOARCH=wasm go build -trimpath -buildvcs=false -ldflags=-s -o "$tmp/cernly-viewer.wasm" ./cmd/cernly-viewer
(cd web && { [ -d node_modules ] || npm ci; } && npx vite build viewer)
node web/viewer/inline.mjs web/viewer/dist "$tmp/cernly-viewer.wasm" "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/cernly-viewer.html"
(cd "$out" && shasum -a 256 cernly-viewer.html > SHA256SUMS)
echo "built $out/cernly-viewer.html ($(wc -c < "$out/cernly-viewer.html" | tr -d ' ') bytes)"
cat "$out/SHA256SUMS"
