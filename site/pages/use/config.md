---
title: .cernly.yml
description: Exclude paths, declare your own domains, set stack roles, gate levels and tags with a .cernly.yml file in the repository; every choice stays visible in the output.
path: /use/config/
section: Use
order: 90
---
# .cernly.yml

**TL;DR** A file in the repo that says what to leave out and what the code cannot say itself. Read only when git tracks it. Every entry shows up in the facts file.

```yaml
version: 1
exclude:
  - path: cmd/tool/testdata
    reason: test fixtures
  - examples/**
domains:
  own: [acme-widgets.example, acme-labs.example]   # and their subdomains
stacks:
  default:
    role: production        # or none
    name: UAT               # optional display name
gate:
  new_outside_party: block  # off, warn or block
  licence_not_allowed:
    level: block
    licences: [AGPL-3.0-only, GPL-3.0-only]
tags:
  team: payments
  product: webshop
```

| Key | Effect |
|---|---|
| `exclude` | Paths or globs to leave out, optionally with a reason. Each is listed in the output. |
| `domains.own` | Your domains: hosts under them are not third parties. |
| `stacks.<name>` | Declare a stack production or not, and its display name. The derived role stays beside it. |
| `gate` | Levels for the [gate](/use/gate/) rules. |
| `tags` | Free key-value labels for the repo, such as team and product. |

## Problems

A mistake in the file never stops a scan: it is reported with its line and the entry is skipped.

## Operator settings

Whoever runs the scan can override the repo with the [scan](/use/scan/) flags or an operator file of the same shape. The operator wins, except that the gate takes the stricter level. The output records who decided what.

How it works: [per-repo configuration](/how/config/).
