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
	"github.com/cernly/cernly/repo"
)

type NpmPlugin struct{}

func NewNpm() plugin.Plugin { return NpmPlugin{} }

func (NpmPlugin) Name() string    { return "deps-npm" }
func (NpmPlugin) Version() string { return "0.2.1" }

func (NpmPlugin) Watches(p string) bool {
	b := path.Base(p)
	return b == "package.json" || b == "package-lock.json"
}

var sections = map[string]string{
	"dependencies":         "prod",
	"optionalDependencies": "prod",
	"devDependencies":      "dev",
}

// Run reports the direct dependencies of every package.json. The version is
// the exact one from the nearest package-lock.json when it has the package,
// else the declared range as written (a URL in either loses its
// credentials); the licence is the lockfile's, when it records one.
//
// Transitive dependencies come from package-lock.json (lockfile version 2 or
// 3, the packages section): one fact per installed location that a manifest
// reaches only through another package, resolved the way npm resolves
// node_modules. Without a lockfile, direct dependencies only.
//
// A dependency is internal when it names a member of the workspace its
// manifest belongs to, or its spec is workspace:, file: or link: pointing to
// a tracked package.json. It then carries internal {name, path, spec} and is
// never a third-party package.
func (NpmPlugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	ws := readWorkspaces(r)
	locks := map[string]*npmLock{} // lockfile path -> parsed lock
	rootsOf := map[string][]lockRoot{}
	for _, file := range r.Match(func(b string) bool { return b == "package.json" }) {
		data, err := r.Read(file)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		entries, err := scanManifest(data)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		dir := repo.Dir(file)
		lockPath, lockDir := nearestLock(r, dir)
		var lock *npmLock
		if lockPath != "" {
			if l, ok := locks[lockPath]; ok {
				lock = l
			} else {
				l, err := readLock(r, lockPath)
				if err != nil {
					res.Errors = append(res.Errors, fact.LocationError{Path: lockPath, Message: err.Error()})
				}
				locks[lockPath] = l
				lock = l
			}
		}
		root := lockRoot{dir: dir, direct: map[string]string{}}
		for _, e := range entries {
			version := e.declared
			loc := lock.resolve(lock.rel(lockDir, dir), e.name)
			license := ""
			if n := lock.node(loc); n != nil {
				if n.version != "" && !n.internal {
					version = n.version
				}
				if !n.internal {
					license = n.license
				}
				if prev, ok := root.direct[loc]; !ok || prev == "dev" {
					root.direct[loc] = e.scope
				}
			}
			version = redact.Userinfo(version)
			attrs := map[string]any{
				"ecosystem":    "npm",
				"name":         e.name,
				"version":      version,
				"version_info": ver.Parse(version).Attrs(),
				"direct":       true,
				"scope":        e.scope,
			}
			if p, ok := ws.internal(r, dir, e.name, e.declared); ok {
				attrs["internal"] = map[string]any{"name": e.name, "path": p, "spec": redact.Userinfo(e.declared)}
			} else if license != "" {
				attrs["license"] = license
			}
			res.Facts = append(res.Facts, fact.Fact{
				Kind:     fact.KindDependency,
				Key:      "dependency:npm:" + dir + ":" + e.name,
				Attrs:    attrs,
				Plugin:   "deps-npm",
				Evidence: []fact.Evidence{{Path: file, Line: e.line, Unit: dir}},
			})
		}
		if lock != nil && lock.graph != nil {
			rootsOf[lockPath] = append(rootsOf[lockPath], root)
		}
	}
	paths := make([]string, 0, len(rootsOf))
	for p := range rootsOf {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		res.Facts = append(res.Facts, locks[p].graph.transitive(rootsOf[p])...)
	}
	return res
}

// workspaceSet holds, per workspace root folder, its members by package name.
type workspaceSet map[string]map[string]string

