---
title: cernly gate
description: cernly gate compares a scan with a base scan and exits 1 when the change adds something the policy blocks, such as a new outside party or an unpinned production image.
path: /use/gate/
section: Use
order: 50
commands: gate
---
# cernly gate

**TL;DR** Compare the scan of a change with the scan of its base. Exit 1 when the change adds something the policy blocks. Use it in CI.

```
cernly gate --base <base.facts.json> [flags] <head.facts.json>
```

| Flag | Meaning |
|---|---|
| `--base` | The earlier facts file to compare with. Required. |
| `--default-branch` | Branch whose deploys count as production. Default `main`. |
| `--format` | `text` (default), `json` or `sarif` for code scanning. |
| `--out` | Output file; `-` (default) is stdout. |

Exit codes: `0` nothing, or warnings only; `1` a finding at level `block`; `2` an error.

## Rules

| Rule | Fires when the change adds | Default |
|---|---|---|
| `new_outside_party` | a third-party provider | warn |
| `new_sensitive_column` | a column that looks personal or secret | warn |
| `new_unpinned_image` | an image without a version or digest | warn |
| `new_production_stack` | a stack that deploys to production | warn |
| `new_dependency` | a direct dependency | warn |
| `new_transitive_dependency` | a dependency through a lockfile | off |
| `licence_not_allowed` | a dependency under a listed licence | warn |
| `new_anonymous_public_endpoint` | an endpoint marked anonymous in code, on a public production route | warn |
| `gate_loosened` | a weaker level than the base had | warn |

Set levels in `.cernly.yml` ([config](/use/config/)): `off`, `warn` or `block`. Of the repo's and the operator's level, the stricter wins.

## In CI

```
git worktree add ../base "$BASE_SHA"
cernly scan ../base > base.json
cernly scan . > head.json
cernly gate --base base.json --format sarif --out gate.sarif head.json
```

How it works: [the gate](/how/gate/).
