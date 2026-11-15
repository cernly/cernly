package deps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/redact"
	ver "github.com/cernly/cernly/internal/version"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugins/dotnet/msbuild"
	"github.com/cernly/cernly/repo"
)

type NugetPlugin struct{}

func (NugetPlugin) Name() string    { return "deps-nuget" }
func (NugetPlugin) Version() string { return "0.1.0" }

func (NugetPlugin) Watches(p string) bool {
	b := path.Base(p)
	switch strings.ToLower(path.Ext(b)) {
	case ".csproj", ".fsproj", ".vbproj":
		return true
	}
	lb := strings.ToLower(b)
	return (strings.HasPrefix(lb, "directory.") && strings.HasSuffix(lb, ".props")) ||
		lb == "packages.lock.json" || lb == "packages.config"
}

// Run reports the direct dependencies of every .NET project file:
// PackageReference items with their versions resolved by the msbuild reader,
// ProjectReference items to other projects of the repo as internal
// dependencies, and, for a non-SDK-style project only, the entries of its sibling packages.config
// (which lists transitive packages too, so every entry is direct).
//
// The key carries the lower-cased NuGet id, since ids are case-insensitive; name
// keeps the spelling as written. A package listed twice in one project (for
// example per target framework) is reported once, from its first line.
// A project reference that leaves the repo, or names a project the repo does not
// contain, gives no fact.
func (NugetPlugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	projects, errs := msbuild.Projects(r)
	res.Errors = append(res.Errors, errs...)
	byPath := map[string]*msbuild.Project{}
	for _, p := range projects {
		byPath[p.Path] = p
	}
	type lockUse struct {
		g    *lockGraph
		root lockRoot
	}
	var locks []lockUse
	for _, p := range projects {
		scope := func(privateAll bool) string {
			switch {
			case p.IsTest:
				return "test"
			case privateAll:
				return "build"
			}
			return "prod"
		}
		prefix := "dependency:nuget:" + p.Path + ":"
		seen := map[string]int{} // key -> index into res.Facts
		// direct holds the lock ids of the project's packages, with their scope.
		direct := map[string]string{}
		add := func(suffix string, attrs map[string]any, ev []fact.Evidence) bool {
			key := prefix + suffix
			if _, ok := seen[key]; ok {
				return false
			}
			seen[key] = len(res.Facts)
			evs := make([]fact.Evidence, len(ev))
			for i, e := range ev {
				e.Unit = p.Dir
				evs[i] = e
			}
			res.Facts = append(res.Facts, fact.Fact{
				Kind: fact.KindDependency, Key: key, Attrs: attrs, Plugin: "deps-nuget", Evidence: evs,
			})
			return true
		}
		addPkg := func(id string, attrs map[string]any, ev []fact.Evidence) {
			if add(strings.ToLower(id), attrs, ev) {
				direct[strings.ToLower(id)], _ = attrs["scope"].(string)
			}
		}
		for _, pk := range p.Packages {
			version := redact.Userinfo(pk.Version)
			attrs := nugetAttrs(pk.ID, version, scope(pk.PrivateAll))
			ext := map[string]any{}
			if pk.Unresolved {
				ext["unresolved"] = true
			}
			if len(pk.Candidates) > 0 {
				cands := make([]any, len(pk.Candidates))
				for i, c := range pk.Candidates {
					cands[i] = map[string]any{"version": redact.Userinfo(c.Raw), "condition": c.Condition, "path": c.Ev.Path, "line": c.Ev.Line}
				}
				ext["candidates"] = cands
			}
			if pk.Condition != "" {
				ext["condition"] = pk.Condition
			}
			if pk.Global {
				ext["global"] = true
			}
			if len(ext) > 0 {
				attrs["ext"] = ext
			}
			addPkg(pk.ID, attrs, pk.Ev)
		}
		// One fact per referenced project, keyed by its path; a project
		// referenced twice (per target framework) has every line and applies
		// when any of its conditions holds.
		var targets []*msbuild.Project
		conds := map[string][]string{}
		for _, pr := range p.ProjectRefs {
			target := byPath[pr.Path]
			if pr.Path == "" || target == nil || target == p {
				continue // leaves the repo, not a project the reader holds, or itself
			}
			ev := fact.Evidence{Path: pr.File, Line: pr.Line, Unit: p.Dir}
			conds[target.Path] = append(conds[target.Path], pr.Condition)
			if i, ok := seen[prefix+"project:"+target.Path]; ok {
				res.Facts[i].Evidence = append(res.Facts[i].Evidence, ev)
				continue
			}
			attrs := nugetAttrs(target.Name, "", scope(false))
			attrs["internal"] = map[string]any{"name": target.Name, "path": target.Dir, "spec": pr.Include}
			add("project:"+target.Path, attrs, []fact.Evidence{ev})
			targets = append(targets, target)
		}
		for _, target := range targets {
			if c := msbuild.AnyCondition(conds[target.Path]); c != "" {
				res.Facts[seen[prefix+"project:"+target.Path]].Attrs["ext"] = map[string]any{"condition": c}
			}
		}
		if cfg := path.Join(p.Dir, "packages.config"); !p.SDKStyle && r.Has(cfg) {
			entries, err := msbuild.PackagesConfig(r, cfg)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: cfg, Message: err.Error()})
			}
			for _, e := range entries {
				if e.ID == "" {
					continue
				}
				s := "prod"
				if e.Dev {
					s = "build"
				}
				if p.IsTest {
					s = "test"
				}
				attrs := nugetAttrs(e.ID, redact.Userinfo(e.Version), s)
				attrs["ext"] = map[string]any{"packages_config": true}
				addPkg(e.ID, attrs, []fact.Evidence{{Path: cfg, Line: e.Line}})
			}
		}
		if lp := path.Join(p.Dir, "packages.lock.json"); r.Has(lp) {
			g, err := readNugetLock(r, lp)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: lp, Message: err.Error()})
				continue
			}
			// The key names the project; the roots name its folder, the unit its
			// direct dependencies carry (scan.v1: roots are manifest folders).
			g.dir = p.Path
			root := lockRoot{dir: p.Dir, direct: map[string]string{}}
			for id, sc := range direct {
				root.direct[id] = sc
			}
			// A referenced project is walked from its lock entry: named by the
			// project name, lower-cased, of type Project.
			for _, target := range targets {
				id := strings.ToLower(target.Name)
				if n := g.nodes[id]; n != nil && n.lockType == "Project" {
					root.direct[id] = scope(false)
				}
			}
			// NuGet locks what restore saw as direct, implicit SDK references
			// included. One the project file does not show is reported from the
			// lock, at the default scope, and starts a walk like the others.
			ids := make([]string, 0, len(g.nodes))
			for id := range g.nodes {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				n := g.nodes[id]
				if _, ok := root.direct[id]; ok || n.lockType != "Direct" || n.internal {
					continue
				}
				root.direct[id] = scope(false)
				version := redact.Userinfo(n.version)
				attrs := nugetAttrs(n.name, version, scope(false))
				if version == "" {
					delete(attrs, "version_info") // as a transitive fact
				}
				attrs["lockfile"] = lp
				attrs["roots"] = []any{p.Dir}
				ext := map[string]any{"lock_direct": true}
				for k, x := range n.ext {
					ext[k] = x
				}
				attrs["ext"] = ext
				var ev []fact.Evidence
				for _, line := range n.evLines() {
					ev = append(ev, fact.Evidence{Path: lp, Line: line, Unit: p.Dir})
				}
				res.Facts = append(res.Facts, fact.Fact{
					Kind: fact.KindDependency, Key: prefix + "lock:" + id, Attrs: attrs, Plugin: "deps-nuget",
					Evidence: ev,
				})
			}
			locks = append(locks, lockUse{g, root})
		}
	}
	for _, l := range locks {
		res.Facts = append(res.Facts, l.g.transitive([]lockRoot{l.root})...)
	}
	return res
}

