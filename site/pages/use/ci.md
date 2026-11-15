---
title: Scan in CI
description: Run cernly scan in your own CI with the GitHub Action or a GitLab job; the facts file stays in your pipeline and nothing is sent to Cernly.
path: /use/ci/
section: Use
order: 25
---
# Scan in CI

**TL;DR** Add the `cernly/cernly` action, pinned by commit, after `actions/checkout`. It builds `cernly` from its own source and writes the facts file in the job. Nothing goes to Cernly: the file stays on the runner and, unless you turn it off, in a workflow artifact of your repository.

## GitHub Actions

```yaml
name: cernly
on:
  push:
    branches: [main]
  pull_request:
permissions:
  contents: read
jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<commit> # v6
        with:
          persist-credentials: false
      - uses: cernly/cernly@<commit> # vX.Y.Z
```

Pin the action by the full commit id of a release, as above, and keep the tag beside it for people. A tag can be moved; a commit cannot.

| Input | Default | Meaning |
|---|---|---|
| `path` | `.` | The checkout to scan. |
| `output` | `cernly.facts.json` | Where the facts file is written, relative to the workspace. |
| `keep-artifact` | `true` | Keep the facts file as a workflow artifact named `cernly-facts`. |

The action's output `facts` is the path of the facts file, for later steps such as `cernly gate` or `cernly sbom`.

What the action does, step by step:

1. Installs the Go toolchain that its `go.mod` names (`actions/setup-go`, pinned by commit).
2. Builds `cernly` from the action's own source at the pinned commit. Go downloads the modules from the Go module proxy and checks each against `go.sum`.
3. Runs `cernly scan --out <output> <path>`. The scan reads the tracked files from git's objects, runs nothing from the repository and opens no connection.
4. If `keep-artifact` is true, uploads the facts file with `actions/upload-artifact` (pinned by commit) to your repository's run.

The job needs only `contents: read`. The repo id comes from the `origin` remote that `actions/checkout` sets, `github.com/<owner>/<repo>`.

## GitLab CI

```yaml
cernly:
  image: golang:1.26
  script:
    - go install github.com/cernly/cernly/cmd/cernly@vX.Y.Z
    - cernly scan --repo "$CI_SERVER_HOST/$CI_PROJECT_PATH" --out cernly.facts.json .
  artifacts:
    paths: [cernly.facts.json]
```

`go install` at a version checks the module against the Go checksum database. `--repo` names the repository, because GitLab's clone URL for the job carries a job token.

## Other CI

Any job with `git` and the checkout can run a [release binary](/use/install/) or `go install`, then `cernly scan --out cernly.facts.json .`. Check the binary first: [verify a release](/use/verify/).
