---
title: Verify a release
description: Check a Cernly release by its build provenance, signed through Sigstore, and by rebuilding the binaries and the viewer to the same hashes.
path: /use/verify/
section: Use
order: 12
---
# Verify a release

**TL;DR** `gh attestation verify` with the release workflow and the tag proves that GitHub built the file from the tagged commit. A rebuild with `scripts/release-build.sh` proves the files hold only what that commit's source says.

## Provenance

Every archive, the viewer page, `binaries.sha256` and `checksums.txt` on a release carry a build provenance attestation: which workflow, repository, ref and commit built the file, signed through Sigstore with the workflow's own identity. There is no Cernly key to trust or lose. Name the release workflow and the tag, so an attestation from another workflow or branch of the repository does not pass:

```
w=cernly/cernly/.github/workflows/release.yml
gh attestation verify cernly_X.Y.Z_linux_amd64.tar.gz --repo cernly/cernly --signer-workflow $w --source-ref refs/tags/vX.Y.Z
gh attestation verify oci://ghcr.io/cernly/cernly:X.Y.Z --repo cernly/cernly --signer-workflow $w --source-ref refs/tags/vX.Y.Z
```

The second line checks the container image.

## Checksums

`checksums.txt` lists the SHA-256 of each archive, `cernly-viewer.html` and `binaries.sha256`. Verify it as above first, then:

```
sha256sum --ignore-missing -c checksums.txt
```

## Rebuild

The binaries, their archives and the viewer are reproducible: the same commit gives the same bytes on any host. CI builds them on Linux and on macOS for every change and fails if one byte differs.

```
git clone https://github.com/cernly/cernly && cd cernly
git checkout vX.Y.Z
scripts/release-build.sh vX.Y.Z /tmp/rebuild
web/viewer/build.sh /tmp/rebuild                    # needs Node 22.23.4
cd /tmp/rebuild && sha256sum --ignore-missing -c /path/to/checksums.txt   # the file from the release
```

The script uses the Go toolchain that `go.mod` names (downloaded if yours is another, and checked against the Go checksum database), ignores your own Go settings, and builds with CGO off, `-trimpath` and `-buildvcs=false`. The archives add `LICENSE`, `NOTICE` and `README.md`, with fixed order, modes and date.