// readNugetLock reads a packages.lock.json: per target framework, the locked
// packages with their resolved version and the packages each one requires.
// A package is one node across frameworks (its frameworks in ext.frameworks;
// its type, Direct when any framework says so). Its version is the one every
// framework resolves; when they differ it is "", with ext.unresolved and each
// framework's version and line in ext.framework_versions, and one evidence
// line per distinct version. Project entries
// are internal: walked, never reported. The caller sets the graph's dir to the
// project that owns the lockfile.
func readNugetLock(r *repo.Repo, p string) (*lockGraph, error) {
	data, err := r.Read(p)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	var newlines []int64 // offsets of every '\n', built once
	for i, c := range data {
		if c == '\n' {
			newlines = append(newlines, int64(i))
		}
	}
	lineAt := func(off int64) int {
		return sort.Search(len(newlines), func(i int) bool { return newlines[i] >= off }) + 1
	}
	expect := func(want json.Delim, what string) error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); !ok || d != want {
			return fmt.Errorf("%s is not an object", what)
		}
		return nil
	}
	if err := expect('{', "packages.lock.json"); err != nil {
		return nil, err
	}
	g := &lockGraph{path: p, dir: repo.Dir(p), eco: "nuget", plugin: "deps-nuget", nodes: map[string]*lockNode{}}
	type fwVersion struct {
		version string
		line    int
	}
	frameworks := map[string]map[string]fwVersion{}
	found := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if kt != "dependencies" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		found = true
		if err := expect('{', "dependencies"); err != nil {
			return nil, err
		}
		for dec.More() {
			ft, err := dec.Token()
			if err != nil {
				return nil, err
			}
			fw, _ := ft.(string)
			if err := expect('{', "dependencies."+fw); err != nil {
				return nil, err
			}
			for dec.More() {
				nt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				name, _ := nt.(string)
				line := lineAt(dec.InputOffset())
				var e struct {
					Type         string            `json:"type"`
					Resolved     string            `json:"resolved"`
					Dependencies map[string]string `json:"dependencies"`
				}
				if err := dec.Decode(&e); err != nil {
					return nil, fmt.Errorf("dependencies.%s[%q]: %w", fw, name, err)
				}
				id := strings.ToLower(name)
				n := g.nodes[id]
				if n == nil {
					n = &lockNode{id: id, name: name, version: e.Resolved, line: line}
					g.nodes[id] = n
				}
				n.internal = n.internal || e.Type == "Project"
				if n.lockType != "Direct" {
					n.lockType = e.Type
				}
				for dep := range e.Dependencies {
					d := strings.ToLower(dep)
					dup := false
					for _, x := range n.deps {
						dup = dup || x == d
					}
					if !dup {
						n.deps = append(n.deps, d)
					}
				}
				if frameworks[id] == nil {
					frameworks[id] = map[string]fwVersion{}
				}
				frameworks[id][fw] = fwVersion{e.Resolved, line}
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("no dependencies section: transitive dependencies not read")
	}
	for id, n := range g.nodes {
		sort.Strings(n.deps)
		fws := make([]any, 0, len(frameworks[id]))
		lineOf := map[string]int{} // version -> its first line
		for fw, fv := range frameworks[id] {
			fws = append(fws, fw)
			if l, ok := lineOf[fv.version]; !ok || fv.line < l {
				lineOf[fv.version] = fv.line
			}
		}
		sort.Slice(fws, func(i, j int) bool { return fws[i].(string) < fws[j].(string) })
		n.ext = map[string]any{"frameworks": fws}
		if len(lineOf) > 1 {
			// The frameworks disagree: no one version, each listed.
			byFw := map[string]any{}
			for fw, fv := range frameworks[id] {
				byFw[fw] = map[string]any{"version": redact.Userinfo(fv.version), "line": fv.line}
			}
			n.version = ""
			n.ext["unresolved"] = true
			n.ext["framework_versions"] = byFw
			for _, l := range lineOf {
				n.lines = append(n.lines, l)
			}
			sort.Ints(n.lines)
		}
	}
	return g, nil
}

func nugetAttrs(name, version, scope string) map[string]any {
	return map[string]any{
		"ecosystem":    "nuget",
		"name":         name,
		"version":      version,
		"version_info": ver.Parse(version).Attrs(),
		"direct":       true,
		"scope":        scope,
	}
}
