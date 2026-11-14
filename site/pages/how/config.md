---
title: Per-repo configuration
description: How Cernly layers shipped defaults, the repo's .cernly.yml and the operator's settings, why the operator wins, and why every choice stays visible.
path: /how/config/
section: How
order: 50
---
# Per-repo configuration

**TL;DR** Three layers: what Cernly ships, what the repo says in `.cernly.yml`, what the operator passes. The operator wins. Every exclusion and declaration is written into the facts file, so nothing is left out silently.

## The layers

1. **Shipped defaults.** File patterns, skipped folders (`node_modules`, `vendor`), the data store table, reserved domains and the provider and agent catalogues. Data in `defaults`, not code in the plugins.
2. **The repo.** `.cernly.yml` at the root, read only when git tracks it. It excludes paths, names own domains, declares stack roles, sets gate levels and adds tags. Keys: [.cernly.yml](/use/config/).
3. **The operator.** Whoever runs the scan: `--exclude`, `--include`, `--no-repo-config`, or `--config` with a file of the same shape.

## Who wins

- The operator over the repo, for exclusions and declarations.
- For gate levels, the stricter of the two: a repo cannot loosen what the operator blocks, and the gate reports when a change loosens its own level.

## Visible, always

The facts file has a `config` section listing every exclusion with its pattern, reason, who set it and the line it came from, plus every declaration. A reader can always tell "not found" from "left out on purpose".

## Mistakes never stop a scan

A wrong key, a bad glob or invalid YAML gives an error with the file and line, and the entry is skipped. The rest of the file still applies and the scan completes.

## Why exclusions at all

Test fixtures, examples and generated code describe systems that do not run. Leaving them in would put fake services and providers on the map. Leaving them out without a trace would hide them. Recording the exclusion does neither.

## In the code

`config` (reading and merging), `defaults` (layer 1).
