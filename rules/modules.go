package rules

import (
	"sort"

	"github.com/cernly/cernly/fact"
)

// ModuleGraph is the import graph of the repo's own packages: import facts
// as edges, the packages they name as nodes, with coupling per package.
// Absent when the scan has no import facts.
type ModuleGraph struct {
	Packages []Package     `json:"packages"`
	Edges    []ImportEdge  `json:"edges"`
	Cycles   []ImportCycle `json:"cycles"`
	// What each service reaches: from its Go entrypoints when its recipe
	// records resolved ones, else every package in its sources.
	Services []ServiceModules `json:"services"`
}

type Package struct {
	Package  string `json:"package"`
	Language string `json:"language"`
	Dir      string `json:"dir"`
	Module   string `json:"module,omitempty"`
	FanIn    int    `json:"fan_in"`  // packages of the repo that import it
	FanOut   int    `json:"fan_out"` // packages of the repo it imports
	// Depth is the longest chain of imports below it, in packages; a cycle
	// counts as one step.
	Depth int  `json:"depth"`
	Cycle bool `json:"cycle,omitempty"`
}

type ImportEdge struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Lines int      `json:"lines"` // import lines that prove it
	Facts []string `json:"facts"`
}

// ImportCycle is a set of packages that import each other, directly or
// through one another (a strongly connected component of more than one).
type ImportCycle struct {
	Packages []string `json:"packages"`
	From     []string `json:"from"`
}

type ServiceModules struct {
	Service  string   `json:"service"`
	Reach    string   `json:"reach"` // entrypoints or sources
	Roots    []string `json:"roots,omitempty"`
	Packages []string `json:"packages"`
	From     []string `json:"from"`
}

// Coupling is the coupling of a set of packages: a service's, or all.
type Coupling struct {
	Packages  int `json:"packages"`
	Imports   int `json:"imports"` // edges between them
	MaxFanIn  int `json:"max_fan_in"`
	MaxFanOut int `json:"max_fan_out"`
	Cycles    int `json:"cycles"`
	Depth     int `json:"depth"`
}

func modules(m *Map, facts []fact.Fact) *ModuleGraph {
	pk := map[string]*Package{}
	edges := map[[2]string]*ImportEdge{}
	for _, f := range facts {
		if f.Kind != fact.KindImport {
			continue
		}
		if f.Str("item") != "" {
			continue // a linked item or import is not an edge between two packages
		}
		from, to := f.Str("from"), f.Str("to")
		if from == "" || to == "" || from == to {
			continue
		}
		lang := f.Str("language")
		if pk[from] == nil {
			pk[from] = &Package{Package: from, Language: lang, Dir: f.Str("from_dir"), Module: f.Str("from_module")}
		}
		if pk[to] == nil {
			pk[to] = &Package{Package: to, Language: lang, Dir: f.Str("to_dir"), Module: f.Str("to_module")}
		}
		k := [2]string{from, to}
		e := edges[k]
		if e == nil {
			e = &ImportEdge{From: from, To: to}
			edges[k] = e
		}
		e.Lines += len(f.Evidence)
		e.Facts = append(e.Facts, f.Key)
	}
	if len(pk) == 0 {
		return nil
	}
	g := &ModuleGraph{Packages: []Package{}, Edges: []ImportEdge{}, Cycles: []ImportCycle{}, Services: []ServiceModules{}}
	out := map[string][]string{}
	for _, e := range edges {
		e.Facts = uniq(e.Facts)
		g.Edges = append(g.Edges, *e)
		out[e.From] = append(out[e.From], e.To)
		pk[e.From].FanOut++
		pk[e.To].FanIn++
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	names := make([]string, 0, len(pk))
	for n := range pk {
		names = append(names, n)
		sort.Strings(out[n])
	}
	sort.Strings(names)

	comp, comps := scc(names, out)
	for _, c := range comps {
		if len(c) < 2 {
			continue
		}
		var from []string
		in := map[string]bool{}
		for _, p := range c {
			in[p] = true
			pk[p].Cycle = true
		}
		for _, e := range g.Edges {
			if in[e.From] && in[e.To] {
				from = append(from, e.Facts...)
			}
		}
		g.Cycles = append(g.Cycles, ImportCycle{Packages: c, From: uniq(from)})
	}
	sort.Slice(g.Cycles, func(i, j int) bool { return g.Cycles[i].Packages[0] < g.Cycles[j].Packages[0] })

	// Depth on the graph of components, which has no cycles.
	depth := map[int]int{}
	var walk func(c int) int
	done := map[int]bool{}
	walk = func(c int) int {
		if done[c] {
			return depth[c]
		}
		done[c] = true
		d := 0
		for _, p := range comps[c] {
			for _, t := range out[p] {
				if tc := comp[t]; tc != c {
					d = max(d, walk(tc)+1)
				}
			}
		}
		depth[c] = d
		return d
	}
	for _, n := range names {
		pk[n].Depth = walk(comp[n])
		g.Packages = append(g.Packages, *pk[n])
	}
	g.Services = serviceModules(m, facts, names, pk, out)
	return g
}

// scc returns Tarjan's strongly connected components, each sorted, in a
// deterministic order, and the component index of every node.
func scc(names []string, out map[string][]string) (map[string]int, [][]string) {
	index, low := map[string]int{}, map[string]int{}
	on := map[string]bool{}
	var stack []string
	comp := map[string]int{}
	var comps [][]string
	i := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = i, i
		i++
		stack = append(stack, v)
		on[v] = true
		for _, w := range out[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp[w] = len(comps)
				c = append(c, w)
				if w == v {
					break
				}
			}
			sort.Strings(c)
			comps = append(comps, c)
		}
	}
	for _, n := range names {
		if _, seen := index[n]; !seen {
			visit(n)
		}
	}
	return comp, comps
}

