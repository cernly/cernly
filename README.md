# Cernly

Architecture facts from a git repository, with file and line evidence for every one of them.

`cernly scan` reads the git-tracked files of one commit (Compose files, Kubernetes manifests, Kustomize overlays and
Helm charts, Dockerfiles, CI workflows, package manifests, env examples, Caddy, nginx and Traefik configuration,
Prisma schemas, SQL migrations, Go, TypeScript and Python code, test files; Java and Spring through the optional `java`
WebAssembly plugin, see below) and writes a facts file: services, data stores, dependencies (direct, and transitive from lockfiles), environment variable names, outside hosts,
routes, database tables, AI tooling. `cernly map` derives the architecture from it: which code builds which
service, which stack is production, which outside parties each service talks to. `cernly mcp` serves both to AI
agents over the Model Context Protocol. `cernly gate` compares a scan with an earlier one and fails a build when
it adds something the policy blocks. `cernly sbom` writes a CycloneDX bill of materials per service. `cernly docs`
writes architecture docs in Markdown: arc42 sections and C4 views in Mermaid, every statement with its file and line.
The map counts the surface (outside parties, data stores, columns to check, unpinned images, production stacks,
routes) per service, with size and the Go import graph's coupling as context.

- **Deterministic:** same commit, same binary, same flags, byte-identical output.
- **Evidence:** every fact names the file and line it came from.
- **Safe:** it never runs, builds or installs anything from the repository it reads. Only git-tracked files;
  symlinks and submodules are never followed.

Docs: https://cernly.dev (for agents: [AGENTS.md](AGENTS.md) and https://cernly.dev/llms.txt)

## Install

```
go install github.com/cernly/cernly/cmd/cernly@latest
```

From a checkout: `go build ./cmd/cernly` (Go 1.26.8 or newer, as `go.mod` says).

Or a binary from [releases](https://github.com/cernly/cernly/releases), or Docker:
`docker run --rm -v "$PWD:/repo:ro" ghcr.io/cernly/cernly scan /repo`.

## Use

```
cernly scan --repo github.com/owner/name . > facts.json
cernly map facts.json > map.json
claude mcp add cernly -- cernly mcp .
cernly gate --base base.facts.json facts.json
cernly sbom --out bom.cdx.json facts.json
cernly docs --out ARCHITECTURE.md facts.json
```

Outside plugins are WebAssembly modules, signed with ed25519, that see only the tracked files they ask for and
return facts in the same vocabulary; they cannot read anything else, reach the network or run the repository's
code. Write one with `plugin/sdk` and test it with `plugin/plugintest`: see [CONTRIBUTING](CONTRIBUTING.md#writing-a-webassembly-plugin).
The repo's own, `wasmplugins/java`, is built and signed the same way and loaded with
`cernly scan --plugins <dir> --trust-key <key>.pub`; releases do not ship a signed build of it yet.

A repository can exclude paths and declare its own domains and stack roles in `.cernly.yml`; every exclusion is
visible in the facts file. The formats are `schema/scan.v1.schema.json` and `schema/map.v1.schema.json`
(JSON Schema 2020-12).

## Local viewer

`web/viewer/build.sh` builds `dist/viewer/cernly-viewer.html`: one file that opens one or two facts files from
disk and shows the map, the findings, the AI tooling set up, the routes and hosts, the tables, the dependencies per
service, the module graph, and what changed between two scans. The map is derived in the page by the same Go
code, compiled to WebAssembly. The page makes no requests: its Content-Security-Policy allows none, and every script
in it is listed by hash. The build is reproducible (Go 1.26.10, `-trimpath`, the committed lockfile): build it
yourself and compare with `dist/viewer/SHA256SUMS`, or with the hash we publish.

The same page scans a repository: pick or drop its folder and the scanner, compiled to WebAssembly, reads it in a
Worker in the page, with no git and no request. It reads the files git tracks from `.git/index` (versions 2 to 4,
split index, SHA-1 and SHA-256), the commit from the refs and objects (loose or packed), and gives the same
`facts.json` as `cernly scan` of the same checkout; "Save facts.json" keeps it. Outside plugins run in the browser's
own WebAssembly engine through a WASI shim with wazero's sandbox (`web/viewer/wasi.js`); the page carries a plugin
only with a signed bundle committed beside it (`wasmplugins/<name>/<name>.plugin.json`). Limits: the picked folder
must be the repository's root (a linked worktree or a submodule points outside it), a sparse index is refused, and a
browser scan reads at most 40 000 files and 2 GB. Safari's folder input leaves out hidden files, `.git` included:
drop the folder instead. `npm run test:browser` in `web` checks the page in Chromium, Firefox and WebKit against the
CLI (build it first with `CERNLY_VIEWER_TEST_PLUGINS=1 web/viewer/build.sh`).

With one scan open, "Prepare an upload" shows what an upload file would hold and saves it: paths, names, hosts and
the repo id become tokens (`redact`), unless you switch that off, and the token list is a second file that stays with
you. The page still sends nothing; see [the viewer page](https://cernly.dev/use/viewer/) for what the policy proves.

## Documentation

- [`.cernly.yml` reference](https://cernly.dev/use/config/): every key, precedence, recipes, errors.
- Commands: [`scan`](https://cernly.dev/use/scan/), [`map`](https://cernly.dev/use/map/),
  [`mcp`](https://cernly.dev/use/mcp/), [`gate`](https://cernly.dev/use/gate/), [`sbom`](https://cernly.dev/use/sbom/),
  [`docs`](https://cernly.dev/use/docs/) and [`plugin`](https://cernly.dev/use/plugin/).
- How it works: [facts](https://cernly.dev/how/facts/), [stacks and production](https://cernly.dev/how/stacks/),
  [the map](https://cernly.dev/how/map/), [plugins](https://cernly.dev/how/plugins/).

## Licence

Apache License 2.0. Cernly is a trademark; the licence does not grant use of the name ([TRADEMARKS.md](TRADEMARKS.md)).
