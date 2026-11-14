// Package wasm runs outside plugins: WebAssembly modules, signed with
// ed25519, loaded through wazero (pure Go, no cgo).
//
// A module runs with WASI preview 1 and nothing else: no preopened
// directories, no sockets, an empty environment, a fixed clock and a fixed
// random seed, a memory cap and a time limit. It reads the matching tracked
// files on stdin and writes facts on stdout. The scanner validates every
// fact against the published schema and drops what the plugin may not say:
// kinds outside its manifest, evidence in files it was not sent. See
// https://cernly.dev/how/wasm-plugins/.
package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/sdk"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/schema"
)

// Limits bound one plugin run. Exceeding one fails that plugin; the scan
// goes on.
type Limits struct {
	MemoryPages uint32        // 64 KiB pages; default 16384 (1 GiB)
	Timeout     time.Duration // default 2 minutes
	Input       int64         // bytes of file content sent; default 256 MiB
	Output      int64         // bytes the plugin may write; default 64 MiB
}

func (l Limits) withDefaults() Limits {
	if l.MemoryPages == 0 {
		l.MemoryPages = 16384
	}
	if l.Timeout == 0 {
		l.Timeout = 2 * time.Minute
	}
	if l.Input == 0 {
		l.Input = 256 << 20
	}
	if l.Output == 0 {
		l.Output = 64 << 20
	}
	return l
}

// Plugin is one loaded, verified module.
type Plugin struct {
	m      sdk.Manifest
	module []byte
	signer string
	limits Limits

	once     sync.Once
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	initErr  error
}

// consumer is a Plugin whose manifest names consumed kinds (phase 2).
type consumer struct{ *Plugin }

func (c consumer) Consumes() []string { return c.m.Consumes }
func (c consumer) RunWith(r *repo.Repo, in []fact.Fact) plugin.Result {
	return c.run(r, c.filter(in))
}

var (
	_ plugin.Plugin   = (*Plugin)(nil)
	_ plugin.Origin   = (*Plugin)(nil)
	_ plugin.Consumer = consumer{}
)

func (p *Plugin) Name() string           { return p.m.Name }
func (p *Plugin) Version() string        { return p.m.Version }
func (p *Plugin) Source() string         { return fact.SourceWasm }
func (p *Plugin) SHA256() string         { return p.m.SHA256 }
func (p *Plugin) Signer() string         { return p.signer }
func (p *Plugin) Manifest() sdk.Manifest { return p.m }
func (p *Plugin) Watches(path string) bool {
	for _, w := range p.m.Watches {
		if sdk.Match(w, path) {
			return true
		}
	}
	return false
}
func (p *Plugin) Run(r *repo.Repo) plugin.Result { return p.run(r, nil) }

// Load reads a bundle (<name>.plugin.json) and its module (<name>.wasm),
// checks the module's hash and the signature, and returns the plugin. A
// module that is unsigned, signed by a key outside trust or changed after
// signing does not load.
func Load(bundlePath string, trust *Trust, lim Limits) (plugin.Plugin, error) {
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, err
	}
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%s: %w", bundlePath, err)
	}
	module, err := os.ReadFile(modulePath(bundlePath))
	if err != nil {
		return nil, err
	}
	return New(b, module, trust, lim)
}

// New verifies a bundle against its module bytes.
func New(b Bundle, module []byte, trust *Trust, lim Limits) (plugin.Plugin, error) {
	if err := CheckManifest(b.Manifest); err != nil {
		return nil, fmt.Errorf("plugin %s: %w", b.Manifest.Name, err)
	}
	if got := ModuleHash(module); got != b.Manifest.SHA256 {
		return nil, fmt.Errorf("plugin %s: module hash %s, manifest says %s", b.Manifest.Name, got, b.Manifest.SHA256)
	}
	signer, err := trust.Verify(b)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: %w", b.Manifest.Name, err)
	}
	p := &Plugin{m: b.Manifest, module: module, signer: signer, limits: lim.withDefaults()}
	if len(p.m.Consumes) > 0 {
		return consumer{p}, nil
	}
	return p, nil
}

