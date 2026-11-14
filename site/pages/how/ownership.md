---
title: Ownership and tags
description: How Cernly reports who owns a repository from CODEOWNERS and Backstage catalog files, and how tags in .cernly.yml label a repo by team, product or domain.
path: /how/ownership/
section: How
order: 100
---
# Ownership and tags

**TL;DR** Ownership is what the repo itself says: the owners in CODEOWNERS and the entities in a Backstage `catalog-info.yaml`. Tags are labels the repo gives itself in `.cernly.yml`. Nothing is guessed: an owner nobody wrote down is not reported.

## Owners

The `ownership` plugin reads:

- **CODEOWNERS** (root, `.github/` or `docs/`): each owner, user or team, with the paths they own and the line that says so. Fact kind `owner`.
- **Backstage `catalog-info.yaml`**: each entity's name, kind, type, owner, system and lifecycle. Fact kind `catalog_entity`.

Commit authors are never used as owners. Who last touched a file says nothing about who is responsible for it.

## Tags

```yaml
tags:
  team: payments
  product: webshop
  domain: checkout
```

Tags are free key-value pairs. They land in the facts file's config section with their line, like every other declaration. An operator file can add tags; per key its value replaces the repo's. The output records who set each one. `product:` and `domain:` are conventions for grouping repos; Cernly gives them no other meaning.

## Why only what the repo says

Ownership guessed from history or names would be wrong often enough to be useless in an audit. A missing CODEOWNERS file is a finding a person can fix; a guessed owner is a fact nobody can trust.

## In the code

`plugins/ownership` (CODEOWNERS and catalog files), `config` (tags). Keys: [.cernly.yml](/use/config/).
