// Package outbound reports the public hosts code points at, graded by what
// the parser can prove: a request (net/http in Go; fetch or axios in
// TypeScript and JavaScript), a configured default, or a mere mention. From
// the same Go parse it reports the imports between the repo's own Go
// packages. Files are parsed, never built, type-checked or run; go tooling
// is never called. See https://cernly.dev/how/plugins/.
package outbound

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/hostname"
	"github.com/cernly/cernly/internal/jsread"
	"github.com/cernly/cernly/internal/pyread"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "outbound" }
func (Plugin) Version() string { return "0.2.1" }

func goSource(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.Contains("/"+p, "/testdata/")
}

func (Plugin) Watches(p string) bool {
	return goSource(p) || path.Base(p) == "go.mod" || jsread.Source(p) || path.Base(p) == "package.json" || pyread.Source(p) || pyread.Manifest(p)
}

// Usage grades, strongest last.
const (
	usageMention = "mention"
	usageDefault = "default"
	usageRequest = "request"
)

var rank = map[string]int{usageMention: 1, usageDefault: 2, usageRequest: 3}

type agg struct {
	usage string
	ev    []fact.Evidence
}

// Run parses every Go package (non-test files of one folder) and emits one
// outbound_host fact per public host.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	byDir := map[string][]string{}
	var dirs []string
	for _, f := range r.Files {
		if goSource(f) {
			d := path.Dir(f)
			if byDir[d] == nil {
				dirs = append(dirs, d)
			}
			byDir[d] = append(byDir[d], f)
		}
	}
	sort.Strings(dirs)
	hosts := map[string]*agg{}
	note := func(host, usage string, ev fact.Evidence) {
		a := hosts[host]
		if a == nil {
			a = &agg{usage: usage}
			hosts[host] = a
		}
		if rank[usage] > rank[a.usage] {
			a.usage = usage
		}
		a.ev = append(a.ev, ev)
	}
	mods := goModules(r, &res)
	imports := map[string]*goImport{}
	for _, d := range dirs {
		unit := module(r, d)
		from := mods.pkgPath(unit, d)
		imp := func(file string, line int, to string) {
			toDir, toMod, ok := mods.resolve(to)
			if !ok || byDir[toDir] == nil || from == "" || to == from {
				return
			}
			k := from + "\x00" + to
			g := imports[k]
			if g == nil {
				g = &goImport{from: from, to: to, fromDir: d, toDir: toDir, fromMod: mods.path[unit], toMod: toMod}
				imports[k] = g
			}
			g.ev = append(g.ev, fact.Evidence{Path: file, Line: line, Unit: unit})
		}
		pkg(r, byDir[d], unit, note, imp, &res)
	}
	res.Facts = append(res.Facts, importFacts(imports)...)
	for _, f := range r.Files {
		if !jsread.Source(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		out, err := jsread.Read(r, f, data)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		unit := jsread.Package(r.Has, f)
		for _, h := range out.Hosts {
			note(h.Host, h.Usage, fact.Evidence{Path: f, Line: h.Line, Unit: unit})
		}
	}
	pyUnits := pyread.Units(r.Files)
	for _, f := range r.Files {
		if !pyread.Source(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		out, err := pyread.Read(r, f, data)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		unit := pyread.Unit(pyUnits, f)
		for _, h := range out.Hosts {
			note(h.Host, h.Usage, fact.Evidence{Path: f, Line: h.Line, Unit: unit})
		}
	}
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		a := hosts[h]
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindOutboundHost, Key: "outbound_host:" + h,
			Attrs: map[string]any{"host": h, "via": "literal_url", "usage": a.usage}, Plugin: "outbound", Evidence: a.ev})
	}
	return res
}

type parsed struct {
	file string
	fset *token.FileSet
	ast  *ast.File
	http string // local name of net/http, "" when not imported
	fmt  string
	url  string
}

