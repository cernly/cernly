---
title: Tasks
description: Common questions about a repository and the exact Cernly commands that answer them, for developers and for the AI agents they ask.
path: /use/tasks/
section: Use
order: 15
---

# Tasks

**TL;DR** Find the question, run the command under it. Every answer comes with the file and line that proves it.

Most tasks start from one scan and its map:

```
cernly scan --out facts.json .
cernly map --out map.json facts.json
```

## Let your agent do it

Connect the MCP server and ask in your own words ([all clients](/use/mcp/)):

```
claude mcp add cernly -- cernly mcp .
```

Or give your agent the Cernly skill, which tells it which command answers which question:

```
mkdir -p .claude/skills/cernly
curl -fsSL https://cernly.dev/skills/cernly/SKILL.md -o .claude/skills/cernly/SKILL.md
```

That is Claude Code's folder. Other agents that read `SKILL.md` files look in their own folder, for example
`.cursor/skills/` or `.github/skills/`.

## Questions and commands

```
# Which third-party services does this code use?
jq '.parties[] | {name, services}' map.json

# Which stack is production, and why?
jq '.stacks[] | {stack, role, reasons}' map.json

# Which environment variables does it read?
jq -r '.facts[] | select(.kind=="env_var") | .key' facts.json

# Which images are not pinned by digest?
jq '.findings[] | select(.rule=="image_pinning")' map.json

# What does it run, in one document?
cernly docs --out ARCHITECTURE.md facts.json

# What is in it, for the supplier list?
cernly sbom --out sbom.cdx.json facts.json
```

Sections of the map: [cernly map](/use/map/). Kinds of facts: [fact kinds](/use/facts/).

## Block a new outside party in CI

Scan the base and the change, then let the gate decide. It exits 1 when the change adds something the policy
blocks ([gate rules](/use/gate/)):

```
cernly scan --commit origin/main --out base.json .
cernly scan --out head.json .
cernly gate --base base.json head.json
```

In GitHub Actions, check out with `fetch-depth: 0` so the base commit is there.

## Leave something out

Test fixtures or examples that should not count go in `.cernly.yml` ([configuration](/use/config/)). The facts
file lists what was left out, so nothing disappears silently.
