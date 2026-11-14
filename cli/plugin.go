package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/sdk"
	"github.com/cernly/cernly/plugin/wasm"
)

// Environment defaults for the plugin flags, so a program that embeds this
// package loads the same WebAssembly plugins as `scan`.
const (
	EnvPluginDir  = "CERNLY_PLUGIN_DIR"  // folder of <name>.wasm + <name>.plugin.json
	EnvPluginKeys = "CERNLY_PLUGIN_KEYS" // PEM public keys to trust, separated by the OS list separator
)

// PluginFlags adds --plugins and --trust-key to a flag set.
type PluginFlags struct {
	Dir  string
	Keys Multi
}

func (pf *PluginFlags) Register(fs *flag.FlagSet) {
	fs.StringVar(&pf.Dir, "plugins", os.Getenv(EnvPluginDir), "`dir` of signed WebAssembly plugins to load (default $"+EnvPluginDir+")")
	fs.Var(&pf.Keys, "trust-key", "PEM ed25519 public key `file.pub` whose plugins load, besides Cernly's (repeatable; default $"+EnvPluginKeys+")")
}

// Plugins returns the built-in plugins plus the loaded ones, sorted by name.
func (pf *PluginFlags) Plugins() ([]plugin.Plugin, error) {
	keys := []string(pf.Keys)
	if len(keys) == 0 && os.Getenv(EnvPluginKeys) != "" {
		keys = filepath.SplitList(os.Getenv(EnvPluginKeys))
	}
	return LoadPlugins(pf.Dir, keys)
}

// LoadPlugins returns plugin.All() plus the WebAssembly plugins in dir, signed
// by Cernly or by one of the key files. A plugin that does not load, or that
// takes a built-in's name, is an error: the operator asked for it.
func LoadPlugins(dir string, keyFiles []string) ([]plugin.Plugin, error) {
	ps := plugin.All()
	if dir == "" {
		if len(keyFiles) > 0 {
			return nil, fmt.Errorf("--trust-key without --plugins")
		}
		return ps, nil
	}
	var keys [][]byte
	for _, k := range keyFiles {
		b, err := os.ReadFile(k)
		if err != nil {
			return nil, err
		}
		keys = append(keys, b)
	}
	trust, err := wasm.NewTrust(keys...)
	if err != nil {
		return nil, err
	}
	loaded, err := wasm.LoadDir(dir, trust, wasm.Limits{})
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, p := range ps {
		taken[p.Name()] = true
	}
	for _, p := range loaded {
		if taken[p.Name()] {
			return nil, fmt.Errorf("plugin %s: the name is taken by a built-in plugin", p.Name())
		}
		taken[p.Name()] = true
	}
	ps = append(ps, loaded...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name() < ps[j].Name() })
	return ps, nil
}

// PluginCmd is `cernly plugin keygen | sign | verify`: what a plugin author
// needs to ship a module the scanner loads.
//
//	plugin keygen [--test] --out name   writes name.key (private) and name.pub
//	plugin sign --key name.key --manifest manifest.json [--out dir] module.wasm
//	plugin verify --trust-key name.pub [--trust-key ...] dir
func PluginCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: plugin keygen | sign | verify")
		os.Exit(2)
	}
	switch args[0] {
	case "keygen":
		pluginKeygen(args[1:])
	case "sign":
		pluginSign(args[1:])
	case "verify":
		pluginVerify(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "plugin: unknown subcommand", args[0])
		os.Exit(2)
	}
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "plugin %s: %v\n", what, err)
	os.Exit(1)
}

func keygenFlags(fs *flag.FlagSet) (out *string, test *bool) {
	out = fs.String("out", "", "path `prefix`: writes <prefix>.key and <prefix>.pub (required)")
	test = fs.Bool("test", false, "write the fixed, public test key instead (local builds and tests; never trust it in production)")
	return
}

