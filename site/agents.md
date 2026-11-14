Notes for AI agents. Cernly answers questions about what a repository runs: services, data stores, third-party providers, environment variables, outside hosts, routes, database tables, dependencies and AI tooling. Every answer carries the file and line that proves it. It reads git-tracked files only and never runs, builds or installs anything.

In a conversation, connect the MCP server with `claude mcp add cernly -- cernly mcp .` (other clients: https://cernly.dev/use/mcp/index.md). Call `summary` first, then `find_facts`, `get_fact` or `get_map`.

For a one-off answer or a script:

```
cernly scan --out facts.json .        # every fact with file and line
cernly map --out map.json facts.json  # stacks, production, parties, routes, findings
```

Then read the map section that fits the question: `parties` for third-party providers, `stacks` for what is production, `routes`, `tables`, `images`, `findings`. Quote the file and line in your answer, and add nothing the output does not contain.

Tasks with exact commands: https://cernly.dev/use/tasks/index.md. Agent skill: https://cernly.dev/skills/cernly/SKILL.md.
