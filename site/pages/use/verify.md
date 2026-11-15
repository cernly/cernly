---
title: Verify a release
description: Check a Cernly release by its build provenance, signed through Sigstore, and by rebuilding the binaries and the viewer to the same hashes.
path: /use/verify/
section: Use
order: 12
---
# Verify a release

**TL;DR** `gh attestation verify <file> --repo cernly/cernly` proves that GitHub's release workflow built the file from the tagged commit. A rebuild with `scripts/release-build.sh` proves the binaries hold only what that commit's source says.

## Provenance

Every archive, the viewer page, `checksums.txt` and `binaries.sha256` on a release carry a build provenance attestation: a statement of which workflow, repository and commit built the file, signed through Sigstore with the workflow's own identity. There is no Cernly key to trust or lose.

```
gh attestation verify cernly_X.Y.Z_linux_amd64.tar.gz --repo cernly/cernly
gh attestation verify oci://ghcr.io/cernly/cernly:X.Y.Z --repo cernly/cernly
```

The second line checks the container image. Without `gh`, `cosign verify-blob-attestation` checks the same bundle.

## Checksums

`checksums.txt` lists the SHA-256 of each archive and of `cernly-viewer.html`:

```
sha256sum --ignore-missing -c checksums.txt
```

## Rebuild

The binaries and the viewer are reproducible: the same commit gives the same bytes on any host. CI builds them on Linux and on macOS for every change and fails if one byte differs.

```
git clone https://github.com/cernly/cernly && cd cernly
git checkout vX.Y.Z
scripts/release-build.sh vX.Y.Z /tmp/rebuild --binaries
diff /tmp/rebuild/binaries.sha256 binaries.sha256   # the file from the release
web/viewer/build.sh /tmp/viewer                     # needs Node 22
cat /tmp/viewer/SHA256SUMS                          # the hash of cernly-viewer.html in checksums.txt
```

The script uses the Go toolchain that `go.mod` names (Go downloads it if yours is another), builds with CGO off, `-trimpath` and `-buildvcs=false`, and writes the hash of each binary. The archives add `LICENSE`, `NOTICE` and `README.md`; on Linux, with GNU tar, the script also rebuilds them to the same bytes.
