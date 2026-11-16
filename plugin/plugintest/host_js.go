//go:build js

package plugintest

// Under GOOS=js (the browser build, tested in Node with go_js_wasm_exec)
// there is no go build and no git: modules come prebuilt from
// $CERNLY_PLUGINTEST_WASM (<base name of pkg>.wasm), fixtures are read
// through os.DirFS, and plugins run in the browser runner,
// web/viewer/wasi.js, each in a Node worker thread.

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"syscall/js"
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
)

func compile(t testing.TB, pkg string) []byte {
	t.Helper()
	dir := os.Getenv("CERNLY_PLUGINTEST_WASM")
	if dir == "" {
		t.Skip("GOOS=js: set CERNLY_PLUGINTEST_WASM to a folder of prebuilt plugin modules")
	}
	name := path.Base(pkg)
	if name == "." {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		name = filepath.Base(wd)
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// nodeRunner defines cernlyRunPlugin as the page's scan worker does, with a
// worker thread in place of a Worker.
const nodeRunner = `(src) => {
  const { Worker } = globalThis.require("node:worker_threads");
  const glue = ";const { parentPort } = require('node:worker_threads');" +
    "parentPort.on('message', (d) => { let r; try { r = globalThis.cernlyWasi.run(d.module, d.stdin, d.opts); } catch (e) { r = { error: String(e && e.message || e) }; } parentPort.postMessage(r); });";
  globalThis.cernlyRunPlugin = (module, stdin, opts) => new Promise((resolve) => {
    const w = new Worker(src + glue, { eval: true });
    const timer = setTimeout(() => { w.terminate(); resolve({ timedOut: true }); }, opts.timeoutMs);
    w.on("message", (r) => { clearTimeout(timer); w.terminate(); resolve(r); });
    w.on("error", (e) => { clearTimeout(timer); resolve({ error: String(e && e.message || e) }); });
    w.postMessage({ module, stdin, opts });
  });
}`

var installRunner sync.Once

func useRunner(t testing.TB) {
	t.Helper()
	installRunner.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		src, err := os.ReadFile(filepath.Join(filepath.Dir(self), "..", "..", "web", "viewer", "wasi.js"))
		if err != nil {
			panic(err)
		}
		js.Global().Call("eval", nodeRunner).Invoke(string(src))
	})
}

func scanWorkTree(dir, repoID, sha, committedAt string, ps []plugin.Plugin, op config.Operator) (*fact.Scan, []string, error) {
	return pipeline.ScanFS(os.DirFS(dir), filepath.Base(dir), pipeline.ScanOptions{RepoID: repoID, Commit: sha, CommittedAt: committedAt, Plugins: ps, Operator: op})
}