var (
	envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// requestArg is the index of the URL argument of each net/http function.
var requestArg = map[string]int{"Get": 0, "Head": 0, "Post": 0, "PostForm": 0, "NewRequest": 1, "NewRequestWithContext": 2}

// pkg handles one package: package-level consts and vars first (a request in
// one file may use a const from another), then every literal and call.
func pkg(r *repo.Repo, files []string, unit string, note func(string, string, fact.Evidence), imp func(string, int, string), res *plugin.Result) {
	var ps []parsed
	for _, f := range files {
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		fset := token.NewFileSet()
		a, err := parser.ParseFile(fset, f, data, parser.SkipObjectResolution)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		p := parsed{file: f, fset: fset, ast: a}
		imported := imp
		for _, imp := range a.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(ip)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imported(f, fset.Position(imp.Path.Pos()).Line, ip)
			switch ip {
			case "net/http":
				p.http = name
			case "fmt":
				p.fmt = name
			case "net/url":
				p.url = name
			}
		}
		ps = append(ps, p)
	}

	// Package-level names holding a URL literal; a configuration name makes it
	// a default.
	pkgHosts := map[string]string{}
	for _, p := range ps {
		for _, decl := range p.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if h := hostOf(p, vs.Values[i], nil, nil); h != "" {
							pkgHosts[n.Name] = h
						}
					}
				}
			}
		}
	}

	for _, p := range ps {
		ev := func(pos token.Pos) fact.Evidence {
			return fact.Evidence{Path: p.file, Line: p.fset.Position(pos).Line, Unit: unit}
		}
		// Every literal is at least a mention.
		ast.Inspect(p.ast, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					if h := hostname.FromURL(s); h != "" {
						note(h, usageMention, ev(lit.Pos()))
					}
				}
			}
			return true
		})
		// Defaults: a configuration name, or the default argument of a call
		// that also names an environment variable.
		for _, decl := range p.ast.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if i < len(vs.Values) && defaults.Get().ConfigName(n.Name) {
						if h := hostOf(p, vs.Values[i], nil, nil); h != "" {
							note(h, usageDefault, ev(vs.Values[i].Pos()))
						}
					}
				}
			}
		}
		ast.Inspect(p.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !namesEnv(call) {
				return true
			}
			for _, a := range call.Args {
				if h := hostOf(p, a, pkgHosts, nil); h != "" {
					note(h, usageDefault, ev(a.Pos()))
				}
			}
			return true
		})
		// Requests, per function so local variables resolve.
		if p.http == "" {
			continue
		}
		for _, decl := range p.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			locals := map[string]string{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					if len(x.Rhs) == 1 && len(x.Lhs) >= 1 {
						if id, ok := x.Lhs[0].(*ast.Ident); ok {
							if h := hostOf(p, x.Rhs[0], pkgHosts, locals); h != "" {
								locals[id.Name] = h
							}
						}
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgID, ok := sel.X.(*ast.Ident)
					idx, isReq := requestArg[sel.Sel.Name]
					if !ok || pkgID.Name != p.http || !isReq || idx >= len(x.Args) {
						return true
					}
					if h := hostOf(p, x.Args[idx], pkgHosts, locals); h != "" {
						note(h, usageRequest, ev(x.Args[idx].Pos()))
					}
				}
				return true
			})
		}
	}
}

// namesEnv reports a call with an ALL_CAPS string literal argument, such as
// envStr("CHIFT_API_BASE_URL", defaultChiftAPIBaseURL).
func namesEnv(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && envName.MatchString(s) && strings.Contains(s, "_") {
				return true
			}
		}
	}
	return false
}

