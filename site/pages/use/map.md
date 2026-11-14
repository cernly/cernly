---
title: cernly map
description: cernly map derives stacks, production roles, providers, images, routes, tables, metrics and findings from a facts file, naming the facts behind each conclusion.
path: /use/map/
section: Use
order: 30
commands: map
---
# cernly map

**TL;DR** Derives the architecture from a facts file alone. Every item names the facts it comes from, so each conclusion traces back to a file and line.

```
cernly map [flags] <facts.json>
```

| Flag | Meaning |
|---|---|
| `--default-branch` | Branch whose deploys count as production. Default `main`. Recorded in the output. |
| `--out` | Output file; `-` (default) is stdout. |
| `--confirmations` | A JSON array of confirmations made outside the repo (for example "this stack is production"). Recorded in the output. |

## Sections

| Section | What it says |
|---|---|
| `stacks` | Each deployment stack and whether it is production, from the CI jobs that start it. |
| `links` | Which Dockerfile and source folder build which service. |
| `images`, `ci_images` | Images and how well they are pinned: digest, version or floating. |
| `parties` | Third-party providers and the services that use them, with the signals: SDK, env var, host. |
| `attached`, `internal`, `unknown`, `own` | Outside hosts sorted into provider, internal, unidentified and your own domains. |
| `routes` | What web servers and proxies serve, and which service is behind them. |
| `tables` | Database tables merged across migrations and ORM models. |
| `tests` | Test suites per service and the CI jobs that run them. |
| `modules` | The import graph of the repo's own packages. |
| `metrics` | Counts per service and in production: the surface at a glance. |
| `ai_tools` | AI coding tools configured in the repo. |
| `findings` | Things worth a look, such as an unpinned image in production. |

Same facts, same rules version, same settings: byte-identical map. Format: [schemas](/use/schema/).

How it works: [the map and rules](/how/map/), [stacks](/how/stacks/), [catalogue](/how/catalogue/).
