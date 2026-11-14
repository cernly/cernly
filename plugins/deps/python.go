package deps

import (
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/redact"
	ver "github.com/cernly/cernly/internal/version"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

// PythonPlugin reads Python dependencies: pyproject.toml (PEP 621 [project],
// [dependency-groups], Poetry tables) and requirements*.txt (PEP 508 lines,
// -r includes), with the exact version from a lockfile in the same folder or
// above (uv.lock, poetry.lock, Pipfile.lock). Transitive dependencies come
// from the same lockfile: uv.lock and poetry.lock record which package
// requires which; Pipfile.lock does not, so its other packages are
// transitive with no via. Nothing is installed or run; setup.py is code and
// is not read.
type PythonPlugin struct{}

func (PythonPlugin) Name() string    { return "deps-python" }
func (PythonPlugin) Version() string { return "0.2.2" }

func (PythonPlugin) Watches(p string) bool {
	b := path.Base(p)
	return b == "pyproject.toml" || isRequirements(b) || b == "uv.lock" || b == "poetry.lock" || b == "Pipfile.lock"
}

func isRequirements(b string) bool {
	return strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")
}

var nonName = regexp.MustCompile(`[-_.]+`)

// normalise is PEP 503's name normalisation: case-insensitive, runs of - _ .
// are one -.
func normalise(name string) string { return nonName.ReplaceAllString(strings.ToLower(name), "-") }

type pyDep struct {
	name, spec, scope, file string
	line                    int
	// transitive: a compiled requirements file (pip-compile, uv pip
	// compile) says only other packages pull it in (# via, no -r).
	transitive bool
	via        []string
}

func (PythonPlugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	dirs := map[string]bool{}
	for _, f := range r.Files {
		if b := path.Base(f); b == "pyproject.toml" || isRequirements(b) {
			dirs[repo.Dir(f)] = true
		}
	}
	units := make([]string, 0, len(dirs))
	for d := range dirs {
		units = append(units, d)
	}
	sort.Strings(units)
	locks := map[string]*pyLock{}
	rootsOf := map[string][]lockRoot{}
	for _, dir := range units {
		var deps []pyDep
		if p := path.Join(dir, "pyproject.toml"); r.Has(p) {
			ds, err := readPyproject(r, p)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: p, Message: err.Error()})
			}
			deps = append(deps, ds...)
		}
		ds, errs := readRequirements(r, dir)
		res.Errors = append(res.Errors, errs...)
		deps = append(deps, ds...)

		pl := nearestPyLock(r, dir, locks, &res)
		var lock map[string]string
		if pl != nil {
			lock = pl.versions
		}
		root := lockRoot{dir: dir, direct: map[string]string{}}
		seen := map[string]int{}
		var facts []fact.Fact
		for _, d := range deps {
			key := "dependency:pypi:" + dir + ":" + d.name
			ev := fact.Evidence{Path: d.file, Line: d.line, Unit: dir}
			if i, ok := seen[d.name]; ok {
				// Declared twice (pyproject and requirements, or two groups):
				// one fact; the first declaration's scope, every place as evidence.
				facts[i].Evidence = append(facts[i].Evidence, ev)
				continue
			}
			version := d.spec
			if v := strings.TrimPrefix(version, "=="); v != version && !strings.ContainsAny(v, ",*;") {
				version = v
			}
			if v, ok := lock[d.name]; ok {
				version = v
			}
			version = redact.Userinfo(version) // name @ git+https://user:token@…
			attrs := map[string]any{"ecosystem": "pypi", "name": d.name, "version": version, "direct": true, "scope": d.scope}
			if version != "" {
				attrs["version_info"] = ver.Parse(version).Attrs()
			}
			if d.transitive {
				attrs["direct"], attrs["lockfile"], attrs["roots"], attrs["via"] = false, d.file, []any{dir}, fact.Strings(d.via)
				seen[d.name] = len(facts)
				facts = append(facts, fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: attrs, Plugin: "deps-python", Evidence: []fact.Evidence{ev}})
				continue
			}
			if prev, ok := root.direct[d.name]; !ok || prev == "dev" {
				root.direct[d.name] = d.scope
			}
			seen[d.name] = len(facts)
			facts = append(facts, fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: attrs, Plugin: "deps-python", Evidence: []fact.Evidence{ev}})
		}
		res.Facts = append(res.Facts, facts...)
		if pl != nil && pl.graph != nil {
			rootsOf[pl.graph.path] = append(rootsOf[pl.graph.path], root)
		}
	}
	paths := make([]string, 0, len(rootsOf))
	for p := range rootsOf {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		res.Facts = append(res.Facts, locks[p].transitive(rootsOf[p])...)
	}
	return res
}