// hostOf resolves the public host an expression starts with: a literal, a
// package-level or local name holding one, the left side of a +, the format
// of fmt.Sprintf, the argument of url.Parse, or x.String() of a resolved x.
func hostOf(p parsed, e ast.Expr, pkgHosts, locals map[string]string) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				return hostname.FromURL(prefixURL(s))
			}
		}
	case *ast.Ident:
		if h := locals[x.Name]; h != "" {
			return h
		}
		return pkgHosts[x.Name]
	case *ast.ParenExpr:
		return hostOf(p, x.X, pkgHosts, locals)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			return hostOf(p, x.X, pkgHosts, locals)
		}
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if id, ok := sel.X.(*ast.Ident); ok && len(x.Args) > 0 {
			if (id.Name == p.fmt && p.fmt != "" && sel.Sel.Name == "Sprintf") || (id.Name == p.url && p.url != "" && sel.Sel.Name == "Parse") {
				return hostOf(p, x.Args[0], pkgHosts, locals)
			}
		}
		if sel.Sel.Name == "String" && len(x.Args) == 0 {
			return hostOf(p, sel.X, pkgHosts, locals)
		}
	}
	return ""
}

// prefixURL keeps a literal up to its first format verb, so
// "https://api.example.eu/v1/%s" still names its host.
func prefixURL(s string) string {
	if i := strings.Index(s, "%"); i >= 0 {
		return s[:i]
	}
	return s
}

// module is the folder of the nearest go.mod, the unit deps-go uses.
func module(r *repo.Repo, dir string) string {
	for d := dir; ; d = path.Dir(d) {
		if r.Has(path.Join(d, "go.mod")) {
			return d
		}
		if d == "." || d == "/" {
			return "."
		}
	}
}

// goMods holds the repo's Go modules: folder of each go.mod -> module path.
type goMods struct {
	path  map[string]string
	order []string // module folders, longest module path first
}

func goModules(r *repo.Repo, res *plugin.Result) goMods {
	m := goMods{path: map[string]string{}}
	for _, f := range r.Files {
		if path.Base(f) != "go.mod" || !goSource(path.Join(path.Dir(f), "x.go")) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		if mp := modfile.ModulePath(data); mp != "" {
			m.path[path.Dir(f)] = mp
			m.order = append(m.order, path.Dir(f))
		}
	}
	sort.Slice(m.order, func(i, j int) bool {
		a, b := m.path[m.order[i]], m.path[m.order[j]]
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return m.order[i] < m.order[j]
	})
	return m
}

// pkgPath is the import path of the package in folder dir of the module in
// folder unit; "" outside any module.
func (m goMods) pkgPath(unit, dir string) string {
	mp, ok := m.path[unit]
	if !ok {
		return ""
	}
	if dir == unit {
		return mp
	}
	rel := strings.TrimPrefix(dir, unit+"/")
	if unit == "." {
		rel = dir
	}
	return mp + "/" + rel
}

// resolve finds the folder of an import path in one of the repo's modules,
// the longest module path that prefixes it.
func (m goMods) resolve(ip string) (dir, mod string, ok bool) {
	for _, u := range m.order {
		mp := m.path[u]
		if ip != mp && !strings.HasPrefix(ip, mp+"/") {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(ip, mp), "/")
		switch {
		case rel == "":
			return u, mp, true
		case u == ".":
			return rel, mp, true
		default:
			return u + "/" + rel, mp, true
		}
	}
	return "", "", false
}

type goImport struct {
	from, to, fromDir, toDir, fromMod, toMod string
	ev                                       []fact.Evidence
}

// importFacts emits one import fact per pair of the repo's own packages, with
// every import line as evidence.
func importFacts(in map[string]*goImport) []fact.Fact {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]fact.Fact, 0, len(keys))
	for _, k := range keys {
		g := in[k]
		out = append(out, fact.Fact{Kind: fact.KindImport, Key: "import:go:" + g.from + ":" + g.to,
			Attrs: map[string]any{"language": "go", "from": g.from, "to": g.to, "from_dir": g.fromDir, "to_dir": g.toDir,
				"from_module": g.fromMod, "to_module": g.toMod},
			Plugin: "outbound", Evidence: g.ev})
	}
	return out
}