// readWorkspaces reads the workspaces field (array, or {packages: [...]}) of
// every tracked package.json and the name of every member it matches.
func readWorkspaces(r *repo.Repo) workspaceSet {
	type manifest struct {
		Name       string          `json:"name"`
		Workspaces json.RawMessage `json:"workspaces"`
	}
	manifests := map[string]manifest{}
	files := r.Match(func(b string) bool { return b == "package.json" })
	for _, f := range files {
		data, err := r.Read(f)
		if err != nil {
			continue
		}
		var m manifest
		if json.Unmarshal(data, &m) == nil {
			manifests[f] = m
		}
	}
	out := workspaceSet{}
	for _, f := range files {
		var globs []string
		raw := manifests[f].Workspaces
		if json.Unmarshal(raw, &globs) != nil {
			var obj struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal(raw, &obj) != nil {
				continue
			}
			globs = obj.Packages
		}
		root := repo.Dir(f)
		members := map[string]string{}
		for _, m := range files {
			dir := repo.Dir(m)
			name := manifests[m].Name
			if m == f || name == "" {
				continue
			}
			for _, g := range globs {
				if p, ok := repo.Join(root, g); ok && matchDir(p, dir) {
					members[name] = dir
				}
			}
		}
		if len(members) > 0 {
			out[root] = members
		}
	}
	return out
}

// matchDir matches a workspace glob against a folder; ** spans folders.
func matchDir(glob, dir string) bool {
	if strings.Contains(glob, "**") {
		prefix := strings.TrimSuffix(glob[:strings.Index(glob, "**")], "/")
		return prefix == "" || prefix == "." || dir == prefix || strings.HasPrefix(dir, prefix+"/")
	}
	ok, _ := path.Match(glob, dir)
	return ok
}

// internal resolves a dependency of the manifest in dir to a repo folder.
func (ws workspaceSet) internal(r *repo.Repo, dir, name, spec string) (string, bool) {
	for _, proto := range []string{"file:", "link:"} {
		if p, ok := strings.CutPrefix(spec, proto); ok {
			if d, ok := repo.Join(dir, p); ok && r.Has(path.Join(d, "package.json")) {
				return d, true
			}
			return "", false
		}
	}
	// The workspace the manifest belongs to decides, as npm resolves it: the
	// root itself or one of its members. A folder under a root that is not a
	// member resolves from the registry.
	roots := make([]string, 0, len(ws))
	for root := range ws {
		roots = append(roots, root)
	}
	sort.Strings(roots) // nested workspaces: same answer every run
	for _, root := range roots {
		members := ws[root]
		if dir != root && !isMember(members, dir) {
			continue
		}
		p, found := members[name]
		return p, found
	}
	return "", false
}

func isMember(members map[string]string, dir string) bool {
	for _, d := range members {
		if d == dir {
			return true
		}
	}
	return false
}

type entry struct {
	name, declared, scope string
	line                  int
}

// scanManifest walks package.json with a token decoder so every dependency
// keeps the line it is declared on.
func scanManifest(data []byte) ([]entry, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	lineAt := func(off int64) int { return bytes.Count(data[:off], []byte{'\n'}) + 1 }
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("package.json is not an object")
	}
	var out []entry
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := kt.(string)
		scope, isDeps := sections[key]
		if !isDeps {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		if t, err := dec.Token(); err != nil {
			return nil, err
		} else if d, ok := t.(json.Delim); !ok || d != '{' {
			return nil, fmt.Errorf("%s is not an object", key)
		}
		for dec.More() {
			nt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			line := lineAt(dec.InputOffset())
			name, _ := nt.(string)
			var declared string
			if err := dec.Decode(&declared); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", key, name, err)
			}
			out = append(out, entry{name: name, declared: declared, scope: scope, line: line})
		}
		if _, err := dec.Token(); err != nil { // closing }
			return nil, err
		}
	}
	return out, nil
}

func nearestLock(r *repo.Repo, dir string) (lockPath, lockDir string) {
	for d := dir; ; d = path.Dir(d) {
		p := path.Join(d, "package-lock.json")
		if r.Has(p) {
			return p, d
		}
		if d == "." || d == "/" {
			return "", ""
		}
	}
}

// npmLock is a package-lock.json: its packages by location, as a graph.
type npmLock struct {
	graph   *lockGraph
	entries map[string]npmEntry
	links   map[string]string // link location -> the member folder it points to
}

