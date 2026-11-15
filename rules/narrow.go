package rules

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
)

// Narrowing says whether a link's sources were cut down to what the image
// builds. Narrowed: the recipe publishes .NET entrypoints whose project closure is known, so the code units outside
// it are left out (Link.Excluded). Kept: anything uncertain keeps the whole
// link; Reason says what was uncertain.
type Narrowing struct {
	State  string `json:"state"` // narrowed or kept
	Reason string `json:"reason,omitempty"`
}

const (
	NarrowingNarrowed = "narrowed"
	NarrowingKept     = "kept"
)

// Precision of a link: entrypoint when narrowed, else module.
func precisionOf(n Narrowing) string {
	if n.State == NarrowingNarrowed {
		return "entrypoint"
	}
	return "module"
}

// Code units are the folders of project files, and of the NuGet files that sit beside one.
var projectExts = []string{".csproj", ".fsproj", ".vbproj"}

func kept(reason string) (Narrowing, []string, []string) {
	return Narrowing{State: NarrowingKept, Reason: reason}, nil, nil
}

// narrow decides the narrowing of one recipe over the repo paths in sources.
// It returns the narrowing, the entrypoint project folders and the excluded
// code-unit folders. A narrowed link must never miss code that is in the image,
// so every doubt keeps the link.
func narrow(recipe fact.Fact, sources []string, facts []fact.Fact) (Narrowing, []string, []string) {
	if len(sources) == 0 {
		return kept("no sources")
	}
	eps, _ := recipe.Attrs["entrypoints"].([]any)
	if len(eps) == 0 {
		return kept("no build entrypoint")
	}
	var entries []string
	var unresolved []string
	reason := ""
	for _, x := range eps {
		e, _ := x.(map[string]any)
		switch anyStr(e["kind"]) {
		case "dotnet":
			ps := anyStrs(e["projects"])
			if e["resolved"] != true || len(ps) == 0 {
				unresolved = append(unresolved, anyStr(e["raw"]))
				continue
			}
			entries = append(entries, ps...)
		case "go":
			reason = "go entrypoints are not narrowed yet"
		default:
			if reason == "" {
				reason = anyStr(e["kind"]) + " entrypoints are not narrowed yet"
			}
		}
	}
	if reason != "" {
		return kept(reason)
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return kept("entrypoint not resolved: " + unresolved[0])
	}
	if others := anyStrs(recipe.Attrs["other_commands"]); len(others) > 0 {
		o := append([]string(nil), others...)
		sort.Strings(o)
		return kept("unknown RUN commands: " + strings.Join(o, ", "))
	}
	graph := false
	edges := map[string][]string{}
	unresolvedItems := map[string][]string{} // project folder -> raw paths it links that did not resolve
	units := map[string]bool{}
	for _, f := range facts {
		if f.Plugin == "dotnet" {
			graph = true
		}
		if f.Kind == fact.KindImport && f.Str("language") == "dotnet" {
			from, to := f.Str("from_dir"), f.Str("to_dir")
			if to != "" {
				edges[from] = append(edges[from], to)
			} else if f.Str("item") != "" {
				unresolvedItems[from] = append(unresolvedItems[from], f.Str("to"))
			}
		}
		addUnits(units, f)
	}
	if !graph {
		return kept("no .NET project graph")
	}
	closure := map[string]bool{}
	var walk func(d string)
	walk = func(d string) {
		if closure[d] {
			return
		}
		closure[d] = true
		for _, to := range edges[d] {
			walk(to)
		}
	}
	entries = uniq(entries)
	for _, d := range entries {
		if !units[d] {
			return kept("entrypoint project not found: " + d)
		}
		walk(d)
	}
	var unres []string
	for d, raws := range unresolvedItems {
		if closure[d] {
			unres = append(unres, raws...)
		}
	}
	if len(unres) > 0 {
		sort.Strings(unres)
		return kept("linked item not resolved: " + unres[0])
	}
	var excluded []string
	for u := range units {
		if closure[u] || !insideAny(u, sources) {
			continue
		}
		keep := false
		for c := range closure {
			// An ancestor of a closure folder, or a folder nested in one, may
			// be compiled into the image: never excluded.
			if inside(c, u) || inside(u, c) {
				keep = true
				break
			}
		}
		if !keep {
			excluded = append(excluded, u)
		}
	}
	sort.Strings(excluded)
	return Narrowing{State: NarrowingNarrowed}, entries, excluded
}

// addUnits adds the code-unit folders one fact's evidence shows: project
// files, and the NuGet files beside a project (an old-style project's
// dependencies are evidenced at packages.config).
func addUnits(units map[string]bool, f fact.Fact) {
	for _, ev := range f.Evidence {
		for _, ext := range projectExts {
			if strings.HasSuffix(ev.Path, ext) {
				units[path.Dir(ev.Path)] = true
			}
		}
		if b := path.Base(ev.Path); b == "packages.config" || b == "packages.lock.json" {
			units[path.Dir(ev.Path)] = true
		}
	}
}

func insideAny(p string, sources []string) bool {
	for _, s := range sources {
		if inside(p, s) {
			return true
		}
	}
	return false
}

// covers reports whether repo path p belongs to a link: inside some source
// and inside no excluded folder.
func covers(p string, sources, excluded []string) bool {
	if !insideAny(p, sources) {
		return false
	}
	for _, x := range excluded {
		if inside(p, x) {
			return false
		}
	}
	return true
}

// scope is the sources and exclusions of one link.
type scope struct{ sources, excluded []string }

func coversAny(p string, ss []scope) bool {
	for _, s := range ss {
		if covers(p, s.sources, s.excluded) {
			return true
		}
	}
	return false
}
