---
title: MCP server
description: How the Cernly MCP server serves one scan to AI agents with read-only tools, why it never adds structure, and how answers stay tied to file and line evidence.
path: /how/mcp/
section: How
order: 120
---
# MCP server

**TL;DR** `cernly mcp` serves one scan and its map to an AI agent over the Model Context Protocol, on stdio. Four read-only tools. The agent reads what the scanner found, with file and line; it never gets a guess.

## Why MCP

Agents write more and more of the code. They answer architecture questions better from facts with evidence than from reading files one by one. MCP is the standard way clients like Claude Code, Cursor and VS Code talk to tools.

## How it works

1. Given a directory, the server scans the commit at `HEAD` first; given a facts file, it loads it.
2. It derives the map with the same rules as `cernly map`.
3. It answers tool calls from those two files, in memory. Nothing is written, nothing leaves the machine.

## The tools

- `summary`: the scan at a glance, so the agent knows what to ask next.
- `find_facts`: search by kind, text and evidence path, paged so a large repo does not flood the context.
- `get_fact`: one fact in full.
- `get_map`: one map section, filtered and paged.

All are marked read-only. The server's instructions tell the agent to cite file and line from the evidence.

## Rules

- **Read-only.** No tool changes a file, runs a command or calls the network.
- **No added structure.** The server returns facts and map items as they are. It does not summarise, rank or infer; that is the agent's job, and the evidence lets a person check it.
- **One repo, one commit.** It serves exactly one scan.

## In the code

`mcp` (the server and tools), `cli` (`cernly mcp`). Setup per client: [cernly mcp](/use/mcp/).
