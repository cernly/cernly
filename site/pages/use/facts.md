---
title: Fact kinds
description: Every kind of fact a Cernly scan emits, from services and data stores to dependencies, routes, tables, tests and AI tooling.
path: /use/facts/
section: Use
order: 110
---
# Fact kinds

**TL;DR** A scan emits facts of the kinds below. Every fact has a kind, a key, attributes, the plugin that found it and file-and-line evidence.

| Kind | What it is |
|---|---|
| `service` | A service in a Compose file or a Kubernetes workload. |
| `stack` | A deployment unit: a Compose project, a Kubernetes overlay. |
| `datastore` | A database, cache or queue among the services. |
| `image_recipe` | A Dockerfile stage and what it builds from. |
| `image_build` | A CI job or Compose entry that builds an image. |
| `dependency` | A package from a manifest or lockfile: npm, Go, Python, Maven, Gradle, NuGet. |
| `env_var` | An environment variable, where it is set and where it is read. |
| `outbound_host` | A host the code or config calls. |
| `route` | What a web server or proxy serves: Caddy, nginx, Traefik, Ingress. |
| `db_table`, `db_column` | Tables and columns from migrations and ORM models. |
| `secret_found` | Reserved in the schema; no plugin emits it yet. |
| `agent_config` | An AI coding tool's configuration in the repo. |
| `owner`, `catalog_entity` | Who owns the repo, as CODEOWNERS or a catalog file says. |
| `test_suite`, `test_run` | Test files per module, and the commands that run them. |
| `source_size` | Files and lines per folder. |
| `import` | An import between the repo's own packages. |

New kinds may appear within v1; tools should ignore kinds they do not know. Schema: [scan.v1](/schema/scan.v1.schema.json).

How it works: [facts and evidence](/how/facts/).
