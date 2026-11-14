---
title: WebAssembly plugins and signing
description: How outside Cernly plugins run as signed WebAssembly modules with no file system, network or clock, and how to build one with the plugin SDK.
path: /how/wasm-plugins/
section: How
order: 70
---
# WebAssembly plugins and signing

**TL;DR** Plugins from outside Cernly are WebAssembly modules, signed with ed25519. They run with no file system, no network, an empty environment and a fixed clock. They read files on stdin and write facts on stdout. The scanner checks every fact.

## Why WebAssembly

A plugin from someone else must not be able to read your disk, call out or run your code. A WebAssembly module under WASI preview 1 with no preopened directories, no sockets, a memory cap and a time limit cannot. The runtime is pure Go (wazero), so Cernly stays one static binary.

## What a plugin gets and returns

- **Manifest:** name, version, plugin API, the file patterns it watches, the kinds it emits, and for a phase-2 plugin the kinds it consumes.
- **Input:** the tracked, non-excluded files that match, and the facts it consumes.
- **Output:** facts in the shared vocabulary. The scanner validates each against `scan.v1.schema.json` and drops a kind outside the manifest or evidence in a file the plugin was not sent.

## Signing

`cernly plugin sign` hashes the module into the manifest and signs both. `cernly scan` loads a module only when its signature is valid for a trusted key: Cernly's own, or one the user adds with the trust-key flag. A tampered or unsigned module is refused and the scan says so. A fixed test key exists for local builds; never trust it in production.

## Building one

```go
package main

import "github.com/cernly/cernly/plugin/sdk"

func main() { sdk.Main(run) }

func run(in *sdk.Input) sdk.Output {
	// read in.Repo.Files, return facts with evidence in them
	return sdk.Output{}
}
```

Build with `GOOS=wasip1 GOARCH=wasm go build` or TinyGo. The `plugintest` package runs it against golden files. The Java plugin (Maven, Gradle, Spring configuration, JPA entities, Flyway and Liquibase) is the worked example.

## In the code

`plugin/wasm` (the runtime), `plugin/sdk` and `plugin/plugintest`, `wasmplugins/java`, `schema/plugin.v1.schema.json`. Commands: [cernly plugin](/use/plugin/).
