---
title: Open core boundary and contributing
description: What is open in Cernly and why, how the boundary is enforced in code, and how to contribute a plugin or a fix under Apache-2.0 with a DCO sign-off.
path: /how/open-core/
section: How
order: 130
---
# Open core boundary and contributing

**TL;DR** Everything that works on one repo at one commit is open, under Apache-2.0. A test fails when the open code imports anything outside it.

## The line

- **Open:** read one commit, derive its map, compare two facts files the caller passes, write an SBOM or architecture docs, serve one scan over MCP. Plugins, the plugin SDK, schemas and catalogues.
- **Not here:** anything across many commits or many repos, and hosting.

A feature that needs one repo at one commit, or two facts files you pass in, belongs here.

## Enforced in code

`boundary_test.go` walks every Go file and fails on an import that is not the standard library, the module's own packages or a short list of allowed third-party modules. A new dependency is added to that list on purpose, in review.

## Contributing

Fork, branch and open a pull request against `main`. Sign off every commit (DCO, no CLA). The checks, the
golden files and the rules for plugins are in [CONTRIBUTING.md](https://github.com/cernly/cernly/blob/main/CONTRIBUTING.md).
New fact kinds or fields are additive within v1; removing or changing one needs v2.

## A new stack

Write a plugin: built in when it is generally useful, as a [WebAssembly plugin](/how/wasm-plugins/) when it is yours. Either way it emits the shared vocabulary, so the map, gate and SBOM work without changes.

## Security

Report a vulnerability privately as `SECURITY.md` describes, not in an issue.

## Licence

Apache-2.0. The name and the mark are not licensed with the code; see `TRADEMARKS.md`.
