---
title: cernly mcp
description: Connect Claude Code, Claude Desktop, Cursor or VS Code to the Cernly MCP server so an AI agent answers architecture questions with file and line evidence.
path: /use/mcp/
section: Use
order: 40
commands: mcp
---
# cernly mcp

**TL;DR** `claude mcp add cernly -- cernly mcp .` and your agent can ask what the repo runs, with file and line for every answer. Read-only, stdio.

```
cernly mcp [flags] <facts.json | dir>
```

Given a directory, it scans `HEAD` first; given a facts file, it serves that file.

| Flag | Meaning |
|---|---|
| `--default-branch` | Branch whose deploys count as production. Default `main`. |

## Tools

| Tool | Returns |
|---|---|
| `summary` | Repo, commit, facts per kind, plugins that did not finish, exclusions, stacks and their role. |
| `find_facts` | Facts filtered by kind, by text in key or attributes, and by evidence path; paged. |
| `get_fact` | One fact with all its attributes and evidence. |
| `get_map` | One section of the map, optionally filtered; paged. Sections: `stacks`, `links`, `images`, `ci_images`, `attached`, `parties`, `internal`, `unknown`, `own`, `routes`, `ai_tools`, `findings`, `tables`, `tests`, `confirmations`, `dismissed`, `metrics` and `modules`. `metrics` and `modules` are objects and come back as one item; their lists are also served on their own so they can be paged: `metrics_services`, `module_packages`, `module_edges` and `module_cycles`. |

## Clients

Claude Code:

```
claude mcp add cernly -- cernly mcp .
```

Claude Desktop, Cursor (`.cursor/mcp.json`) and other clients:

```json
{ "mcpServers": { "cernly": { "command": "cernly", "args": ["mcp", "/path/to/repo"] } } }
```

VS Code (`.vscode/mcp.json`):

```json
{ "servers": { "cernly": { "type": "stdio", "command": "cernly", "args": ["mcp", "${workspaceFolder}"] } } }
```

## Good questions

- Which third-party services does this code talk to, and where?
- Which stack is production, and what proves it?
- Which environment variables does the API read?
- Which production images are not pinned?

How it works: [MCP server](/how/mcp/).
