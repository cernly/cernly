---
title: The gate
description: How cernly gate compares a scan with its base, which additions it watches, and how repo and operator levels combine so a change cannot loosen its own policy.
path: /how/gate/
section: How
order: 80
---
# The gate

**TL;DR** The gate is a pure comparison of two scans: what the head adds that the base did not have, judged by a policy. A finding at level `block` fails the build. The base is whatever the caller passes; Cernly never fetches it.

## What it compares

Both facts files are mapped with the same rules and settings, then compared item by item. Only additions count: a removed provider is good news, not a finding. Each finding names the rule, the subject, the facts behind it and their file and line in the head.

## The rules

New outside party, new sensitive column, new unpinned image, new production stack, new direct dependency, new transitive dependency, a licence on the deny list, and a loosened gate. Levels and defaults: [cernly gate](/use/gate/).

## The policy

- Each rule has a level: `off`, `warn` or `block`.
- The head scan's config section carries the repo's levels (from `.cernly.yml`) and the operator's.
- The stricter of the two wins. Without an operator entry, the repo may set `off`.
- When the head sets a rule weaker than the base did, `gate_loosened` fires: a change cannot quietly switch off its own check.
- The licence rule takes a list of licence ids from both sides; the longer list is stricter.

## Output

Text for people, JSON (`cernly.gate/v1`) for tools, SARIF for code scanning, which shows findings as annotations on the changed lines. The result lists the policy in effect and who set each level.

## Determinism

Two scans and their maps in, a result out: no file, network or clock access. Same inputs, same result, in CI and on a laptop.

## In the code

`rules/gate.go` (the comparison), `config` (rule names and levels), `cli/gate.go` (the command and exit codes).
