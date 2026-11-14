package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
)

// FindingDriverWithoutStore: a service ships a database driver, but the
// repo describes no data store of that engine. A driver never becomes a
// data store: a service can ship one it does not use. A person confirms
// which store it talks to, or dismisses it, through a driver confirmation.
const FindingDriverWithoutStore = "driver_without_store"

// drivers adds a finding per direct runtime dependency that the driver list
// (defaults.yml, drivers) names, when no data store fact of its engine is in
// the scan. The subject is the dependency's key; the detail names the
// services it is attached to, else its manifest folder.
func drivers(m *Map, facts []fact.Fact) {
	list := defaults.Get().Drivers
	// NuGet ids are case-insensitive: one lower-cased copy per call.
	nuget := map[string]string{}
	for name, engine := range list["nuget"] {
		nuget[strings.ToLower(name)] = engine
	}
	stores := map[string]bool{}
	for _, f := range facts {
		if f.Kind == fact.KindDatastore {
			stores[f.Str("engine")] = true
		}
	}
	on := map[string][]string{}
	for _, a := range m.Attached {
		if a.Kind == fact.KindDependency {
			on[a.Fact] = append(on[a.Fact], a.Service)
		}
	}
	for _, f := range facts {
		if f.Kind != fact.KindDependency || Transitive(f) || f.Attrs["internal"] != nil {
			continue
		}
		if sc := f.Str("scope"); sc != "" && sc != "prod" {
			continue
		}
		engine := list[f.Str("ecosystem")][f.Str("name")]
		if f.Str("ecosystem") == "nuget" {
			engine = nuget[strings.ToLower(f.Str("name"))]
		}
		if engine == "" || stores[engine] {
			continue
		}
		who := "folder " + unitOf(f)
		if svcs := uniq(on[f.Key]); len(svcs) > 0 {
			sort.Strings(svcs)
			who = strings.Join(svcs, ", ")
		}
		m.Findings = append(m.Findings, Finding{Rule: FindingDriverWithoutStore, Subject: f.Key,
			Detail: fmt.Sprintf("%s ships %s, a %s driver, but no %s data store is described", who, f.Str("name"), engine, engine),
			From:   []string{f.Key}})
	}
}

// unitOf is the manifest folder a dependency was declared in.
func unitOf(f fact.Fact) string {
	for _, e := range f.Evidence {
		if e.Unit != "" {
			return e.Unit
		}
	}
	if len(f.Evidence) > 0 {
		return f.Evidence[0].Path
	}
	return "."
}
