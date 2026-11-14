---
title: Install
description: Install the Cernly CLI with Go, a release binary or Docker, and run a first scan.
path: /use/install/
section: Use
order: 10
---
# Install

**TL;DR** `go install github.com/cernly/cernly/cmd/cernly@latest`, then `cernly scan . > facts.json` in a git repository.

## Go

Go 1.24 or later:

```
go install github.com/cernly/cernly/cmd/cernly@latest
```

## Release binary

Download the archive for Linux, macOS or Windows from [GitHub releases](https://github.com/cernly/cernly/releases), check it against `checksums.txt`, and put `cernly` on your `PATH`.

## Docker

```
docker run --rm -v "$PWD:/repo:ro" ghcr.io/cernly/cernly scan /repo > facts.json
```

## Requirements

`git` on the `PATH`: Cernly asks git which files are tracked and reads them from git's objects. No network access, nothing from the repository is run.

## First scan

```
cd your-repo
cernly scan . > facts.json
cernly map facts.json > map.json
```

The scan prints a summary on stderr: how many facts, which plugins ran, which did not finish. Next: [what a scan finds](/use/scan/), or [connect your agent](/use/mcp/).
