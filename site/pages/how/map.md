---
title: The map and rules
description: How Cernly derives the map from facts with pure rules, why the map is a separate file, and how links, findings, metrics and the module graph are built.
path: /how/map/
section: How
order: 40
---
# The map and rules

**TL;DR** Facts are observations; the map is their interpretation. Rules are pure functions: facts and settings in, map out. Every map item names the facts it comes from.

## Why a separate file

- The scan stays "same commit, same bytes": settings such as the default branch are not repo input.
- A stored facts file can be re-derived when the rules improve, without scanning again.
- Declared items (a confirmed role, an own domain) stay apart from what was found.

The map header records the facts file's hash, the rules version, the catalogue version and the settings. Same facts, rules, catalogue and settings: the same map.

## The rules

- **Link:** a service is linked to the Dockerfile and source folder that build it, through Compose `build:` or a CI image build that produces its image. Precision is the module folder.
- **Production:** see [stacks](/how/stacks/).
- **Narrowing:** when a Dockerfile builds a named project, the link keeps it and what it references, and lists the code units left out. Unresolved builds keep every source.
- **Endpoints:** attached to the service holding the file, joined by path to the proxy routes in front. `endpoint_anonymous_public` prompts a look at an anonymous endpoint on a public production route; it does not claim the endpoint is open.
- **Parties:** provider signals from the [catalogue](/how/catalogue/), graded.
- **Images:** an image no build in the repo produces is third-party, graded by pinning.
- **Tables:** tables and columns merged across migrations and ORM models; columns that look personal or secret are listed to check.
- **Findings:** things worth a look, each with its rule id and the facts behind it.

## Metrics

`metrics` counts the surface: outside parties, data stores, columns to check, unpinned images, production stacks and routes. In total, in production and per service. Services also get endpoint counts, including endpoints on a link that keeps more than one app. Each count lists its items, so a number is never without its proof. Size (files, lines, test lines) and coupling are context, not scores.

## Module graph

From `import` facts between the repo's own packages: per package its fan-in and fan-out, and the highest of each. It shows where code is coupled, not whether that is bad.

## In the code

`rules` (one file per rule), `schema/map.v1.schema.json` (the format). Command: [cernly map](/use/map/).
