#!/usr/bin/env bash
# Builds the release binaries reproducibly: the same commit gives the same
# bytes on any host, so anyone can rebuild a release and compare it with
# binaries.sha256 on the release page (site/pages/use/verify.md).
#
#   scripts/release-build.sh <version> <out-dir>              binaries, binaries.sha256 and the archives
#   scripts/release-build.sh <version> <out-dir> --binaries   binaries and binaries.sha256 only
#
# Reproducible because: the toolchain is the one go.mod names, CGO is off,
# -trimpath drops the build host's paths, -buildvcs=false leaves out the
# checkout's state, and the archives fix order, owner, mode and dates
# (SOURCE_DATE_EPOCH, by default the commit's date). The archives need GNU tar.
set -euo pipefail

version="${1:?version}"
version="${version#v}"
out="${2:?out-dir}"
only="${3:-}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

GOTOOLCHAIN="$(sed -n 's/^toolchain //p' go.mod)"
export GOTOOLCHAIN CGO_ENABLED=0
: "${SOURCE_DATE_EPOCH:=$(git log -1 --format=%ct)}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
bin="$out/bin"
rm -rf "$bin"
mkdir -p "$bin"

targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
for t in $targets; do
  os="${t%/*}" arch="${t#*/}" ext=
  [ "$os" = windows ] && ext=.exe
  d="cernly_${version}_${os}_${arch}"
  mkdir "$bin/$d"
  GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$bin/$d/cernly$ext" ./cmd/cernly
done
(cd "$bin" && find . -type f | sort | sed 's|^\./||' | xargs shasum -a 256) > "$out/binaries.sha256"
[ "$only" = --binaries ] && { cat "$out/binaries.sha256"; exit 0; }

tar --version 2>/dev/null | grep -q GNU || { echo "the archives need GNU tar" >&2; exit 1; }
for t in $targets; do
  os="${t%/*}" arch="${t#*/}"
  d="cernly_${version}_${os}_${arch}"
  cp LICENSE NOTICE README.md "$bin/$d/"
  find "$bin/$d" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
  if [ "$os" = windows ]; then
    (cd "$bin" && find "$d" -print | sort | TZ=UTC zip -qX "$out/$d.zip" -@)
  else
    tar -C "$bin" --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
      --mode=u+rw,go=rX,a-s --format=gnu -cf - "$d" | gzip -9n > "$out/$d.tar.gz"
  fi
done
rm -rf "$bin"
cat "$out/binaries.sha256"