type npmEntry struct {
	Version              string            `json:"version"`
	License              json.RawMessage   `json:"license"`
	Dev                  bool              `json:"dev"`
	Link                 bool              `json:"link"`
	Resolved             string            `json:"resolved"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

// readLock reads the packages section of a lockfile with a token decoder, so
// every location keeps the line it is written on. Lockfile version 1 has no
// packages section: no versions, no graph, and an error that says so.
func readLock(r *repo.Repo, p string) (*npmLock, error) {
	data, err := r.Read(p)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	lineAt := func(off int64) int { return bytes.Count(data[:off], []byte{'\n'}) + 1 }
	if t, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("package-lock.json is not an object")
	}
	entries := map[string]npmEntry{}
	lines := map[string]int{}
	found := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if kt != "packages" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		found = true
		if t, err := dec.Token(); err != nil {
			return nil, err
		} else if d, ok := t.(json.Delim); !ok || d != '{' {
			return nil, fmt.Errorf("packages is not an object")
		}
		for dec.More() {
			lt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			loc, _ := lt.(string)
			lines[loc] = lineAt(dec.InputOffset())
			var e npmEntry
			if err := dec.Decode(&e); err != nil {
				return nil, fmt.Errorf("packages[%q]: %w", loc, err)
			}
			entries[loc] = e
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("no packages section (lockfile version 1): versions and transitive dependencies not read")
	}
	g := &lockGraph{path: p, dir: repo.Dir(p), eco: "npm", plugin: "deps-npm", nodes: map[string]*lockNode{}}
	l := &npmLock{graph: g, entries: entries}
	for loc, e := range entries {
		if e.Link {
			continue // a symlink to a workspace member: resolve() follows it
		}
		n := &lockNode{id: loc, name: npmName(loc), version: e.Version, license: npmLicense(e.License), line: lines[loc]}
		n.internal = !strings.HasPrefix(loc, "node_modules/") && !strings.Contains(loc, "/node_modules/")
		if !n.internal {
			n.scope = "prod"
			if e.Dev {
				n.scope = "dev"
			}
		}
		g.nodes[loc] = n
	}
	links := map[string]string{}
	for loc, e := range entries {
		if e.Link {
			links[loc] = e.Resolved
		}
	}
	l.links = links
	resolve := func(from, name string) string { return resolveLoc(entries, links, from, name) }
	for loc, e := range entries {
		n := g.nodes[loc]
		if n == nil {
			continue
		}
		var names []string
		for _, m := range []map[string]string{e.Dependencies, e.OptionalDependencies, e.PeerDependencies} {
			for name := range m {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if d := resolve(loc, name); d != "" && g.nodes[d] != nil && d != loc {
				n.deps = append(n.deps, d)
			}
		}
	}
	return l, nil
}

// npmName is the package name: the part after the last node_modules/. A
// workspace member keeps its folder path; it is walked, never reported.
func npmName(loc string) string {
	if i := strings.LastIndex(loc, "node_modules/"); i >= 0 {
		return loc[i+len("node_modules/"):]
	}
	return loc
}

// npmLicense reads the lockfile's license field: a string (an SPDX
// expression as the package declares it). Old object and array forms are not
// read.
func npmLicense(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// resolveLoc finds the location npm installs name at for a package at from:
// its own node_modules, then each enclosing node_modules up to the root.
// A link entry resolves to the workspace member it points to.
func resolveLoc(entries map[string]npmEntry, links map[string]string, from, name string) string {
	loc := from
	for {
		cand := "node_modules/" + name
		if loc != "" {
			cand = loc + "/node_modules/" + name
		}
		if target, ok := links[cand]; ok {
			return target
		}
		if _, ok := entries[cand]; ok {
			return cand
		}
		if loc == "" {
			return ""
		}
		if i := strings.LastIndex(loc, "/node_modules/"); i >= 0 {
			loc = loc[:i]
		} else {
			loc = ""
		}
	}
}

// rel is a manifest folder's location in the lockfile: "" for the lockfile's
// own folder, the relative path for a workspace member.
func (l *npmLock) rel(lockDir, dir string) string {
	if l == nil || dir == lockDir {
		return ""
	}
	if lockDir == "." {
		return dir
	}
	return strings.TrimPrefix(dir, lockDir+"/")
}

func (l *npmLock) resolve(from, name string) string {
	if l == nil || l.graph == nil {
		return ""
	}
	return resolveLoc(l.entries, l.links, from, name)
}

func (l *npmLock) node(loc string) *lockNode {
	if l == nil || l.graph == nil || loc == "" {
		return nil
	}
	return l.graph.nodes[loc]
}
