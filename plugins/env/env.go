// Package env reports environment variable names: from example env files,
// Compose files and Go code, each by its own parser. Values never leave the
// plugin; an example value contributes only the public host it points to. See
// https://cernly.dev/how/plugins/.
package env

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/caddyfile"
	"github.com/cernly/cernly/internal/envname"
	"github.com/cernly/cernly/internal/hostname"
	"github.com/cernly/cernly/internal/jsread"
	"github.com/cernly/cernly/internal/nginxconf"
	"github.com/cernly/cernly/internal/prismaschema"
	"github.com/cernly/cernly/internal/pyread"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "env" }
func (Plugin) Version() string { return "0.1.1" }

const (
	originExample = "env_example"
	originCompose = "compose"
	originCode    = "code"
	originConfig  = "config" // a tool's config file, read when the tool loads it (Caddyfile, Prisma schema)
)

// namedExample reports env example files by name (.env.example,
// prod.env.example, env.prod.example,...); byContent the names that count
// only when their content is dotenv-shaped. Both lists are in the shipped
// defaults.
func namedExample(base string) bool {
	return defaults.MatchBase(defaults.Get().Files.EnvExamples, base)
}

func byContent(base string) bool {
	return defaults.MatchBase(defaults.Get().Files.EnvExamplesByContent, base)
}

func isPrisma(base string) bool { return defaults.MatchBase(defaults.Get().Files.Prisma, base) }

func (Plugin) Watches(p string) bool {
	b := path.Base(p)
	return namedExample(b) || byContent(b) || defaults.IsCompose(b) || defaults.IsCaddyfile(b) || isPrisma(b) || nginxconf.IsConfig(p) || jsread.Source(p) || pyread.Source(p) || pyread.Manifest(p) ||
		(strings.HasSuffix(b, ".go") && !strings.HasSuffix(b, "_test.go"))
}

type agg struct {
	origins map[string]bool
	hosts   map[string]bool
	ev      []fact.Evidence
}

type collector map[string]*agg

func (c collector) add(name, origin string, ev fact.Evidence) {
	a := c[name]
	if a == nil {
		a = &agg{origins: map[string]bool{}, hosts: map[string]bool{}}
		c[name] = a
	}
	a.origins[origin] = true
	a.ev = append(a.ev, ev)
}

// Run collects every name, then emits one env_var fact per name.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	c := collector{}
	pyUnits := pyread.Units(r.Files)
	for _, f := range r.Files {
		b := path.Base(f)
		switch {
		case namedExample(b) || byContent(b) && !defaults.IsCaddyfile(b) && !isPrisma(b):
			examples(r, f, namedExample(b), c, &res) // Caddyfile.local.example is a Caddyfile, not a dotenv file
		case defaults.IsCompose(b):
			compose(r, f, c, &res)
		case defaults.IsCaddyfile(b):
			configFile(r, f, "", caddyEnv, c, &res)
		case nginxconf.IsConfig(f) && strings.HasSuffix(b, ".template"):
			configFile(r, f, "", nginxEnv, c, &res) // rendered by the nginx image's envsubst
		case isPrisma(b):
			configFile(r, f, jsread.Package(r.Has, f), prismaEnv, c, &res)
		case strings.HasSuffix(b, ".go") && !strings.HasSuffix(b, "_test.go"):
			goCode(r, f, c, &res)
		case pyread.Source(f):
			pyCode(r, f, pyUnits, c, &res)
		case jsread.Source(f):
			jsCode(r, f, c, &res)
		}
	}
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := c[n]
		attrs := map[string]any{"name": n, "origins": fact.SortedSet(a.origins)}
		if len(a.hosts) > 0 {
			attrs["host_hints"] = fact.SortedSet(a.hosts)
		}
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + n, Attrs: attrs, Plugin: "env", Evidence: a.ev})
	}
	return res
}

// examples reads a dotenv file. A *.example file without env in its name
// counts only when every non-comment line is an assignment.
func examples(r *repo.Repo, file string, named bool, c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	type assign struct {
		name, value string
		line        int
	}
	var found []assign
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 1
	for ; sc.Scan(); line++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimPrefix(t, "export ")
		name, value, ok := strings.Cut(t, "=")
		name = strings.TrimSpace(name)
		if !ok || !envname.Valid(name) {
			if named {
				continue
			}
			return // not a dotenv file
		}
		found = append(found, assign{name, value, line})
	}
	if err := sc.Err(); err != nil {
		// A line over the buffer ends the read: say so. An unnamed file is
		// only a dotenv file when every line is read, so it adds nothing.
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Line: line, Message: err.Error() + "; rest of the file not read"})
		if !named {
			return
		}
	}
	for _, a := range found {
		c.add(a.name, originExample, fact.Evidence{Path: file, Line: a.line})
		if h := publicHost(a.value); h != "" {
			c[a.name].hosts[h] = true
		}
	}
}

// publicHost returns the host of a URL value with a public hostname, else "".
// Nothing else of the value is kept.
func publicHost(value string) string {
	v := strings.TrimSpace(value)
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if strings.Contains(v, "$") {
		return ""
	}
	return hostname.FromURL(strings.Trim(v, `"'`))
}

