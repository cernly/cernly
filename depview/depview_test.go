package depview

import (
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

func dep(key, name string, direct bool, unit string, attrs map[string]any) fact.Fact {
	a := map[string]any{"ecosystem": "npm", "name": name, "version": "1.0.0", "direct": direct}
	for k, v := range attrs {
		a[k] = v
	}
	return fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: a, Evidence: []fact.Evidence{{Path: unit + "/package.json", Line: 3, Unit: unit}}}
}

func scan() (*fact.Scan, *rules.Map) {
	s := &fact.Scan{Facts: []fact.Fact{
		{Kind: "service", Key: "service:compose:shop:web", Attrs: map[string]any{"name": "web", "stack": "shop"}},
		dep("dependency:npm:web:react", "react", true, "web", nil),
		dep("dependency:npm:web:a-lib", "a-lib", true, "web", nil),
		dep("dependency:npm:web:scheduler", "scheduler", false, "web", map[string]any{"roots": []any{"web"}, "license": "MIT"}),
		dep("dependency:npm:tools:lint", "lint", true, "tools", nil),
		dep("dependency:npm:tools:orphan", "orphan", false, "tools", map[string]any{"roots": []any{"nowhere"}}),
		dep("dependency:npm:web:own", "own", true, "web", map[string]any{"internal": "packages/own"}),
	}}
	m := &rules.Map{Attached: []rules.Attached{
		{Service: "service:compose:shop:web", Fact: "dependency:npm:web:react", Kind: fact.KindDependency},
		{Service: "service:compose:shop:web", Fact: "dependency:npm:web:a-lib", Kind: fact.KindDependency},
		{Service: "service:compose:shop:web", Fact: "dependency:npm:web:own", Kind: fact.KindDependency},
	}}
	return s, m
}

func keys(ds []Dep) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Key)
	}
	return out
}

func TestGroup(t *testing.T) {
	s, m := scan()
	g := Group(s, m, nil)
	if len(g) != 2 || g[0].Service != "service:compose:shop:web" || g[0].Name != "shop/web" {
		t.Fatalf("groups: %+v", g)
	}
	if got := keys(g[0].Direct); len(got) != 2 || got[0] != "dependency:npm:web:a-lib" || got[1] != "dependency:npm:web:react" {
		t.Errorf("direct: %v", got)
	}
	if got := keys(g[0].Transitive); len(got) != 1 || got[0] != "dependency:npm:web:scheduler" || g[0].Transitive[0].Licence != "MIT" {
		t.Errorf("transitive: %+v", g[0].Transitive)
	}
}

func TestGroupRest(t *testing.T) {
	s, m := scan()
	g := Group(s, m, nil)
	r := g[len(g)-1]
	if r.Name != Rest || r.Service != "" {
		t.Fatalf("last group: %+v", r)
	}
	if d, tr := keys(r.Direct), keys(r.Transitive); len(d) != 1 || d[0] != "dependency:npm:tools:lint" || len(tr) != 1 || tr[0] != "dependency:npm:tools:orphan" {
		t.Errorf("rest: %v %v", d, tr)
	}
}

func TestGroupSkipsInternal(t *testing.T) {
	s, m := scan()
	for _, g := range Group(s, m, nil) {
		for _, d := range append(g.Direct, g.Transitive...) {
			if d.Name == "own" {
				t.Errorf("internal package listed under %q", g.Name)
			}
		}
	}
}

func TestGroupMark(t *testing.T) {
	s, m := scan()
	g := Group(s, m, func(d *Dep) { d.Change = "new" })
	if c := Count(g); c["new"] != 5 {
		t.Errorf("new: %v", c)
	}
}

func TestCount(t *testing.T) {
	s, m := scan()
	m.Attached = append(m.Attached, rules.Attached{Service: "service:compose:shop:api", Fact: "dependency:npm:web:react", Kind: fact.KindDependency})
	c := Of(s, m).Counts
	if c["direct"] != 3 || c["transitive"] != 2 || c["licence_declared"] != 1 {
		t.Errorf("counts: %v", c)
	}
}
