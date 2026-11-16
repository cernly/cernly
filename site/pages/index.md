---
title: Cernly
description: Open-source CLI and MCP server that reads a git repository and reports its architecture as facts, each with the file and line that proves it.
path: /
---
# Cernly

**TL;DR** Cernly reads one commit of a git repository and tells you what it runs: services, data stores, third-party providers, environment variables, outside hosts, routes, tables, dependencies and AI tooling. Every fact carries its file and line. It never runs your code.

```
go install github.com/cernly/cernly/cmd/cernly@latest
cernly scan . > facts.json          # facts, with evidence
cernly map facts.json > map.json    # the architecture derived from them
cernly mcp .                        # the same, for your AI agent
```

- **Deterministic.** Same commit, same binary, same flags: the same bytes.
- **Read-only.** Only git-tracked files are read. Nothing is built, installed or executed.
- **Evidence first.** No fact without a file and a line. AI never adds structure.

## Start

- [Install](/use/install/) and run a first scan.
- [Connect your AI agent](/use/mcp/) over MCP.
- [Common questions](/use/tasks/) and the commands that answer them.
- [Block a risky change in CI](/use/gate/).
- Every page is also plain Markdown: add `index.md` to its URL. [llms.txt](/llms.txt) lists them all.

Apache-2.0. Source on [GitHub](https://github.com/cernly/cernly). The hosted product, with history across commits, several repositories and evidence reports, is at [cernly.eu](https://cernly.eu).