// serviceModules gives each linked service and CI image the packages it
// reaches. A recipe whose Go builds resolve to repo folders roots the walk
// at those folders' packages; otherwise every package in the sources counts.
func serviceModules(m *Map, facts []fact.Fact, names []string, pk map[string]*Package, out map[string][]string) []ServiceModules {
	recipes := map[string]fact.Fact{}
	for _, f := range facts {
		if f.Kind == fact.KindImageRecipe {
			recipes[f.Key] = f
		}
	}
	type target struct {
		subject, recipe string
		sources         []string
		excluded        []string
	}
	var ts []target
	for _, l := range m.Links {
		ts = append(ts, target{l.Service, l.Recipe, l.Sources, l.Excluded})
	}
	for _, c := range m.CIImages {
		ts = append(ts, target{c.Image, c.Recipe, c.Sources, c.Excluded})
	}
	var res []ServiceModules
	seen := map[string]bool{}
	for _, t := range ts {
		if seen[t.subject] {
			continue
		}
		seen[t.subject] = true
		in := func(dir string) bool { return covers(dir, t.sources, t.excluded) }
		var roots []string
		eps, _ := recipes[t.recipe].Attrs["entrypoints"].([]any)
		for _, x := range eps {
			e, _ := x.(map[string]any)
			if anyStr(e["kind"]) != "go" || e["resolved"] != true {
				continue
			}
			for _, n := range names {
				if pk[n].Dir == anyStr(e["path"]) && in(pk[n].Dir) {
					roots = append(roots, n)
				}
			}
		}
		sm := ServiceModules{Service: t.subject, Reach: "sources", Packages: []string{}, From: []string{t.recipe}}
		if len(roots) > 0 {
			sm.Reach, sm.Roots = "entrypoints", uniq(roots)
			reached := map[string]bool{}
			var walk func(p string)
			walk = func(p string) {
				if reached[p] {
					return
				}
				reached[p] = true
				for _, q := range out[p] {
					walk(q)
				}
			}
			for _, r := range sm.Roots {
				walk(r)
			}
			for _, n := range names {
				if reached[n] {
					sm.Packages = append(sm.Packages, n)
				}
			}
		} else {
			for _, n := range names {
				if in(pk[n].Dir) {
					sm.Packages = append(sm.Packages, n)
				}
			}
		}
		if len(sm.Packages) > 0 {
			res = append(res, sm)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Service < res[j].Service })
	if res == nil {
		res = []ServiceModules{}
	}
	return res
}

// coupling sums a set of packages; nil set means all of them.
func (g *ModuleGraph) coupling(set []string) *Coupling {
	if g == nil {
		return nil
	}
	in := map[string]bool{}
	for _, p := range set {
		in[p] = true
	}
	all := set == nil
	c := &Coupling{}
	for _, p := range g.Packages {
		if !all && !in[p.Package] {
			continue
		}
		c.Packages++
		c.MaxFanIn, c.MaxFanOut, c.Depth = max(c.MaxFanIn, p.FanIn), max(c.MaxFanOut, p.FanOut), max(c.Depth, p.Depth)
	}
	for _, e := range g.Edges {
		if all || (in[e.From] && in[e.To]) {
			c.Imports++
		}
	}
	for _, cy := range g.Cycles {
		if all || in[cy.Packages[0]] {
			c.Cycles++
		}
	}
	if c.Packages == 0 {
		return nil
	}
	return c
}
