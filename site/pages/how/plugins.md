---
title: Plugins and stack adapters
description: How Cernly's plugins work as stack adapters that translate one stack's files into a shared fact vocabulary, and which stacks they read.
path: /how/plugins/
section: How
order: 20
---
# Plugins and stack adapters

**TL;DR** A plugin reads the tracked files of one stack and returns facts in the shared vocabulary, with evidence. Rules and the map read only that vocabulary, so a new stack needs a plugin, never a new rule.

## The contract

- Input: the tracked, non-excluded files whose paths match the plugin's patterns.
- Output: facts with evidence in those files, and errors with a location.
- No network, no clock, no randomness, no environment variables. Never run, build or install anything from the repo.
- A real parser for each format, never a regex over source code.
- A plugin that fails is listed as not finished; the others still run.

## Built in

| Plugin | Reads |
|---|---|
| `compose` | Docker Compose: services, stacks, data stores, image references. |
| `kubernetes` | Plain manifests, Kustomize overlays, Helm values and `Chart.yaml`. |
| `dockerfile` | Stages, base images, build sources, entrypoints. |
| `command` | Build and deploy commands in CI workflows and Makefiles. |
| `deps` | Go, npm, Python and NuGet manifests and lockfiles (`deps-go`, `deps-npm`, `deps-python`, `deps-nuget`). |
| `dotnet` | .NET projects and C#: EF Core migrations as tables, test projects, project references, env vars and hosts. |
| `env` | Environment variables in code, Compose and example files. |
| `outbound` | Hosts the Go, TypeScript and JavaScript code calls. |
| `caddy`, `nginx`, `traefik` | Routes and the services behind them. |
| `migrations`, `prisma` | SQL migrations and Prisma models as tables and columns. |
| `agentconfig` | AI coding tool configuration. |
| `ownership` | CODEOWNERS and Backstage catalog files. |
| `tests` | Test files per module and the commands that run them. |
| `size` | Files and lines per folder. |

TypeScript is read through esbuild and a JavaScript parser; evidence points at the TypeScript line through the source map. C# is read by a token lexer, so a URL in a comment or in an interpolated string with holes is never a host. Test files are not read for env vars or hosts.

## Two phases

Most plugins read files only (phase 1). A phase-2 plugin also gets the phase-1 facts it names, for example to attach tests to the service that builds their folder.

## Outside plugins

Stacks beyond the built-in ones come as signed [WebAssembly plugins](/how/wasm-plugins/), under the same contract.

## In the code

`plugin` (the contract), `plugins/*` (one folder per plugin), `defaults` (file patterns and skipped folders, as data).