func pluginKeygen(args []string) {
	fs := NewFlagSet("plugin keygen", args)
	out, test := keygenFlags(fs)
	_ = fs.Parse(args)
	if *out == "" {
		fmt.Fprintln(os.Stderr, "plugin keygen: --out is required")
		os.Exit(2)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail("keygen", err)
	}
	if *test {
		priv = wasm.TestKey()
	}
	privPEM, pubPEM, err := wasm.EncodeKeys(priv)
	if err != nil {
		fail("keygen", err)
	}
	if err := os.WriteFile(*out+".key", privPEM, 0o600); err != nil {
		fail("keygen", err)
	}
	if err := os.WriteFile(*out+".pub", pubPEM, 0o644); err != nil {
		fail("keygen", err)
	}
	fmt.Fprintf(os.Stderr, "key %s: keep %s.key secret, give %s.pub to the operators who load your plugins\n",
		wasm.KeyID(priv.Public().(ed25519.PublicKey)), *out, *out)
}

func signFlags(fs *flag.FlagSet) (key, manifest, out *string) {
	key = fs.String("key", "", "PEM ed25519 private `key.key` (required)")
	manifest = fs.String("manifest", "", "`manifest.json` (name, version, plugin_api, watches, emits, consumes, tools); sha256 is filled in (required)")
	out = fs.String("out", "", "`dir` to write <name>.wasm and <name>.plugin.json (default: beside the module)")
	return
}

func pluginSign(args []string) {
	fs := NewFlagSet("plugin sign", args)
	keyFile, manifestFile, outDir := signFlags(fs)
	_ = fs.Parse(args)
	if *keyFile == "" || *manifestFile == "" || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: plugin sign --key file.key --manifest manifest.json [--out dir] module.wasm")
		os.Exit(2)
	}
	keyPEM, err := os.ReadFile(*keyFile)
	if err != nil {
		fail("sign", err)
	}
	priv, err := wasm.ParsePrivateKey(keyPEM)
	if err != nil {
		fail("sign", err)
	}
	raw, err := os.ReadFile(*manifestFile)
	if err != nil {
		fail("sign", err)
	}
	var m sdk.Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		fail("sign", fmt.Errorf("%s: %w", *manifestFile, err))
	}
	module, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fail("sign", err)
	}
	m.SHA256 = wasm.ModuleHash(module)
	if err := wasm.CheckManifest(m); err != nil {
		fail("sign", err)
	}
	sig, err := wasm.Sign(priv, m)
	if err != nil {
		fail("sign", err)
	}
	dir := *outDir
	if dir == "" {
		dir = filepath.Dir(fs.Arg(0))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("sign", err)
	}
	modOut := filepath.Join(dir, m.Name+".wasm")
	if abs(modOut) != abs(fs.Arg(0)) {
		if err := os.WriteFile(modOut, module, 0o644); err != nil {
			fail("sign", err)
		}
	}
	b, err := fact.Canonical(wasm.Bundle{Manifest: m, Signature: sig})
	if err != nil {
		fail("sign", err)
	}
	if err := os.WriteFile(filepath.Join(dir, m.Name+wasm.BundleSuffix), append(b, '\n'), 0o644); err != nil {
		fail("sign", err)
	}
	fmt.Fprintf(os.Stderr, "signed %s %s (sha256 %s) with %s\n", m.Name, m.Version, m.SHA256, sig.Key)
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func verifyFlags(fs *flag.FlagSet) *Multi {
	var keys Multi
	fs.Var(&keys, "trust-key", "PEM ed25519 public `key.pub` (repeatable)")
	return &keys
}

func pluginVerify(args []string) {
	fs := NewFlagSet("plugin verify", args)
	keys := verifyFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: plugin verify [--trust-key file.pub]... dir")
		os.Exit(2)
	}
	ps, err := LoadPlugins(fs.Arg(0), *keys)
	if err != nil {
		fail("verify", err)
	}
	for _, p := range ps {
		if o, ok := p.(plugin.Origin); ok {
			fmt.Printf("%s %s sha256 %s signer %s\n", p.Name(), p.Version(), o.SHA256(), o.Signer())
		}
	}
}
