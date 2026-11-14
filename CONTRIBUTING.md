# Contributing

Thank you for helping. Three rules keep the scanner trustworthy:

1. **Deterministic.** Same commit, same binary, same flags: byte-identical output. No network, no clock, no map
   iteration order in the output.
2. **Evidence.** Every fact carries the file and line it was read from. A fact without evidence is dropped.
3. **Never run the repo.** Plugins read git-tracked files. They never execute code, build tools or package managers
   from the repository they scan.

## Before you open a pull request

```
go vet ./...
go test ./...
go test ./cli -run TestGolden -update   # only for an intended output change; explain the diff in the PR
go build -o /tmp/cernly ./cmd/cernly
/tmp/cernly scan --repo example.com/o/r . > /tmp/a.json
/tmp/cernly scan --repo example.com/o/r . > /tmp/b.json
cmp /tmp/a.json /tmp/b.json              # two scans of the same commit must be byte-identical
```

A new built-in plugin emits facts in the existing vocabulary (`fact`, `schema/scan.v1.schema.json`) and comes with a fixture
under `cli/testdata` and a golden file.

## Writing a built-in plugin

A built-in plugin is Go code compiled into the binary, in `plugins/<name>`.

1. **Implement `plugin.Plugin`** (`plugin/plugin.go`): `Name`, `Version`, `Watches(path)` (true when a change to that
   file can change the output) and `Run(r *repo.Repo) plugin.Result`. A plugin that must see other plugins' facts
   implements `plugin.Consumer` instead: a phase-2 plugin that runs after every plain plugin and gets their facts of the
   kinds `Consumes()` names through `RunWith(r, facts)`. Bump `Version` when the output changes.
2. **Register it** from an `init` function: `func init() { plugin.Register(Plugin{}) }`.
3. **Import it** in `plugins/all/all.go`, which links every built-in plugin into the binary.
4. **Add a fixture** to the golden table: create `cli/testdata/fixture-<name>` with the files it reads, add the folder
   name to `goldenFixtures` in `cli/scan_test.go`, run `go test ./cli -run TestGolden -update` once and review the new
   `cli/testdata/fixture-<name>.golden.json`. `TestDeterministic` and `TestSchemas` cover the same table.

## Writing a WebAssembly plugin

A stack adapter that is not built in is a WebAssembly module (https://cernly.dev/how/plugins/). It cannot read files, reach
the network, see the environment or the real clock: the scanner sends it the tracked files it asks for on stdin
and reads facts from stdout. The protocol is `schema/plugin.v1.schema.json`; the facts are
`schema/scan.v1.schema.json`.

1. **Code.** A Go main package (Go 1.26 or TinyGo) that imports only the standard library, `plugin/sdk` and `fact`
   (for the fact types); `wasmplugins/java` is a full example. This one reads `.env.example` files and emits one
   `env_var` fact per name:

   ```go
   package main

   import (
   	"strings"

   	"github.com/cernly/cernly/fact"
   	"github.com/cernly/cernly/plugin/sdk"
   )

   func main() { sdk.Main(run) }

   func run(in *sdk.Input) sdk.Output {
   	var out sdk.Output
   	seen := map[string]bool{}
   	for _, f := range in.Repo.Files {
   		for i, line := range strings.Split(string(f.Content), "\n") {
   			name, _, ok := strings.Cut(strings.TrimSpace(line), "=")
   			if !ok || name == "" || strings.HasPrefix(name, "#") || seen[name] {
   				continue
   			}
   			seen[name] = true
   			out.Facts = append(out.Facts, fact.Fact{
   				Kind:     fact.KindEnvVar,
   				Key:      "env_var:" + name,
   				Attrs:    map[string]any{"name": name, "origins": []any{"env_example"}},
   				Evidence: []fact.Evidence{{Path: f.Path, Line: i + 1}},
   			})
   		}
   	}
   	return out
   }
   ```

2. **Manifest** (`manifest.json`; for the example above `"watches": ["**/.env.example"]` and `"emits": ["env_var"]`): `name`, `version`, `plugin_api: 1`, `watches` (globs in the `.cernly.yml`
   syntax: only these files are sent), `emits` (the fact kinds it may return), and for a phase-2 plugin `consumes`
   (and `tools` for stacks). The scanner drops any fact of another kind, with evidence in a file it did not send,
   or with attributes the schema rejects, and lists it in the run's errors.
3. **Golden test** with `plugin/plugintest`: it builds the module for `GOOS=wasip1 GOARCH=wasm`, signs it with the
   test key, runs it through the scanner's own runtime on a fixture folder, checks the result against the schema,
   checks two runs give the same bytes, and compares with a golden file (`-update` rewrites it).

   ```go
   var update = flag.Bool("update", false, "rewrite golden files")

   func TestGolden(t *testing.T) {
   	p := plugintest.Load(t, ".", plugintest.Manifest(t, "manifest.json"))
   	plugintest.Golden(t, "testdata/repo", "testdata/repo.golden.json", *update, p)
   }
   ```

4. **Build and sign.**

   ```
   GOOS=wasip1 GOARCH=wasm go build -trimpath -o name.wasm .
   cernly plugin keygen --out mykey             # once; keep mykey.key secret
   cernly plugin sign --key mykey.key --manifest manifest.json --out plugins name.wasm
   cernly scan --plugins plugins --trust-key mykey.pub <repo>
   ```

   The signature covers the manifest, which holds the module's SHA-256, so neither can change after signing. The
   scan records `source: wasm`, the hash and the signer's key id.

**The test key** (`plugin keygen --test`, `plugin/wasm.TestKey`) is derived from a public seed: anyone can sign
with it. It exists for tests and local builds only, is never one of Cernly's built-in keys, and a scan that
trusts it records the signer as `test-key-not-for-production:ed25519:...`. Nobody trusts it in production.

## Sign-off (DCO)

Every commit is signed off under the [Developer Certificate of Origin](https://developercertificate.org):
`git commit -s`. By signing off you state that you wrote the change or have the right to submit it under the
Apache License 2.0. There is no contributor licence agreement.