// groupScope maps a dependency group's name to a scope.
func groupScope(g string) string {
	switch strings.ToLower(g) {
	case "dev", "development", "lint", "linting", "docs", "doc", "typing", "types", "format":
		return "dev"
	case "test", "tests", "testing":
		return "test"
	}
	return "unknown"
}

type pyproject struct {
	Project struct {
		Dependencies         []string            `toml:"dependencies"`
		OptionalDependencies map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	DependencyGroups map[string][]any `toml:"dependency-groups"`
	Tool             struct {
		UV struct {
			DevDependencies []string `toml:"dev-dependencies"`
		} `toml:"uv"`
		Poetry struct {
			Dependencies    map[string]any `toml:"dependencies"`
			DevDependencies map[string]any `toml:"dev-dependencies"`
			Group           map[string]struct {
				Dependencies map[string]any `toml:"dependencies"`
			} `toml:"group"`
		} `toml:"poetry"`
	} `toml:"tool"`
}

func readPyproject(r *repo.Repo, file string) ([]pyDep, error) {
	data, err := r.Read(file)
	if err != nil {
		return nil, err
	}
	var p pyproject
	if _, err := toml.Decode(string(data), &p); err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var out []pyDep
	add := func(req, scope string) {
		name, spec, ok := parseRequirement(req)
		if !ok {
			return
		}
		out = append(out, pyDep{name: name, spec: spec, scope: scope, file: file, line: findQuoted(lines, req)})
	}
	for _, req := range p.Project.Dependencies {
		add(req, "prod")
	}
	for _, g := range slices.Sorted(maps.Keys(p.Project.OptionalDependencies)) {
		for _, req := range p.Project.OptionalDependencies[g] {
			add(req, "prod")
		}
	}
	for _, g := range slices.Sorted(maps.Keys(p.DependencyGroups)) {
		for _, v := range p.DependencyGroups[g] {
			if req, ok := v.(string); ok { // {include-group = "…"} entries name other groups
				add(req, groupScope(g))
			}
		}
	}
	for _, req := range p.Tool.UV.DevDependencies {
		add(req, "dev")
	}
	poetry := func(table string, deps map[string]any, scope string) {
		for _, name := range slices.Sorted(maps.Keys(deps)) {
			if strings.EqualFold(name, "python") {
				continue
			}
			spec := ""
			switch v := deps[name].(type) {
			case string:
				spec = v
			case map[string]any:
				spec, _ = v["version"].(string)
			}
			out = append(out, pyDep{name: normalise(name), spec: spec, scope: scope, file: file, line: findKey(lines, table, name)})
		}
	}
	poetry("tool.poetry.dependencies", p.Tool.Poetry.Dependencies, "prod")
	poetry("tool.poetry.dev-dependencies", p.Tool.Poetry.DevDependencies, "dev")
	for _, g := range slices.Sorted(maps.Keys(p.Tool.Poetry.Group)) {
		poetry("tool.poetry.group."+g+".dependencies", p.Tool.Poetry.Group[g].Dependencies, groupScope(g))
	}
	return out, nil
}

// findQuoted is the line (1-based) holding the requirement as a TOML string.
func findQuoted(lines []string, req string) int {
	for i, l := range lines {
		if strings.Contains(l, `"`+req+`"`) || strings.Contains(l, `'`+req+`'`) {
			return i + 1
		}
	}
	return 1
}

// findKey is the line of `name =` in the table [table].
func findKey(lines []string, table, name string) int {
	in := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = strings.Trim(t, "[] ") == table
			continue
		}
		if in {
			k, _, ok := strings.Cut(t, "=")
			if ok && strings.Trim(strings.TrimSpace(k), `"'`) == name {
				return i + 1
			}
		}
	}
	return 1
}

