package wasm_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/plugintest"
	"github.com/cernly/cernly/plugin/sdk"
	"github.com/cernly/cernly/plugin/wasm"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/schema"
)

const probe = "./testdata/probe"

func manifest() sdk.Manifest {
	return sdk.Manifest{Name: "probe", Version: "0.1.0", PluginAPI: 1,
		Watches: []string{"mode.txt"}, Emits: []string{fact.KindEnvVar}}
}

// fixture writes a folder with mode.txt and a file the plugin does not watch.
func fixture(t *testing.T, mode string) *repo.Repo {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"mode.txt": mode + "\n", "secret.txt": "not sent\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func names(fs []fact.Fact) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Key)
	}
	return out
}

func TestRunRecordsOrigin(t *testing.T) {
	p := plugintest.Load(t, probe, manifest())
	res := p.Run(fixture(t, "ok"))
	if res.Failed || len(res.Errors) > 0 {
		t.Fatalf("run: %+v", res)
	}
	if got := names(res.Facts); len(got) != 1 || got[0] != "env_var:PROBE_OK" || res.Facts[0].Plugin != "probe" {
		t.Fatalf("facts %v", res.Facts)
	}
	o := p.(plugin.Origin)
	if o.Source() != "wasm" || len(o.SHA256()) != 64 || o.Signer() != "test-key-not-for-production:"+wasm.KeyID(plugintest.TestKey().Public().(ed25519.PublicKey)) {
		t.Fatalf("origin %s %s %s", o.Source(), o.SHA256(), o.Signer())
	}
	if !p.Watches("mode.txt") || p.Watches("secret.txt") {
		t.Fatal("watches")
	}
}

