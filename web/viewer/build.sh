#!/usr/bin/env bash
# Builds the local viewer: one offline HTML file and its SHA256SUMS, by default
# in dist/viewer under the open core's root. The WebAssembly module (deflated),
# the scan worker, the page, the fonts and the bundled plugins are folded into
# the file; its Content-Security-Policy lists each inline script by hash and
# allows no request. The same toolchain and lockfile give the same bytes, so
# anyone can check the published hash.
#
# A plugin under wasmplugins/<name> is bundled when its signed bundle
# (wasmplugins/<name>/<name>.plugin.json, signed with Cernly's release key) is
# committed beside it; its module hash must equal the module built here.
# CERNLY_VIEWER_TEST_PLUGINS=1 signs every plugin with the public test key
# instead and makes the page trust it (tests only; the scan records the signer
# as test-key-not-for-production).
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
go run ./internal/cmd/deflate "$tmp/cernly-viewer.wasm" "$tmp/cernly-viewer.wasm.deflate"
hash=$(shasum -a 256 "$tmp/cernly-viewer.wasm" | cut -d' ' -f1)

plugin_args=()
for dir in wasmplugins/*/; do
	name=$(basename "$dir")
	GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$tmp/$name.wasm" "./wasmplugins/$name"
	if [ "${CERNLY_VIEWER_TEST_PLUGINS:-}" = 1 ]; then
		[ -f "$tmp/test.key" ] || go run ./cmd/cernly plugin keygen --test --out "$tmp/test" > /dev/null
		go run ./cmd/cernly plugin sign --key "$tmp/test.key" --manifest "$dir/manifest.json" --out "$tmp/signed" "$tmp/$name.wasm" > /dev/null
		bundle="$tmp/signed/$name.plugin.json"
	elif [ -f "$dir/$name.plugin.json" ]; then
		bundle="$dir/$name.plugin.json"
		want=$(jq -r .manifest.sha256 "$bundle")
		got=$(shasum -a 256 "$tmp/$name.wasm" | cut -d' ' -f1)
		[ "$want" = "$got" ] || { echo "$bundle signs module $want, but this build gives $got: sign the new module" >&2; exit 1; }
		# The signature too, against Cernly's keys only: a bundle signed with
		# the test key would build and then be refused by every scan.
		mkdir -p "$tmp/verify/$name"
		cp "$bundle" "$tmp/verify/$name/$name.plugin.json"
		cp "$tmp/$name.wasm" "$tmp/verify/$name/$name.wasm"
		go run ./cmd/cernly plugin verify "$tmp/verify/$name" > /dev/null ||
			{ echo "$bundle is not signed with a Cernly key: sign it with cernly-plugins-2026" >&2; exit 1; }
	else
		echo "no signed bundle for $name ($dir$name.plugin.json): the page has no $name plugin" >&2
		continue
	fi
	go run ./internal/cmd/deflate "$tmp/$name.wasm" "$tmp/$name.wasm.deflate"
	plugin_args+=(--plugin "$bundle=$tmp/$name.wasm.deflate")
done
[ "${CERNLY_VIEWER_TEST_PLUGINS:-}" = 1 ] && plugin_args+=(--trust "$tmp/test.pub")

(cd web && { [ -d node_modules ] || npm ci; } && npx vite build viewer)
node web/viewer/inline.mjs --dist web/viewer/dist --wasm "$tmp/cernly-viewer.wasm.deflate" --hash "$hash" \
	--wasm-exec "$(go env GOROOT)/lib/wasm/wasm_exec.js" --out "$out/cernly-viewer.html" ${plugin_args[@]+"${plugin_args[@]}"}
(cd "$out" && shasum -a 256 cernly-viewer.html > SHA256SUMS)
echo "built $out/cernly-viewer.html ($(wc -c < "$out/cernly-viewer.html" | tr -d ' ') bytes; module sha256 $hash)"
cat "$out/SHA256SUMS"