// compose reads environment: keys of every service and the ${VAR} names
// interpolated anywhere in the file.
func compose(r *repo.Repo, file string, c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: "invalid YAML: " + err.Error()})
		return
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		if key.Value != "services" || val.Kind != yaml.MappingNode {
			interpolations(val, "", file, c)
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			svc, spec := val.Content[j].Value, val.Content[j+1]
			interpolations(spec, svc, file, c)
			if spec.Kind != yaml.MappingNode {
				continue
			}
			for k := 0; k+1 < len(spec.Content); k += 2 {
				if spec.Content[k].Value != "environment" {
					continue
				}
				envNode := spec.Content[k+1]
				switch envNode.Kind {
				case yaml.MappingNode:
					for e := 0; e+1 < len(envNode.Content); e += 2 {
						if n := envNode.Content[e]; envname.Valid(n.Value) {
							c.add(n.Value, originCompose, fact.Evidence{Path: file, Line: n.Line, Unit: svc})
						}
					}
				case yaml.SequenceNode:
					for _, n := range envNode.Content {
						name, _, _ := strings.Cut(n.Value, "=")
						if envname.Valid(name) {
							c.add(name, originCompose, fact.Evidence{Path: file, Line: n.Line, Unit: svc})
						}
					}
				}
			}
		}
	}
}

// interpolations reports ${VAR}, ${VAR:-default}, nested ones too, and $VAR in
// every scalar under n. $$ is an escaped dollar.
func interpolations(n *yaml.Node, unit, file string, c collector) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode {
		for _, name := range varNames(n.Value) {
			c.add(name, originCompose, fact.Evidence{Path: file, Line: n.Line, Unit: unit})
		}
		return
	}
	for _, ch := range n.Content {
		interpolations(ch, unit, file, c)
	}
}

func varNames(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			continue
		}
		if s[i+1] == '$' {
			i++
			continue
		}
		j := i + 1
		if s[j] == '{' {
			j++
		}
		k := j
		for k < len(s) && (s[k] == '_' || s[k] >= 'A' && s[k] <= 'Z' || s[k] >= 'a' && s[k] <= 'z' || s[k] >= '0' && s[k] <= '9') {
			k++
		}
		if k > j && envname.Valid(s[j:k]) {
			out = append(out, s[j:k])
		}
		i = k - 1
	}
	return out
}

var envFuncs = map[string]bool{"Getenv": true, "LookupEnv": true, "Setenv": true, "Unsetenv": true}

// goCode reads os.Getenv / LookupEnv / Setenv / Unsetenv calls with a literal
// name. The file is parsed, never built or run.
func goCode(r *repo.Repo, file string, c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	osName := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os" {
			osName = "os"
			if imp.Name != nil {
				osName = imp.Name.Name
			}
		}
	}
	if osName == "" || osName == "_" {
		return
	}
	unit := module(r, file)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !envFuncs[sel.Sel.Name] {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != osName {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if name, err := strconv.Unquote(lit.Value); err == nil && envname.Valid(name) {
			c.add(name, originCode, fact.Evidence{Path: file, Line: fset.Position(lit.Pos()).Line, Unit: unit})
		}
		return true
	})
}

// jsCode reads process.env and import.meta.env names in TypeScript and
// JavaScript through the shared reader; unit = the nearest package.json
// folder.
func jsCode(r *repo.Repo, file string, c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	out, err := jsread.Read(r, file, data)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	unit := jsread.Package(r.Has, file)
	for _, e := range out.Env {
		if envname.Valid(e.Name) {
			c.add(e.Name, originCode, fact.Evidence{Path: file, Line: e.Line, Unit: unit})
		}
	}
}

// module is the folder of the nearest go.mod, the unit deps-go uses too.
func module(r *repo.Repo, file string) string {
	for d := path.Dir(file); ; d = path.Dir(d) {
		if r.Has(path.Join(d, "go.mod")) {
			return d
		}
		if d == "." || d == "/" {
			return "."
		}
	}
}

type configEnv struct {
	name string
	line int
}

func caddyEnv(src []byte) ([]configEnv, error) {
	es, err := caddyfile.EnvNames(src)
	out := make([]configEnv, 0, len(es))
	for _, e := range es {
		out = append(out, configEnv{e.Name, e.Line})
	}
	return out, err
}

func nginxEnv(src []byte) ([]configEnv, error) {
	es, err := nginxconf.EnvNames(src)
	out := make([]configEnv, 0, len(es))
	for _, e := range es {
		out = append(out, configEnv{e.Name, e.Line})
	}
	return out, err
}

func prismaEnv(src []byte) ([]configEnv, error) {
	es, err := prismaschema.EnvNames(src)
	out := make([]configEnv, 0, len(es))
	for _, e := range es {
		out = append(out, configEnv{e.Name, e.Line})
	}
	return out, err
}

// configFile reads the names a tool's config file takes from the environment,
// through that file's shared reader. A Prisma schema's unit is its npm
// package, as for TypeScript.
func configFile(r *repo.Repo, file, unit string, read func([]byte) ([]configEnv, error), c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	es, err := read(data)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Line: 1, Message: err.Error()})
		return
	}
	for _, e := range es {
		if envname.Valid(e.name) {
			c.add(e.name, originConfig, fact.Evidence{Path: file, Line: e.line, Unit: unit})
		}
	}
}

// pyCode reads a Python file's env names: direct reads, and the fields of
// pydantic BaseSettings classes with their prefix.
func pyCode(r *repo.Repo, file string, u map[string]bool, c collector, res *plugin.Result) {
	data, err := r.Read(file)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	out, err := pyread.Read(r, file, data)
	if err != nil {
		res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return
	}
	unit := pyread.Unit(u, file)
	for _, e := range out.Env {
		if envname.Valid(e.Name) {
			c.add(e.Name, originCode, fact.Evidence{Path: file, Line: e.Line, Unit: unit})
		}
	}
}
