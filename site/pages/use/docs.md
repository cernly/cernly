---
title: cernly docs
description: cernly docs writes architecture documentation in Markdown from one scan, with arc42 sections, C4 context and container views in Mermaid, and a footnote with evidence per statement.
path: /use/docs/
section: Use
order: 70
commands: docs
---
# cernly docs

**TL;DR** Architecture docs in Markdown from a facts file: arc42 sections, C4 context and container views in Mermaid, and a footnote with file and line behind every statement.

```
cernly docs [flags] <facts.json>
```

| Flag | Meaning |
|---|---|
| `--default-branch` | Branch whose deploys count as production. Default `main`. |
| `--out` | Output file, for example `ARCHITECTURE.md`; `-` (default) is stdout. |

## What you get

- The arc42 sections the code can answer: context, building blocks, deployment, cross-cutting concepts, risks.
- A C4 context view (the system and its outside parties) and a container view (services and data stores), as Mermaid that GitHub renders.
- A footnote per statement with the facts behind it and their file and line.

## What stays blank

Goals, constraints, strategy, runtime, decisions, quality and the glossary: the code cannot answer them. Those sections are marked "Not in the code" for a person to write.

Same scan, same options: the same bytes, so the file can live in the repo and change only when the architecture does.

How it works: [architecture docs](/how/architecture-docs/).