var reqName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*(.*)$`)

// parseRequirement reads a PEP 508 requirement: the normalised name and the
// version spec as written (markers after ';' and extras are dropped; a direct
// reference "name @ url" keeps the url as its spec).
func parseRequirement(req string) (name, spec string, ok bool) {
	req = strings.TrimSpace(req)
	if i := strings.Index(req, ";"); i >= 0 {
		req = strings.TrimSpace(req[:i])
	}
	m := reqName.FindStringSubmatch(req)
	if m == nil {
		return "", "", false
	}
	spec = strings.TrimSpace(m[3])
	spec = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(spec, "("), ")"))
	spec = strings.ReplaceAll(spec, " ", "")
	if strings.HasPrefix(spec, "@") {
		spec = strings.TrimSpace(spec[1:])
	}
	return normalise(m[1]), spec, true
}

// readRequirements reads the folder's requirements*.txt. Files another file
// includes with -r are read through that file; the roots are read runtime
// first, so a dev file that includes the runtime one adds only its own.
func readRequirements(r *repo.Repo, dir string) ([]pyDep, []fact.LocationError) {
	var files []string
	for _, f := range r.Files {
		if repo.Dir(f) == dir && isRequirements(path.Base(f)) {
			files = append(files, f)
		}
	}
	included := map[string]bool{}
	parsed := map[string][]reqLine{}
	var errs []fact.LocationError
	for _, f := range files {
		data, err := r.Read(f)
		if err != nil {
			errs = append(errs, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		parsed[f] = parseReqFile(string(data))
		for _, l := range parsed[f] {
			if l.include != "" {
				if p, ok := repo.Join(dir, l.include); ok {
					included[p] = true
				}
			}
		}
	}
	scopeOf := func(f string) string {
		b := strings.ToLower(path.Base(f))
		switch {
		case strings.Contains(b, "test"):
			return "test"
		case strings.Contains(b, "dev") || strings.Contains(b, "lint") || strings.Contains(b, "doc"):
			return "dev"
		}
		return "prod"
	}
	// A file nothing includes is read on its own, and so is every runtime
	// file: requirements-dev.txt including requirements.txt does not make the
	// runtime packages dev ones.
	roots := []string{}
	for _, f := range files {
		if !included[f] || scopeOf(f) == "prod" {
			roots = append(roots, f)
		}
	}
	rank := map[string]int{"prod": 0, "test": 1, "dev": 2}
	sort.SliceStable(roots, func(i, j int) bool { return rank[scopeOf(roots[i])] < rank[scopeOf(roots[j])] })
	var out []pyDep
	seen := map[string]bool{}
	// visited holds the shallowest depth each file was expanded at. Expanding
	// it again adds nothing (seen drops repeated names) unless the depth cap
	// cut the first expansion short, so a diamond of includes costs each
	// file once instead of once per path, and a cycle ends at once.
	visited := map[string]int{}
	var walk func(f, scope string, depth int)
	walk = func(f, scope string, depth int) {
		if depth > 8 {
			return
		}
		if d, ok := visited[f]; ok && d <= depth {
			return
		}
		visited[f] = depth
		ls, ok := parsed[f]
		if !ok {
			if data, err := r.Read(f); err == nil {
				ls = parseReqFile(string(data))
				parsed[f] = ls
			}
		}
		for _, l := range ls {
			if l.include != "" {
				if p, ok := repo.Join(repo.Dir(f), l.include); ok && r.Has(p) {
					walk(p, scope, depth+1)
				}
				continue
			}
			if seen[l.name] {
				continue
			}
			seen[l.name] = true
			out = append(out, pyDep{name: l.name, spec: l.spec, scope: scope, file: f, line: l.line, transitive: l.transitive, via: l.via})
		}
	}
	for _, f := range roots {
		walk(f, scopeOf(f), 0)
	}
	return out, errs
}

type reqLine struct {
	name, spec, include string
	line                int
	transitive          bool
	via                 []string
}

// parseReqFile reads pip's requirements format: one requirement per line,
// backslash continuations, # comments, -r/--requirement includes; other
// options (-c, -e, --index-url, --hash …) are skipped. A compiled file
// (pip-compile, uv pip compile) annotates each requirement with # via
// lines: one pulled in only by other packages is transitive, one with
// "-r <file>" among them was declared there.
func parseReqFile(src string) []reqLine {
	var out []reqLine
	lines := strings.Split(src, "\n")
	vias := map[int][]string{} // index in out -> via entries as written
	inVia := -1
	for i := 0; i < len(lines); i++ {
		start := i + 1
		l := lines[i]
		if c := strings.TrimSpace(l); strings.HasPrefix(c, "#") {
			c = strings.TrimSpace(strings.TrimPrefix(c, "#"))
			switch {
			case len(out) > 0 && out[len(out)-1].name != "" && (c == "via" || strings.HasPrefix(c, "via ")):
				inVia = len(out) - 1
				vias[inVia] = append(vias[inVia], "")
				if v := strings.TrimSpace(strings.TrimPrefix(c, "via")); v != "" {
					vias[inVia] = append(vias[inVia], v)
				}
			case inVia >= 0 && c != "":
				vias[inVia] = append(vias[inVia], c)
			default:
				inVia = -1
			}
			continue
		}
		inVia = -1
		for strings.HasSuffix(strings.TrimRight(l, " \t\r"), `\`) && i+1 < len(lines) {
			l = strings.TrimSuffix(strings.TrimRight(l, " \t\r"), `\`) + " " + lines[i+1]
			i++
		}
		if j := strings.Index(l, " #"); j >= 0 {
			l = l[:j]
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "-") {
			f := strings.Fields(l)
			switch {
			case (f[0] == "-r" || f[0] == "--requirement") && len(f) > 1:
				out = append(out, reqLine{include: f[1], line: start})
			case strings.HasPrefix(f[0], "--requirement="):
				out = append(out, reqLine{include: strings.TrimPrefix(f[0], "--requirement="), line: start})
			}
			continue
		}
		if f := strings.Fields(l); len(f) > 1 && strings.HasPrefix(f[len(f)-1], "--hash") {
			l = strings.Split(l, " --hash")[0]
		}
		if name, spec, ok := parseRequirement(l); ok {
			out = append(out, reqLine{name: name, spec: spec, line: start})
		}
	}
	for i, vs := range vias {
		declared := false
		var pkgs []string
		for _, v := range vs {
			switch {
			case v == "":
			case strings.HasPrefix(v, "-r ") || strings.HasPrefix(v, "--requirement"):
				declared = true
			case strings.HasPrefix(v, "-"):
				// -c constraints.txt, --override: not a declaration
			default:
				if name, _, ok := parseRequirement(strings.Fields(v)[0]); ok {
					pkgs = append(pkgs, name)
				}
			}
		}
		if !declared && len(pkgs) > 0 {
			sort.Strings(pkgs)
			out[i].transitive, out[i].via = true, pkgs
		}
	}
	return out
}

// pyLock is one Python lockfile: exact versions by normalised name (a name
// locked at more than one version is left out) and the package graph.
type pyLock struct {
	versions map[string]string
	graph    *lockGraph
	flat     bool // Pipfile.lock: no graph; every other package is transitive
}

// transitive is the graph walk, or for a flat lockfile every package no root
// declares, on every root, with no via: a hub node stands in for the graph the
// lockfile does not record.
func (l *pyLock) transitive(roots []lockRoot) []fact.Fact {
	if !l.flat {
		return l.graph.transitive(roots)
	}
	direct := map[string]bool{}
	for _, r := range roots {
		for id := range r.direct {
			direct[id] = true
		}
	}
	hub := &lockNode{id: "\x00", internal: true}
	for id := range l.graph.nodes {
		if !direct[id] {
			hub.deps = append(hub.deps, id)
		}
	}
	sort.Strings(hub.deps)
	l.graph.nodes[hub.id] = hub
	defer delete(l.graph.nodes, hub.id)
	var rs []lockRoot
	for _, r := range roots {
		rs = append(rs, lockRoot{dir: r.dir, direct: map[string]string{hub.id: "prod"}})
	}
	return l.graph.transitive(rs)
}

// nearestPyLock reads the lockfile of a folder, or of the nearest folder above
// it (a uv workspace locks at its root).
func nearestPyLock(r *repo.Repo, dir string, cache map[string]*pyLock, res *plugin.Result) *pyLock {
	for d := dir; ; d = path.Dir(d) {
		for _, name := range []string{"uv.lock", "poetry.lock", "Pipfile.lock"} {
			p := path.Join(d, name)
			if d == "." {
				p = name
			}
			if !r.Has(p) {
				continue
			}
			if l, ok := cache[p]; ok {
				return l
			}
			l, err := readPyLock(r, p)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: p, Message: err.Error()})
			}
			cache[p] = l
			return l
		}
		if d == "." || d == "/" {
			return nil
		}
	}
}

func readPyLock(r *repo.Repo, p string) (*pyLock, error) {
	data, err := r.Read(p)
	if err != nil {
		return nil, err
	}
	g := &lockGraph{path: p, dir: repo.Dir(p), eco: "pypi", plugin: "deps-python", nodes: map[string]*lockNode{}}
	l := &pyLock{graph: g}
	versions := map[string][]string{}
	lines := strings.Split(string(data), "\n")
	if path.Base(p) == "Pipfile.lock" {
		l.flat = true
		var lock map[string]json.RawMessage
		if err := json.Unmarshal(data, &lock); err != nil {
			return nil, err
		}
		for _, section := range []string{"default", "develop"} {
			var pkgs map[string]struct {
				Version string `json:"version"`
			}
			if raw, ok := lock[section]; ok {
				if err := json.Unmarshal(raw, &pkgs); err != nil {
					return nil, err
				}
			}
			for name, v := range pkgs {
				id, version := normalise(name), strings.TrimPrefix(v.Version, "==")
				versions[id] = append(versions[id], version)
				if g.nodes[id] != nil {
					continue // in both sections: default wins
				}
				scope := "prod"
				if section == "develop" {
					scope = "dev"
				}
				g.nodes[id] = &lockNode{id: id, name: id, version: version, scope: scope, line: findLine(lines, `"`+name+`": {`)}
			}
		}
	} else {
		var lock struct {
			Package []struct {
				Name         string         `toml:"name"`
				Version      string         `toml:"version"`
				Source       map[string]any `toml:"source"`
				Dependencies any            `toml:"dependencies"`
			} `toml:"package"`
		}
		if _, err := toml.Decode(string(data), &lock); err != nil {
			return nil, err
		}
		pkgLines := packageLines(lines)
		for _, pk := range lock.Package {
			id := normalise(pk.Name)
			versions[id] = append(versions[id], pk.Version)
			if g.nodes[id] != nil {
				continue // locked twice (uv markers): the first entry
			}
			n := &lockNode{id: id, name: id, version: pk.Version, line: pkgLines[id]}
			for _, k := range []string{"editable", "virtual", "directory", "path"} {
				if _, ok := pk.Source[k]; ok {
					n.internal = true // the project itself or a workspace member
				}
			}
			n.deps = pyDeps(pk.Dependencies)
			g.nodes[id] = n
		}
	}
	l.versions = map[string]string{}
	for name, vs := range versions {
		sort.Strings(vs)
		if len(vs) > 0 && vs[0] == vs[len(vs)-1] && vs[0] != "" {
			l.versions[name] = vs[0]
		}
	}
	return l, nil
}

// pyDeps reads a package's dependencies: uv writes a list of tables with a
// name, poetry a table of name to spec.
func pyDeps(v any) []string {
	var out []string
	switch d := v.(type) {
	case []map[string]any:
		for _, x := range d {
			if n, ok := x["name"].(string); ok {
				out = append(out, normalise(n))
			}
		}
	case []any:
		for _, x := range d {
			if m, ok := x.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					out = append(out, normalise(n))
				}
			}
		}
	case map[string]any:
		for n := range d {
			out = append(out, normalise(n))
		}
	}
	sort.Strings(out)
	return out
}

// packageLines finds the line of each [[package]]'s name in a TOML lockfile.
func packageLines(lines []string) map[string]int {
	out := map[string]int{}
	in := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "[[package]]" {
			in = true
			continue
		}
		if in && strings.HasPrefix(t, "name = ") {
			id := normalise(strings.Trim(strings.TrimPrefix(t, "name = "), `"'`))
			if _, ok := out[id]; !ok {
				out[id] = i + 1
			}
			in = false
		}
	}
	return out
}

func findLine(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	return 1
}
