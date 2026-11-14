---
title: cernly sbom
description: cernly sbom writes a CycloneDX 1.6 bill of materials from one scan, per service, with dependencies, transitive dependencies from lockfiles and images.
path: /use/sbom/
section: Use
order: 60
commands: sbom
---
# cernly sbom

**TL;DR** A CycloneDX 1.6 bill of materials from a facts file: per service, its dependencies, the transitive ones from lockfiles, and its images. Deterministic.

```
cernly sbom [flags] <facts.json>
```

| Flag | Meaning |
|---|---|
| `--default-branch` | Branch whose deploys count as production. Default `main`. |
| `--service` | One service's key, for example `service:compose:default:api`. Default: the whole repo. |
| `--stack` | The services of one stack, for example `stack:compose:prod`. |
| `--out` | Output file; `-` (default) is stdout. |

## What it lists

- Per service: the dependencies attached to it, the transitive dependencies its manifest folders reach through lockfiles, and the images it runs or builds from.
- Licences where the package manager records them (npm today).

## What it does not

- The contents of images (OS packages): Cernly never opens an image. Each image says so in a property.

The serial number comes from the facts file's hash and the timestamp from the commit, so the same scan gives the same file.

How it works: [dependencies and SBOM](/how/sbom/).
