---
title: Provider and agent catalogue
description: How Cernly recognises third-party providers and AI coding tools from SDKs, environment variables and hosts, and why a provider needs two kinds of signal to be confirmed.
path: /how/catalogue/
section: How
order: 60
---
# Provider and agent catalogue

**TL;DR** A versioned data file lists providers and the signals that point at them: SDK packages, environment variable names and host names. A provider on two kinds of signal is confirmed; on one, it is listed as single and worth a look.

## A provider entry

One of the `entries` in `providers.json`, shortened:

```json
{
  "id": "sentry",
  "name": "Sentry",
  "category": "error tracking",
  "kind": "provider",
  "sdks": [
    { "ecosystem": "go", "name": "github.com/getsentry/sentry-go" },
    { "ecosystem": "npm", "name": "@sentry/" }
  ],
  "env_prefixes": ["SENTRY_"],
  "hosts": ["sentry.io"],
  "images": ["getsentry/sentry-cli"]
}
```

Each entry also records the sources its signals were checked against and the date.

SDK names match as each ecosystem names packages: Go module prefixes, npm scopes, and NuGet ids case-insensitively. A `nuget` name ending in `.*` matches a package family, so `AWSSDK.*` covers `AWSSDK.S3`.

## Grading

| Grade | When |
|---|---|
| `confirmed` | Two or more signal kinds agree, from two or more facts: an SDK and an env var, say. |
| `single` | One signal kind, or one fact. |

One weak signal is never dropped: it stays on the map as single, so a person can confirm or dismiss it.

## Protocols are not providers

Some entries are protocols: S3, SMTP. An S3 SDK beside a MinIO data store in the same stack means S3 as a protocol, not AWS. Matching the SDK alone would put AWS on the map wrongly.

## Hosts

Outside hosts from code and config are sorted: a catalogue provider, a reserved or internal name, the repo's own domains (`domains.own`), or unknown. Unknown hosts are listed, never guessed.

## Data stores and drivers

`defaults/defaults.yml` maps images to engines and driver packages to the engine they talk to. An image is looked up by its full repository path first, then its last segment: `mcr.microsoft.com/mssql/server` gives `sqlserver`. A driver package names the engine, never a connection string, which several engines share.

## AI coding tools

A second catalogue lists AI coding tools and the files that configure them: instruction files, editor rules, MCP server lists, agent hooks. Each found file becomes an `agent_config` fact and the map's `ai_tools` section names the tool.

## Versioned data

Both catalogues are embedded in the binary. Their version is in the map header, so the same facts and catalogue give the same map. Adding a provider is a data change, not a code change.

## In the code

`defaults/providers.json`, `defaults/agents.json`, `defaults/defaults.yml`, `rules/parties.go`, `rules/drivers.go`.
