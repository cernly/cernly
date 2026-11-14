---
name: cernly
description: Answer questions about what a git repository runs, with file and line evidence, using the Cernly CLI or MCP server. Use when asked which third-party services, data stores, environment variables, outside hosts, routes, database tables, dependencies or AI tools a repo uses; which deployment stack is production; to draw or document the architecture; to produce an SBOM; or to fail CI when a change adds a new outside party or an unpinned production image.
---

# Cernly

Cernly reads the git-tracked files of one commit and reports what the repo runs as facts. Every fact carries the
file and line that proves it. It never runs, builds or installs the code it reads.

## Before you start

Check that `cernly` is on the PATH (`command -v cernly`). If it is not, install it: `go install github.com/cernly/cernly/cmd/cernly@latest`
(other ways: https://cernly.dev/use/install/index.md).

## Pick the command

| The user wants | Run |
|---|---|
| Answers in conversation | `cernly mcp .` as an MCP server; call `summary` first, then `find_facts`, `get_fact`, `get_map` |
| Facts with evidence | `cernly scan --out facts.json .` |
| Third-party providers, production stack, routes, tables, findings | `cernly map --out map.json facts.json`, then the matching section |
| Architecture docs (arc42, C4 in Mermaid) | `cernly docs --out ARCHITECTURE.md facts.json` |
| A CycloneDX SBOM | `cernly sbom --out sbom.cdx.json facts.json` |
| A CI check on a change | `cernly gate --base base.json head.json`, both from `cernly scan`; exit 1 means blocked |

Map sections, as `get_map` serves them and as the map file names them: `stacks` (what is production and why),
`links`, `images` and `ci_images` (pinning), `attached`, `parties` (third-party providers and who uses them),
`internal`, `unknown`, `own`, `routes`, `ai_tools`, `findings`, `tables`, `tests`, `confirmations`, `dismissed`,
`metrics` and `modules` (the last two come back as one item; page their lists with `metrics_services`,
`module_packages`, `module_edges` and `module_cycles`). Fact kinds and fields:
https://cernly.dev/use/facts/index.md.

## Rules

1. Answer from the output only. Quote the evidence as `path:line` for every claim.
2. Do not guess what the output does not say. Say that Cernly found nothing, and name what it reads
   (https://cernly.dev/how/plugins/index.md).
3. A stack is production only when the map says so; `production (unconfirmed)` means a CI condition could not be
   read.
4. Paths the repo left out are listed in the facts file's `config` section. Mention them when they matter.
5. To leave paths out or name your own domains, edit `.cernly.yml` (https://cernly.dev/use/config/index.md), not
   the output.

Tasks with exact commands: https://cernly.dev/use/tasks/index.md. All docs: https://cernly.dev/llms.txt.
