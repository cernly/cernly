# AGENTS.md

Cernly reads one commit of a git repository and reports its architecture as facts, each with the file and line
that proves it. It never runs the code it reads.

## Use it

```
cernly scan . > facts.json          # facts with evidence (JSON, schema cernly.scan/v1)
cernly map facts.json > map.json    # stacks, production role, providers, routes, findings
cernly mcp .                        # the same over MCP (stdio), read-only tools
```

- Docs for agents: https://cernly.dev/llms.txt (every page also as Markdown at `<page>index.md`).
- MCP setup per client: https://cernly.dev/use/mcp/
- Questions and their commands: https://cernly.dev/use/tasks/
- Agent skill for users' repos: `skills/cernly/SKILL.md`, served at https://cernly.dev/skills/cernly/SKILL.md
- Output schemas: https://cernly.dev/use/schema/

## Work on it

- `gofmt`, `go vet ./...` and `go test ./...` must pass; output changes update goldens with
  `go test ./cli -run TestGolden -update`.
- Only deterministic extraction: every fact needs file and line evidence. A real parser per format, never a
  regex over code. Plugins never run, build or install anything.
- The open core imports only what `boundary_test.go` allows; add a dependency there on purpose.
- Example names only in code, tests and docs (`acme`, `example.com`).
- Docs live in `site/pages` (Markdown, TL;DR first); build with `go run ./cmd/cernly-site`.
- Sign off commits (`git commit -s`, DCO). How it is built: https://cernly.dev/how/open-core/
