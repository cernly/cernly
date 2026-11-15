// Package depview groups one scan's dependencies by service: the direct ones
// the attach rule puts on a service, and the transitive ones its manifest
// folders reach through the lockfiles. What no service carries is grouped
// last, as "not on a service".
package depview

import (
	"sort"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// Dep is one dependency as a view lists it.
type Dep struct {
	Key        string   `json:"key"`
	Ecosystem  string   `json:"ecosystem"`
	Name       string   `json:"name"`
	Version    string   `json:"version,omitempty"`
	Scope      string   `json:"scope,omitempty"`   // prod, dev, unknown
	Licence    string   `json:"licence,omitempty"` // as declared; empty: not declared
	Direct     bool     `json:"direct"`
	Via        []string `json:"via,omitempty"`
	Path       string   `json:"path"`
	Line       int      `json:"line"`
	Change     string   `json:"change,omitempty"` // new, upgraded, downgraded, changed; empty: unchanged or no comparison
	WasVersion string   `json:"was_version,omitempty"`
}

// Service is the dependencies of one service.
type Service struct {
	Service    string `json:"service"` // key; "" for what no service carries
	Name       string `json:"name"`
	Direct     []Dep  `json:"direct"`
	Transitive []Dep  `json:"transitive"`
}

// Summary is one scan's dependencies by service and their counts.
type Summary struct {
	Services []Service      `json:"services"`
	Counts   map[string]int `json:"counts"`
}

// Rest is the name of the group for dependencies on no service.
const Rest = "not on a service"

// FromFact is a dependency fact as a Dep, unmarked.
func FromFact(f fact.Fact) Dep {
	d := Dep{Key: f.Key, Ecosystem: Str(f, "ecosystem"), Name: Str(f, "name"), Version: Str(f, "version"), Scope: Str(f, "scope"),
		Licence: Str(f, "license"), Direct: !rules.Transitive(f), Via: strs(f, "via")}
	if len(f.Evidence) > 0 {
		d.Path, d.Line = f.Evidence[0].Path, f.Evidence[0].Line
	}
	return d
}

// Str is a string attribute of a fact, or "".
func Str(f fact.Fact, k string) string {
	v, _ := f.Attrs[k].(string)
	return v
}

func strs(f fact.Fact, k string) []string {
	var out []string
	if xs, ok := f.Attrs[k].([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if xs, ok := f.Attrs[k].([]string); ok {
		out = append(out, xs...)
	}
	return out
}

// Group lists scan to's dependencies per service, services by key and the
// rest last, each list by key. mark, when not nil, is called on every Dep
// before it is listed (a comparison sets Change there). Dependencies marked
// internal are left out.
func Group(to *fact.Scan, m *rules.Map, mark func(*Dep)) []Service {
	if mark == nil {
		mark = func(*Dep) {}
	}
	facts := map[string]fact.Fact{}
	for _, f := range to.Facts {
		facts[f.Key] = f
	}
	byRoot := map[string][]fact.Fact{}
	for _, f := range to.Facts {
		if rules.Transitive(f) {
			for _, r := range strs(f, "roots") {
				byRoot[r] = append(byRoot[r], f)
			}
		}
	}
	onService := map[string]bool{}
	bySvc := map[string]*Service{}
	svc := func(key string) *Service {
		if bySvc[key] == nil {
			name := key
			if f, ok := facts[key]; ok && Str(f, "name") != "" {
				name = Str(f, "name")
				if st := Str(f, "stack"); st != "" {
					name = st + "/" + name
				}
			}
			bySvc[key] = &Service{Service: key, Name: name, Direct: []Dep{}, Transitive: []Dep{}}
		}
		return bySvc[key]
	}
	folders := map[string]map[string]bool{}
	for _, a := range m.Attached {
		if a.Kind != fact.KindDependency {
			continue
		}
		f, ok := facts[a.Fact]
		if !ok || f.Attrs["internal"] != nil {
			continue
		}
		onService[f.Key] = true
		sd := svc(a.Service)
		d := FromFact(f)
		mark(&d)
		sd.Direct = append(sd.Direct, d)
		for _, e := range f.Evidence {
			if e.Unit != "" {
				if folders[a.Service] == nil {
					folders[a.Service] = map[string]bool{}
				}
				folders[a.Service][e.Unit] = true
			}
		}
	}
	for s, dirs := range folders {
		seen := map[string]bool{}
		for dir := range dirs {
			for _, t := range byRoot[dir] {
				if seen[t.Key] {
					continue
				}
				seen[t.Key] = true
				onService[t.Key] = true
				d := FromFact(t)
				mark(&d)
				bySvc[s].Transitive = append(bySvc[s].Transitive, d)
			}
		}
	}
	rest := &Service{Name: Rest, Direct: []Dep{}, Transitive: []Dep{}}
	for _, f := range to.Facts {
		if f.Kind != fact.KindDependency || onService[f.Key] || f.Attrs["internal"] != nil {
			continue
		}
		d := FromFact(f)
		mark(&d)
		if d.Direct {
			rest.Direct = append(rest.Direct, d)
		} else {
			rest.Transitive = append(rest.Transitive, d)
		}
	}
	all := []*Service{rest}
	for _, v := range bySvc {
		all = append(all, v)
	}
	for _, sd := range all {
		sort.Slice(sd.Direct, func(i, j int) bool { return sd.Direct[i].Key < sd.Direct[j].Key })
		sort.Slice(sd.Transitive, func(i, j int) bool { return sd.Transitive[i].Key < sd.Transitive[j].Key })
	}
	keys := make([]string, 0, len(bySvc))
	for k := range bySvc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []Service{}
	for _, k := range keys {
		out = append(out, *bySvc[k])
	}
	if len(rest.Direct)+len(rest.Transitive) > 0 {
		out = append(out, *rest)
	}
	return out
}

// Count counts the distinct dependencies of services: direct, transitive,
// licence_declared, and by change new, upgraded and changed (downgraded or
// another version change).
func Count(services []Service) map[string]int {
	out := map[string]int{}
	counted := map[string]bool{}
	for _, sd := range services {
		for _, d := range append(append([]Dep(nil), sd.Direct...), sd.Transitive...) {
			if counted[d.Key] {
				continue
			}
			counted[d.Key] = true
			if d.Direct {
				out["direct"]++
			} else {
				out["transitive"]++
			}
			if d.Licence != "" {
				out["licence_declared"]++
			}
			switch d.Change {
			case "new":
				out["new"]++
			case "upgraded":
				out["upgraded"]++
			case "downgraded", "changed":
				out["changed"]++
			}
		}
	}
	return out
}

// Of is one scan's dependencies by service, with no comparison.
func Of(to *fact.Scan, m *rules.Map) Summary {
	s := Group(to, m, nil)
	return Summary{Services: s, Counts: Count(s)}
}
