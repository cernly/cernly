---
title: Local viewer
description: One offline HTML page that scans a repository or opens a facts file on your machine, and prepares an upload file with paths, names, hosts and the repo id replaced by tokens.
path: /use/viewer/
section: Use
order: 85
---
# Local viewer

**TL;DR** One HTML file. Open it from disk, pick a repository's folder or a facts file, and read the map. Nothing leaves the page. To share a scan, the page shows exactly what the upload file would hold and saves it as a file you upload yourself.

`web/viewer/build.sh` builds `dist/viewer/cernly-viewer.html`. The scanner, the rules and the map run inside the page, compiled to WebAssembly from the CLI's Go code. It scans a folder with no git binary and no server, and opens one facts file or two.

## What the policy proves

The page's Content-Security-Policy has `default-src 'none'` and no `connect-src`, so the browser refuses every `fetch`, XHR, WebSocket and beacon. Each script is listed by hash and `form-action` is `'none'`.

It does not prove more. It cannot stop a page from navigating away with data in the address, and it cannot stop a browser extension. For those the check is the file: the build is reproducible, so compare the page you have with the hash we publish, or build it yourself.

## Check the page you have

1. Run `web/viewer/build.sh` on the release's commit. The same toolchain, lockfile and commit give the same bytes, and `dist/viewer/SHA256SUMS` lists the hash.
2. Compare it with your `cernly-viewer.html` and with the published hash.
3. The page's footer names the scanner version and the first 12 characters of its module's SHA-256; hover for all of it. The build prints the same module hash.

## Prepare an upload

With one scan open, "Prepare an upload" shows the file before anything is saved: how many values of each kind were replaced, every readable string, and the first 40 lines as they will be saved.

- **Redact every string the scanner did not write itself** is on by default; switch it off before saving if you want.
- **Save upload file** writes `<name>.upload.json`.
- **Save token list** writes `<name>.tokens.json`, only while redaction is on. It turns tokens back into your values. Keep it; it is not uploaded.

The page cannot send anything; you upload the file yourself.

## What redaction replaces

A value becomes a token and a number: `path-1`, `name-1`, `host-1`, `repo-1`. The same value always gets the same token, so a service that depends on another still points at it, and the same scan gives the same file.

| Kind | Covers |
|---|---|
| `path` | File and folder paths, and URL paths such as `/api/Catalog/{id}`, one token per segment. Folders and extensions stay: `Controllers/Catalog.cs` becomes `path-1/path-2.cs`. Well-known names such as `Dockerfile`, `go.mod` and `.github` stay. |
| `host` | Hosts and registries, and the host of any URL. Ports and URL schemes stay; a query or fragment becomes a name token and a user name or password is dropped. |
| `repo` | The repo id. |
| `name` | Every other string: service, package and variable names, images, commands, messages, tags, licences, signers, type names: anything the scanner did not write itself. |

## What stays readable

| Kept | Examples |
|---|---|
| Fact kinds and plugin names | `service`, `dependency`, `compose` |
| The scanner's vocabulary | languages, engines, frameworks, ecosystems, HTTP methods, scopes, pinning, version schemes, bare `[Authorize]`, column types such as `varchar(64)` |
| Numbers and flags | versions, line numbers, counts, booleans |
| Identity of the scan | the commit id and date, the scanner's and each plugin's name, version, source and hash |

The token list's schema is `cernly.redaction/v1`.
