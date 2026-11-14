// Package deps extracts direct dependencies from Go and npm manifests.
package deps

import (
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/version"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type GoPlugin struct{}

func init() {
	plugin.Register(GoPlugin{})
	plugin.Register(NpmPlugin{})
	plugin.Register(PythonPlugin{})
	plugin.Register(NugetPlugin{})
}

func NewGo() plugin.Plugin { return GoPlugin{} }

func (GoPlugin) Name() string    { return "deps-go" }
func (GoPlugin) Version() string { return "0.2.0" }

func (GoPlugin) Watches(p string) bool {
	b := path.Base(p)
	return b == "go.mod" || b == "go.work" || strings.HasSuffix(b, ".go")
}

// Run reports every requirement of every go.mod. A requirement marked //
// indirect is a transitive dependency: direct false, roots the module
// folder. Since Go 1.17 a module's go.mod lists every module its build
// needs, so go.mod is the lockfile; go.sum adds hashes, not packages. No
// via: go.mod does not record which module requires which. The key carries
// the module directory, so two modules in one repo can pin different
// versions without clashing, and the unit links the dependency to the
// service built from that directory.
//
// A requirement is internal when a replace directive points it to a folder
// of the repo with a go.mod, or a go.work uses both modules.
//
// Scope comes from the import lines of the module's own Go files: imported
// from a non-test file = prod, only from _test.go = test, imported nowhere
// = unknown (a tool, a build tag, a leftover).
func (GoPlugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	modules := map[string]string{} // module path -> folder, from every go.mod
	for _, file := range r.Match(func(b string) bool { return b == "go.mod" }) {
		data, err := r.Read(file)
		if err != nil {
			continue
		}
		if mf, err := modfile.ParseLax(file, data, nil); err == nil {
			if mf.Module != nil {
				modules[mf.Module.Mod.Path] = repo.Dir(file)
			}
		}
	}
	work := workspaces(r, &res)
	imports := moduleImports(r, &res)
	for _, file := range r.Match(func(b string) bool { return b == "go.mod" }) {
		data, err := r.Read(file)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		mf, err := modfile.ParseLax(file, data, nil)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		dir := repo.Dir(file)
		// ParseLax drops replace directives; read them with the strict parser
		// when the file allows it.
		var replaces []*modfile.Replace
		if full, err := modfile.Parse(file, data, nil); err == nil {
			replaces = full.Replace
		}
		replaced := map[string]string{} // module path -> local folder
		for _, rep := range replaces {
			if rep.New.Version != "" || !(strings.HasPrefix(rep.New.Path, "./") || strings.HasPrefix(rep.New.Path, "../")) {
				continue
			}
			if p, ok := repo.Join(dir, rep.New.Path); ok && r.Has(path.Join(p, "go.mod")) {
				replaced[rep.Old.Path] = p
			}
		}
		for _, req := range mf.Require {
			if req.Syntax == nil {
				continue
			}
			if req.Indirect {
				res.Facts = append(res.Facts, fact.Fact{
					Kind: fact.KindDependency,
					Key:  "dependency:go:" + dir + ":" + req.Mod.Path,
					Attrs: map[string]any{
						"ecosystem": "go", "name": req.Mod.Path, "version": req.Mod.Version,
						"version_info": version.Parse(req.Mod.Version).Attrs(), "direct": false,
						"scope": "unknown", "lockfile": file, "roots": []any{dir}, "via": []any{},
					},
					Plugin:   "deps-go",
					Evidence: []fact.Evidence{{Path: file, Line: req.Syntax.Start.Line}},
				})
				continue
			}
			attrs := map[string]any{
				"ecosystem":    "go",
				"name":         req.Mod.Path,
				"version":      req.Mod.Version,
				"version_info": version.Parse(req.Mod.Version).Attrs(),
				"direct":       true,
				"scope":        imports[dir].scope(req.Mod.Path),
			}
			if p, ok := replaced[req.Mod.Path]; ok {
				attrs["internal"] = map[string]any{"name": req.Mod.Path, "path": p, "spec": "replace"}
			} else if p, ok := modules[req.Mod.Path]; ok && work[dir] != "" && work[dir] == work[p] {
				attrs["internal"] = map[string]any{"name": req.Mod.Path, "path": p, "spec": "go.work"}
			}
			res.Facts = append(res.Facts, fact.Fact{
				Kind:     fact.KindDependency,
				Key:      "dependency:go:" + dir + ":" + req.Mod.Path,
				Attrs:    attrs,
				Plugin:   "deps-go",
				Evidence: []fact.Evidence{{Path: file, Line: req.Syntax.Start.Line, Unit: dir}},
			})
		}
	}
	return res
}

// workspaces maps every module folder a go.work uses to that go.work.
func workspaces(r *repo.Repo, res *plugin.Result) map[string]string {
	out := map[string]string{}
	for _, file := range r.Match(func(b string) bool { return b == "go.work" }) {
		data, err := r.Read(file)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		wf, err := modfile.ParseWork(file, data, nil)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		for _, u := range wf.Use {
			if p, ok := repo.Join(repo.Dir(file), u.Path); ok {
				out[p] = file
			}
		}
	}
	return out
}

// importSet holds the import paths of one module's Go files.
type importSet struct{ prod, test map[string]bool }

func (s importSet) scope(module string) string {
	in := func(set map[string]bool) bool {
		for p := range set {
			if p == module || strings.HasPrefix(p, module+"/") {
				return true
			}
		}
		return false
	}
	switch {
	case in(s.prod):
		return "prod"
	case in(s.test):
		return "test"
	}
	return "unknown"
}

// moduleImports reads only the import lines of every tracked Go file and
// groups them by the module (nearest go.mod) the file belongs to.
func moduleImports(r *repo.Repo, res *plugin.Result) map[string]importSet {
	out := map[string]importSet{}
	for _, f := range r.Files {
		if !strings.HasSuffix(f, ".go") || strings.Contains("/"+f, "/testdata/") {
			continue
		}
		mod := ""
		for d := path.Dir(f); ; d = path.Dir(d) {
			if r.Has(path.Join(d, "go.mod")) {
				mod = d
				break
			}
			if d == "." || d == "/" {
				break
			}
		}
		if mod == "" {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, data, parser.ImportsOnly)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		set, ok := out[mod]
		if !ok {
			set = importSet{prod: map[string]bool{}, test: map[string]bool{}}
			out[mod] = set
		}
		target := set.prod
		if strings.HasSuffix(f, "_test.go") {
			target = set.test
		}
		for _, imp := range file.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				target[p] = true
			}
		}
	}
	return out
}
