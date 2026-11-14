---
title: cernly scan
description: cernly scan reads the git-tracked files of one commit and writes a deterministic facts file in which every fact carries file and line evidence.
path: /use/scan/
section: Use
order: 20
commands: scan
---
# cernly scan

**TL;DR** Reads one commit and writes a facts file (JSON). Every fact has a kind, a key, attributes, the plugin that found it and its evidence: file and line.

```
cernly scan [flags] <dir>
```

| Flag | Meaning |
|---|---|
| `--repo` | Canonical repo id, for example `github.com/acme/shop`. Default: from the `origin` remote. |
| `--out` | Output file; `-` (default) is stdout. |
| `--commit` | Commit to read. Default: `HEAD`. Read from git's objects, without a checkout. |
| `--committed-at` | Committer date, RFC 3339. Default: from git. |
| `--exclude` | Leave a path or glob out. Repeatable. Recorded in the output. |
| `--include` | Bring back a path the repo's `.cernly.yml` excluded. Repeatable. |
| `--no-repo-config` | Ignore the repo's `.cernly.yml`. Recorded in the output. |
| `--config` | An operator file with the same keys as `.cernly.yml`; it wins over the repo's file. |
| `--plugins` | Folder of signed WebAssembly plugins to load. Default `$CERNLY_PLUGIN_DIR`. |
| `--trust-key` | Public key whose plugins may load, besides Cernly's. Repeatable. Default `$CERNLY_PLUGIN_KEYS`. |

## Example

```
$ cernly scan --repo github.com/acme/shop . > facts.json
cernly: 412 facts from 18 plugins, all finished
```

One fact from that file:

```json
{
  "kind": "datastore",
  "key": "datastore:compose:default:postgres",
  "plugin": "compose",
  "attrs": { "engine": "postgres", "stack": "default", "version": "16" },
  "evidence": [{ "path": "docker-compose.yml", "line": 20, "end_line": 21 }]
}
```

## Rules

- Only git-tracked files. Symlinks and submodules are never followed.
- A plugin that fails is listed as not finished; the scan still completes.
- Same commit, same binary, same flags: byte-identical output.

All fact kinds: [facts](/use/facts/). Output format: [schemas](/use/schema/).

How it works: [facts and evidence](/how/facts/), [plugins](/how/plugins/).
