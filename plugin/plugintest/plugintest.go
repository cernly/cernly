// Package plugintest is the golden-test harness for WebAssembly plugin
// authors. It builds a plugin for wasip1 with the Go toolchain, signs it
// with a fixed test key, runs it through the scanner's own runtime on a
// fixture folder and compares the facts with a golden file:
//
//	var update = flag.Bool("update", false, "rewrite golden files")
//
//	func TestGolden(t *testing.T) {
//		p := plugintest.Load(t, ".", plugintest.Manifest(t, "manifest.json"))
//		plugintest.Golden(t, "testdata/fixture", "testdata/fixture.golden.json", *update, p)
//	}
//
// The test key is public and fixed; never trust it outside tests. See
// https://cernly.dev/how/wasm-plugins/.
package plugintest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/sdk"
	"github.com/cernly/cernly/plugin/wasm"
	"github.com/cernly/cernly/schema"
)

// Fixed commit identity for fixture scans outside git.
const (
	SHA         = "0123456789abcdef0123456789abcdef01234567"
	CommittedAt = "2026-10-14T16:02:11Z"
	// ModuleHash replaces a wasm plugin's real hash in goldens: the bytes
	// differ between Go releases, the facts must not.
	ModuleHash = "0000000000000000000000000000000000000000000000000000000000000000"
)

// TestKey is wasm.TestKey: fixed, public, never trusted in production.
func TestKey() ed25519.PrivateKey { return wasm.TestKey() }

// TestTrust trusts TestKey and nothing else (no built-in keys).
func TestTrust(t testing.TB) *wasm.Trust {
	t.Helper()
	_, pub, err := wasm.EncodeKeys(TestKey())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := wasm.NewTrust(pub)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

var (
	buildMu sync.Mutex
	built   = map[string][]byte{}
)

// Build compiles the Go main package pkg (a path relative to the test's
// folder, or an import path) for GOOS=wasip1 GOARCH=wasm, once per test
// process.
func Build(t testing.TB, pkg string) []byte {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if b, ok := built[pkg]; ok {
		return b
	}
	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s for wasip1: %v\n%s", pkg, err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	built[pkg] = b
	return b
}

// Manifest reads a manifest file (the sha256 is filled in by Sign).
func Manifest(t testing.TB, file string) sdk.Manifest {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m sdk.Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return m
}

// Sign returns the bundle for module, signed with TestKey.
func Sign(t testing.TB, module []byte, m sdk.Manifest) wasm.Bundle {
	t.Helper()
	m.SHA256 = wasm.ModuleHash(module)
	sig, err := wasm.Sign(TestKey(), m)
	if err != nil {
		t.Fatal(err)
	}
	return wasm.Bundle{Manifest: m, Signature: sig}
}

// Load builds pkg, signs it with m and loads it through the runtime.
func Load(t testing.TB, pkg string, m sdk.Manifest, lim ...wasm.Limits) plugin.Plugin {
	t.Helper()
	module := Build(t, pkg)
	var l wasm.Limits
	if len(lim) > 0 {
		l = lim[0]
	}
	p, err := wasm.New(Sign(t, module, m), module, TestTrust(t), l)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Scan scans dir with ps and returns the canonical facts file, wasm hashes
// replaced by ModuleHash.
func Scan(t testing.TB, dir string, ps ...plugin.Plugin) []byte {
	t.Helper()
	s, warnings, err := pipeline.ScanWorkTree(dir, "local/"+filepath.Base(dir), SHA, CommittedAt, ps, config.Operator{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	for i := range s.Plugins {
		if s.Plugins[i].Source == fact.SourceWasm {
			s.Plugins[i].SHA256 = ModuleHash
		}
	}
	data, err := fact.Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// Golden scans dir with ps and compares with the golden file; update
// rewrites it. Every plugin must run ok: an error in a fixture is a bug.
func Golden(t testing.TB, dir, golden string, update bool, ps ...plugin.Plugin) {
	t.Helper()
	got := Scan(t, dir, ps...)
	if again := Scan(t, dir, ps...); !bytes.Equal(got, again) {
		t.Fatalf("%s: two scans differ; a plugin is not deterministic", dir)
	}
	if err := ValidScan(got); err != nil {
		t.Fatalf("%s: the scan does not match scan.v1.schema.json: %v", dir, err)
	}
	var s fact.Scan
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Plugins {
		if p.Status != "ok" {
			t.Errorf("plugin %s: %s %v", p.Name, p.Status, p.Errors)
		}
	}
	if update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run the test with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from %s; rerun with -update and review the diff\n%s", dir, golden, firstDiff(want, got))
	}
}

// ValidScan validates a facts file against the published scan schema.
func ValidScan(data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ScanV1))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schema.ScanV1ID, doc); err != nil {
		return err
	}
	sch, err := c.Compile(schema.ScanV1ID)
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(v)
}

func firstDiff(want, got []byte) string {
	w := strings.Split(prettyOrRaw(want), "\n")
	g := strings.Split(prettyOrRaw(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + ":\n- " + a + "\n+ " + b
		}
	}
	return ""
}

func prettyOrRaw(b []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", " ") != nil {
		return string(b)
	}
	return buf.String()
}
