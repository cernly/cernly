---
title: cernly plugin
description: Create a signing key, sign a WebAssembly plugin and verify a folder of plugins before cernly scan loads them.
path: /use/plugin/
section: Use
order: 80
commands: plugin keygen, plugin sign, plugin verify
---
# cernly plugin

**TL;DR** Outside plugins are signed WebAssembly modules. `keygen` makes a key, `sign` signs a module, `verify` checks a folder. `cernly scan --plugins <dir>` loads only plugins signed by a trusted key.

```
cernly plugin keygen --out acme
cernly plugin sign --key acme.key --manifest manifest.json plugin.wasm
cernly plugin verify --trust-key acme.pub dist/plugins
```

## keygen

| Flag | Meaning |
|---|---|
| `--out` | Path prefix: writes `<out>.key` and `<out>.pub`. |
| `--test` | Write the fixed, public test key instead. For local builds and tests only; never trust it in production. |

## sign

| Flag | Meaning |
|---|---|
| `--key` | PEM ed25519 private key. |
| `--manifest` | Manifest JSON: name, version, plugin API, watched files, emitted kinds. The hash is filled in. |
| `--out` | Folder for `<name>.wasm` and `<name>.plugin.json`. Default: beside the module. |

## verify

| Flag | Meaning |
|---|---|
| `--trust-key` | PEM ed25519 public key. Repeatable. |

## Loading

`cernly scan` loads the folder given with its plugins flag (or `$CERNLY_PLUGIN_DIR`) and trusts Cernly's own key plus each key given with its trust-key flag. An unsigned or tampered module is refused and the scan says so. See [scan](/use/scan/).

How it works: [WebAssembly plugins and signing](/how/wasm-plugins/).