// Facts the plugin may not return are dropped and recorded: evidence in a
// file it was not sent, a kind outside emits, attrs that break the schema,
// another plugin's name.
func TestRejectsWhatThePluginMayNotSay(t *testing.T) {
	p := plugintest.Load(t, probe, manifest())
	res := p.Run(fixture(t, "reject"))
	if res.Failed {
		t.Fatalf("failed: %v", res.Errors)
	}
	if got := names(res.Facts); len(got) != 1 || got[0] != "env_var:PROBE_OK" {
		t.Fatalf("kept %v", got)
	}
	var msgs []string
	for _, e := range res.Errors {
		msgs = append(msgs, e.Path+": "+e.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"mode.txt: seen",
		"env_var:PROBE_OUTSIDE dropped: evidence in secret.txt",
		"service:x:y dropped: kind not in the manifest's emits",
		"env_var:BAD dropped: schema",
		"env_var:OTHER dropped: plugin env is not probe",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}

// The sandbox: no files, no directory listing, no environment, no writes, no
// network, a fake clock.
func TestSandbox(t *testing.T) {
	t.Setenv("CERNLY_SANDBOX_PROBE", "leak")
	p := plugintest.Load(t, probe, manifest())
	res := p.Run(fixture(t, "sandbox"))
	if res.Failed {
		t.Fatalf("failed: %v", res.Errors)
	}
	if len(res.Facts) > 0 {
		t.Fatalf("sandbox let through: %v", names(res.Facts))
	}
}

func TestLimitsFailThePlugin(t *testing.T) {
	cases := map[string]struct {
		lim  wasm.Limits
		want string
	}{
		"loop":   {wasm.Limits{Timeout: 2 * time.Second}, "time limit"},
		"exit":   {wasm.Limits{}, "exit code 3: probe failed on purpose"},
		"memory": {wasm.Limits{MemoryPages: 4096}, ""},
	}
	for mode, c := range cases {
		t.Run(mode, func(t *testing.T) {
			p := plugintest.Load(t, probe, manifest(), c.lim)
			res := p.Run(fixture(t, mode))
			if !res.Failed || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, c.want) {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

func TestNothingWatchedNothingRun(t *testing.T) {
	m := manifest()
	m.Watches = []string{"absent/**"}
	p := plugintest.Load(t, probe, m)
	if res := p.Run(fixture(t, "loop")); res.Failed || len(res.Facts) > 0 {
		t.Fatalf("%+v", res)
	}
}

func TestDoesNotLoad(t *testing.T) {
	module := plugintest.Build(t, probe)
	good := plugintest.Sign(t, module, manifest())
	trust := plugintest.TestTrust(t)
	if _, err := wasm.New(good, module, trust, wasm.Limits{}); err != nil {
		t.Fatal(err)
	}
	other := ed25519.NewKeyFromSeed(make([]byte, 32))
	byOther := good
	byOther.Signature, _ = wasm.Sign(other, good.Manifest)
	tampered := append(append([]byte(nil), module...), 0)
	changed := good
	changed.Manifest.Emits = []string{fact.KindEnvVar, fact.KindService}
	unsigned := good
	unsigned.Signature = wasm.Signature{}
	badName := plugintest.Sign(t, module, sdk.Manifest{Name: "Probe", Version: "1", PluginAPI: 1, Watches: []string{"x"}, Emits: []string{"env_var"}})
	badKind := plugintest.Sign(t, module, sdk.Manifest{Name: "probe", Version: "1", PluginAPI: 1, Watches: []string{"x"}, Emits: []string{"widget"}})
	for name, c := range map[string]struct {
		b    wasm.Bundle
		mod  []byte
		want string
	}{
		"untrusted key":    {byOther, module, "not a trusted key"},
		"module changed":   {good, tampered, "module hash"},
		"manifest changed": {changed, module, "does not match the manifest"},
		"unsigned":         {unsigned, module, "unsigned"},
		"bad name":         {badName, module, "does not match"},
		"unknown kind":     {badKind, module, "unknown fact kind"},
	} {
		if _, err := wasm.New(c.b, c.mod, trust, wasm.Limits{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestKeysRoundTrip(t *testing.T) {
	seed := sha256.Sum256([]byte("x"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	privPEM, pubPEM, err := wasm.EncodeKeys(priv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := wasm.ParsePrivateKey(privPEM)
	if err != nil || !back.Equal(priv) {
		t.Fatal("private key", err)
	}
	pub, err := wasm.ParsePublicKey(pubPEM)
	if err != nil || !pub.Equal(priv.Public()) {
		t.Fatal("public key", err)
	}
	if _, err := wasm.ParsePublicKey(privPEM); err == nil {
		t.Fatal("a private key read as public")
	}
}

// A bundle on disk loads by its directory.
func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	module := plugintest.Build(t, probe)
	b := plugintest.Sign(t, module, manifest())
	raw, _ := fact.Canonical(b)
	os.WriteFile(filepath.Join(dir, "probe.wasm"), module, 0o644)
	os.WriteFile(filepath.Join(dir, "probe.plugin.json"), raw, 0o644)
	ps, err := wasm.LoadDir(dir, plugintest.TestTrust(t), wasm.Limits{})
	if err != nil || len(ps) != 1 || ps[0].Name() != "probe" {
		t.Fatalf("%v %v", ps, err)
	}
}

// A signed bundle and the probe's output match plugin.v1.schema.json.
func TestPluginSchema(t *testing.T) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	for id, raw := range map[string][]byte{schema.ScanV1ID: schema.ScanV1, schema.PluginV1ID: schema.PluginV1} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
	}
	check := func(def string, v any) {
		sch, err := c.Compile(schema.PluginV1ID + "#/$defs/" + def)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		doc, _ := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err := sch.Validate(doc); err != nil {
			t.Errorf("%s: %v", def, err)
		}
	}
	module := plugintest.Build(t, probe)
	check("bundle", plugintest.Sign(t, module, manifest()))
	var out bytes.Buffer
	in := `{"plugin_api":1,"repo":{"files":[{"path":"mode.txt","content_b64":"b2sK"}]},"facts":[]}`
	var input any
	json.Unmarshal([]byte(in), &input)
	check("input", input)
	if err := sdk.Serve(strings.NewReader(in), &out, func(*sdk.Input) sdk.Output { return sdk.Output{} }); err != nil {
		t.Fatal(err)
	}
	var output any
	json.Unmarshal(out.Bytes(), &output)
	check("output", output)
}

// The test key is labelled wherever it signs, and Cernly's built-in keys
// never include it.
func TestTestKeyIsLabelledAndNotBuiltIn(t *testing.T) {
	tr, err := wasm.NewTrust()
	if err != nil {
		t.Fatal(err)
	}
	builtIn := 0
	for _, k := range tr.Keys() {
		if k.Pub.Equal(wasm.TestKey().Public()) {
			t.Fatal("the test key is a built-in key")
		}
		if k.By == "cernly" {
			builtIn++
		}
	}
	if builtIn == 0 {
		t.Fatal("no built-in release key")
	}
	if !strings.HasPrefix(wasm.TestSigner(), wasm.TestSignerPrefix+"ed25519:") {
		t.Fatal(wasm.TestSigner())
	}
}
