package deps

import (
	"sort"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/redact"
	ver "github.com/cernly/cernly/internal/version"
)

// Transitive dependencies come from lockfiles only: what the lockfile
// records, never a resolution of our own. A lockfile is a graph of locked
// packages; the manifests that use it start a walk from their direct
// dependencies. Every package the walk reaches beyond a manifest's own
// direct dependencies is a transitive dependency of that manifest's folder
// (roots), through the packages that require it (via).

type lockNode struct {
	id       string // the lockfile's own identity: npm location, or the normalised Python name
	name     string
	version  string
	license  string // as the lockfile declares it; empty when it does not
	scope    string // prod or dev when the lockfile says; empty: from the manifests that reach it
	line     int
	lines    []int          // evidence lines when there are several (NuGet frameworks that disagree); else line
	internal bool           // a folder of the repo (workspace member, editable project): walked, never reported
	lockType string         // NuGet: the entry's type (Direct, Transitive, Project); Direct when any framework says so
	deps     []string       // ids of the packages it requires, as the lockfile resolves them
	ext      map[string]any // ecosystem-specific facts about the package, reported as attrs.ext
}

// evLines is the node's evidence lines, at least line 1.
func (n *lockNode) evLines() []int {
	if len(n.lines) > 0 {
		return n.lines
	}
	if n.line < 1 {
		return []int{1}
	}
	return []int{n.line}
}

type lockGraph struct {
	path, dir string
	eco       string
	plugin    string
	nodes     map[string]*lockNode
}

// lockRoot is one manifest that uses the lockfile: its folder, the ids of its
// direct dependencies, and the scope of each.
type lockRoot struct {
	dir    string
	direct map[string]string // id -> scope
}

// transitive walks the graph from every root and returns one fact per locked
// package that some root reaches only through another package.
func (g *lockGraph) transitive(roots []lockRoot) []fact.Fact {
	type reach struct {
		roots, via map[string]bool
		rank       int // the highest scope rank among the roots' dependencies that reach it
	}
	reached := map[string]*reach{}
	for _, r := range roots {
		type step struct {
			id   string
			rank int
		}
		seen := map[string]int{} // best rank a package was queued at
		var queue []step
		for id, scope := range r.direct {
			queue = append(queue, step{id, scopeRank(scope)})
		}
		// Highest rank first, so most packages are walked once. A package is
		// queued again when a higher rank reaches it later, so its descendants
		// end up at the highest rank that reaches it at any depth.
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].rank != queue[j].rank {
				return queue[i].rank > queue[j].rank
			}
			return queue[i].id < queue[j].id
		})
		for _, s := range queue {
			seen[s.id] = s.rank
		}
		for len(queue) > 0 {
			s := queue[0]
			queue = queue[1:]
			n := g.nodes[s.id]
			if n == nil {
				continue
			}
			for _, d := range n.deps {
				child := g.nodes[d]
				if child == nil {
					continue
				}
				if _, isDirect := r.direct[d]; !isDirect && !child.internal {
					x := reached[d]
					if x == nil {
						x = &reach{roots: map[string]bool{}, via: map[string]bool{}, rank: -1}
						reached[d] = x
					}
					x.roots[r.dir] = true
					if !n.internal {
						x.via[n.name] = true
					}
					if s.rank > x.rank {
						x.rank = s.rank
					}
				}
				if prev, ok := seen[d]; !ok || s.rank > prev {
					seen[d] = s.rank
					queue = append(queue, step{d, s.rank})
				}
			}
		}
	}
	ids := make([]string, 0, len(reached))
	for id := range reached {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []fact.Fact
	for _, id := range ids {
		n, x := g.nodes[id], reached[id]
		scope := n.scope
		if scope == "" {
			scope = rankScope(x.rank)
		}
		version := redact.Userinfo(n.version) // a git or tarball URL loses its credentials
		attrs := map[string]any{
			"ecosystem": g.eco, "name": n.name, "version": version, "direct": false, "scope": scope,
			"lockfile": g.path, "roots": fact.SortedSet(x.roots), "via": fact.SortedSet(x.via),
		}
		if version != "" {
			attrs["version_info"] = ver.Parse(version).Attrs()
		}
		if n.license != "" {
			attrs["license"] = n.license
		}
		if n.ext != nil {
			attrs["ext"] = n.ext
		}
		var ev []fact.Evidence
		for _, line := range n.evLines() {
			ev = append(ev, fact.Evidence{Path: g.path, Line: line})
		}
		out = append(out, fact.Fact{
			Kind: fact.KindDependency, Key: "dependency:" + g.eco + ":" + g.dir + ":lock:" + id,
			Attrs: attrs, Plugin: g.plugin, Evidence: ev,
		})
	}
	return out
}

// Scope ranks of a reached package: prod beats build beats test beats dev. A
// scope the walk does not know (npm's optional, say) counts as prod, as a
// dependency that is not dev always did.
func scopeRank(scope string) int {
	switch scope {
	case "dev":
		return 0
	case "test":
		return 1
	case "build":
		return 2
	}
	return 3
}

func rankScope(rank int) string {
	switch rank {
	case 0:
		return "dev"
	case 1:
		return "test"
	case 2:
		return "build"
	}
	return "prod"
}