// LoadDir loads every bundle in dir, sorted by file name. One bad bundle
// fails the whole load: the operator asked for each of them.
func LoadDir(dir string, trust *Trust, lim Limits) ([]plugin.Plugin, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*"+BundleSuffix))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []plugin.Plugin
	for _, n := range names {
		p, err := Load(n, trust, lim)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// knownKinds are the fact kinds of scan.v1; a manifest may name only these.
var knownKinds = map[string]bool{}

func init() {
	var s struct {
		Defs struct {
			Fact struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"fact"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schema.ScanV1, &s); err != nil {
		panic(err)
	}
	for _, k := range s.Defs.Fact.Properties.Kind.Enum {
		knownKinds[k] = true
	}
}

// CheckManifest validates a manifest's fields.
func CheckManifest(m sdk.Manifest) error {
	switch {
	case !plugin.NamePattern.MatchString(m.Name):
		return fmt.Errorf("name %q does not match %s", m.Name, plugin.NamePattern)
	case m.Version == "":
		return errors.New("no version")
	case m.PluginAPI != sdk.API:
		return fmt.Errorf("plugin_api %d, the scanner speaks %d", m.PluginAPI, sdk.API)
	case len(m.Watches) == 0:
		return errors.New("no watches")
	case len(m.Emits) == 0:
		return errors.New("no emits")
	case len(m.SHA256) != 64:
		return errors.New("no sha256")
	}
	for _, w := range m.Watches {
		if err := validPattern(w); err != nil {
			return err
		}
	}
	for _, k := range append(append([]string(nil), m.Emits...), m.Consumes...) {
		if !knownKinds[k] {
			return fmt.Errorf("unknown fact kind %q", k)
		}
	}
	if len(m.Tools) > 0 && !slices.Contains(m.Consumes, fact.KindStack) {
		return errors.New("tools needs stack in consumes")
	}
	return nil
}

func validPattern(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("watch %q must be a repo-relative glob", p)
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("watch %q has an empty, . or .. segment", p)
		}
	}
	return nil
}

// filter keeps the consumed facts, stacks narrowed to the manifest's tools.
func (p *Plugin) filter(in []fact.Fact) []fact.Fact {
	var out []fact.Fact
	for _, f := range in {
		if !slices.Contains(p.m.Consumes, f.Kind) {
			continue
		}
		if f.Kind == fact.KindStack && len(p.m.Tools) > 0 {
			tool, _ := f.Attrs["tool"].(string)
			if !slices.Contains(p.m.Tools, tool) {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// cache holds compiled modules by hash for the life of the process, so a
// caller that scans many commits compiles each module once.
var cache = wazero.NewCompilationCache()

func (p *Plugin) init() error {
	p.once.Do(func() {
		ctx := context.Background()
		cfg := wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			WithMemoryLimitPages(p.limits.MemoryPages).
			WithCloseOnContextDone(true)
		p.rt = wazero.NewRuntimeWithConfig(ctx, cfg)
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, p.rt); err != nil {
			p.initErr = err
			return
		}
		p.compiled, p.initErr = p.rt.CompileModule(ctx, p.module)
		if p.initErr == nil {
			if imp := p.foreignImports(); imp != "" {
				p.initErr = fmt.Errorf("imports %s; only wasi_snapshot_preview1 is provided", imp)
			}
		}
	})
	return p.initErr
}

// foreignImports names the first import outside WASI preview 1.
func (p *Plugin) foreignImports() string {
	for _, f := range p.compiled.ImportedFunctions() {
		mod, name, _ := f.Import()
		if mod != wasi_snapshot_preview1.ModuleName {
			return mod + "." + name
		}
	}
	return ""
}

// Close releases the compiled module.
func (p *Plugin) Close() error {
	if p.rt != nil {
		return p.rt.Close(context.Background())
	}
	return nil
}

func failed(msg string) plugin.Result {
	return plugin.Result{Failed: true, Errors: []fact.LocationError{{Path: ".", Message: msg}}}
}

func (p *Plugin) run(r *repo.Repo, in []fact.Fact) plugin.Result {
	if err := p.init(); err != nil {
		return failed("load: " + err.Error())
	}
	input := sdk.Input{PluginAPI: sdk.API, Repo: sdk.Repo{Files: []sdk.File{}}, Facts: in}
	if input.Facts == nil {
		input.Facts = []fact.Fact{}
	}
	sent := map[string]bool{}
	var size int64
	for _, f := range r.Files {
		if !p.Watches(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			return failed(fmt.Sprintf("read %s: %v", f, err))
		}
		size += int64(len(data))
		if size > p.limits.Input {
			return failed(fmt.Sprintf("input over %d bytes", p.limits.Input))
		}
		input.Repo.Files = append(input.Repo.Files, sdk.File{Path: f, Content: data})
		sent[f] = true
	}
	for _, f := range in {
		for _, e := range f.Evidence {
			sent[e.Path] = true
		}
	}
	if len(input.Repo.Files) == 0 && len(in) == 0 {
		return plugin.Result{} // nothing it watches: not run, as a built-in finds nothing
	}
	stdin, err := json.Marshal(input)
	if err != nil {
		return failed(err.Error())
	}
	stdout := &capped{max: p.limits.Output}
	stderr := &capped{max: 4096}
	ctx, cancel := context.WithTimeout(context.Background(), p.limits.Timeout)
	defer cancel()
	// No WithFS, no WithEnv, no WithSysWalltime/Nanotime, no WithRandSource:
	// wazero's defaults are no files, no environment, a fake clock and a
	// fixed random source.
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(p.m.Name).
		WithStdin(bytes.NewReader(stdin)).
		WithStdout(stdout).
		WithStderr(stderr)
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	if err != nil {
		var exit *sys.ExitError
		switch {
		case ctx.Err() != nil:
			return failed(fmt.Sprintf("time limit %s exceeded", p.limits.Timeout))
		case errors.As(err, &exit) && exit.ExitCode() == 0:
		case errors.As(err, &exit):
			return failed(fmt.Sprintf("exit code %d: %s", exit.ExitCode(), firstLine(stderr.String())))
		default:
			return failed(err.Error())
		}
	}
	if stdout.over {
		return failed(fmt.Sprintf("output over %d bytes", p.limits.Output))
	}
	return p.check(stdout.Bytes(), sent)
}

// check decodes and validates the output: schema, kinds, evidence.
func (p *Plugin) check(raw []byte, sent map[string]bool) plugin.Result {
	var out struct {
		Facts  []json.RawMessage    `json:"facts"`
		Errors []fact.LocationError `json:"errors"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return failed("output: " + err.Error())
	}
	var res plugin.Result
	for _, e := range out.Errors {
		if e.Path != "." && !sent[e.Path] {
			e.Message = e.Path + ": " + e.Message
			e.Path, e.Line = ".", 0
		}
		res.Errors = append(res.Errors, e)
	}
	reject := func(f fact.Fact, why string) {
		path := "."
		if len(f.Evidence) > 0 && sent[f.Evidence[0].Path] {
			path = f.Evidence[0].Path
		}
		res.Errors = append(res.Errors, fact.LocationError{Path: path, Message: fmt.Sprintf("fact %s %s dropped: %s", f.Kind, f.Key, why)})
	}
	for _, rawFact := range out.Facts {
		var f fact.Fact
		d := json.NewDecoder(bytes.NewReader(rawFact))
		d.DisallowUnknownFields()
		if err := d.Decode(&f); err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: ".", Message: "fact dropped: " + err.Error()})
			continue
		}
		if f.Plugin == "" {
			f.Plugin = p.m.Name
		}
		switch {
		case f.Plugin != p.m.Name:
			reject(f, "plugin "+f.Plugin+" is not "+p.m.Name)
			continue
		case !slices.Contains(p.m.Emits, f.Kind):
			reject(f, "kind not in the manifest's emits")
			continue
		case len(f.Evidence) == 0:
			reject(f, "no evidence")
			continue
		}
		if bad := outside(f.Evidence, sent); bad != "" {
			reject(f, "evidence in "+bad+", which the plugin was not sent")
			continue
		}
		if !strings.HasPrefix(f.Key, f.Kind+":") {
			reject(f, "key does not start with "+f.Kind+":")
			continue
		}
		if err := validFact(f); err != nil {
			reject(f, err.Error())
			continue
		}
		res.Facts = append(res.Facts, f)
	}
	return res
}

func outside(ev []fact.Evidence, sent map[string]bool) string {
	for _, e := range ev {
		if !sent[e.Path] {
			return e.Path
		}
	}
	return ""
}

var (
	factSchema     *jsonschema.Schema
	factSchemaOnce sync.Once
	factSchemaErr  error
)

// validFact checks one fact against scan.v1's fact def.
func validFact(f fact.Fact) error {
	factSchemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ScanV1))
		if err != nil {
			factSchemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err := c.AddResource(schema.ScanV1ID, doc); err != nil {
			factSchemaErr = err
			return
		}
		factSchema, factSchemaErr = c.Compile(schema.ScanV1ID + "#/$defs/fact")
	})
	if factSchemaErr != nil {
		return factSchemaErr
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := factSchema.Validate(doc); err != nil {
		return fmt.Errorf("schema: %s", lastLine(err.Error()))
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// lastLine is the most specific reason in a jsonschema error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// capped is a buffer that stops growing at max.
type capped struct {
	bytes.Buffer
	max  int64
	over bool
}

func (c *capped) Write(b []byte) (int, error) {
	if int64(c.Len()+len(b)) > c.max {
		c.over = true
		return 0, io.ErrShortWrite
	}
	return c.Buffer.Write(b)
}
