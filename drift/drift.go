// Package drift compares two scans and their maps: which facts
// and which conclusions were added, removed or changed. Facts are compared by
// kind and key, so a moved line is never drift; map items by their identity
// (a party by id, a link by service and recipe, a finding by rule and
// subject, so a new detail is a change).
package drift

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// SchemaID is the id every drift document is written with.
const SchemaID = "cernly.drift/v1"

// legacySchemaID is the id drift documents had before the rename; Read still
// accepts it.
const legacySchemaID = "architecture-observability.drift/v1"

type Ref struct {
	Commit      string `json:"commit"`
	CommittedAt string `json:"committed_at"`
}

type Change struct {
	Change  string          `json:"change"`  // added, removed, changed
	Section string          `json:"section"` // fact kind, or map section (parties, links, stacks, ...)
	Key     string          `json:"key"`
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after,omitempty"`
}

type Drift struct {
	Schema string   `json:"schema"`
	Repo   string   `json:"repo"`
	From   Ref      `json:"from"`
	To     Ref      `json:"to"`
	Facts  []Change `json:"facts"`
	Map    []Change `json:"map"`
}

// Compare returns what changed from a to b. Maps may be nil (facts only).
func Compare(a, b *fact.Scan, am, bm *rules.Map) *Drift {
	d := &Drift{Schema: SchemaID, Repo: b.Repo,
		From: Ref{a.Commit.SHA, a.Commit.CommittedAt}, To: Ref{b.Commit.SHA, b.Commit.CommittedAt},
		Facts: []Change{}, Map: []Change{}}
	d.Facts = diff(factItems(a), factItems(b))
	if am != nil && bm != nil {
		d.Map = diff(mapItems(am), mapItems(bm))
	}
	return d
}

// item is one comparable thing: its section, its identity, its value.
type item struct{ section, key string }

func factItems(s *fact.Scan) map[item]json.RawMessage {
	out := map[item]json.RawMessage{}
	for _, f := range s.Facts {
		section := f.Kind
		if rules.Transitive(f) {
			section = SectionTransitive // its own group, so it never floods the direct ones
		}
		out[item{section, f.Key}] = canonical(f.Attrs) // evidence (lines) is not compared
	}
	return out
}

func mapItems(m *rules.Map) map[item]json.RawMessage {
	out := map[item]json.RawMessage{}
	add := func(section, key string, v any) { out[item{section, key}] = canonical(v) }
	for _, s := range m.Stacks {
		add("stacks", s.Stack, map[string]any{"role": s.Role, "manual": s.Manual})
	}
	for _, l := range m.Links {
		add("links", l.Service+" -> "+l.Recipe, map[string]any{"sources": l.Sources, "excluded": l.Excluded, "narrowing": l.Narrowing, "via": l.Via})
	}
	for _, i := range m.Images {
		add("images", i.Service, i)
	}
	for _, c := range m.CIImages {
		var pushed []string
		for _, p := range c.Pushes {
			pushed = append(pushed, p.Target)
		}
		add("ci_images", c.Image, map[string]any{"sources": c.Sources, "excluded": c.Excluded, "narrowing": c.Narrowing, "pushes": pushed, "test_only": c.TestOnly, "deployment": c.Deployment})
	}
	for _, a := range m.Attached {
		add("attached", a.Service+" <- "+a.Fact, map[string]any{"scope": a.Scope, "internal": a.Internal})
	}
	for _, p := range m.Parties {
		add("parties", p.ID, map[string]any{"grade": p.Grade, "services": p.Services})
	}
	for _, i := range m.Internal {
		add("internal", i.ID+" "+i.Service, map[string]any{"datastore": i.Datastore})
	}
	for _, u := range m.Unknown {
		add("unknown", u.Host, map[string]any{"usage": u.Usage})
	}
	for _, o := range m.Own {
		by := ""
		if o.Declared != nil {
			by = o.Declared.By
		}
		add("own", o.Host, map[string]any{"domain": o.Domain, "declared": by, "served_by": o.ServedBy})
	}
	for _, r := range m.Routes {
		add("routes", r.Route+" @ "+r.Stack, map[string]any{"via": r.Via, "sites": r.Sites, "match": r.Match,
			"action": r.Action, "upstream": r.Upstream, "to": r.To})
	}
	for _, t := range m.AITools {
		add("ai_tools", t.Tool, map[string]any{"name": t.Name, "files": t.Files})
	}
	// A subject with one finding per rule is keyed without its detail; one
	// with several (base images of a recipe) keeps the detail in the key.
	n := map[string]int{}
	for _, f := range m.Findings {
		n[f.Rule+" "+f.Subject]++
	}
	for _, f := range m.Findings {
		key := f.Rule + " " + f.Subject
		if n[key] > 1 {
			key += " " + f.Detail
		}
		add("findings", key, map[string]any{"grade": f.Grade, "detail": f.Detail})
	}
	return out
}

func diff(a, b map[item]json.RawMessage) []Change {
	out := []Change{}
	for k, av := range a {
		bv, ok := b[k]
		switch {
		case !ok:
			out = append(out, Change{Change: "removed", Section: k.section, Key: k.key, Before: av})
		case string(av) != string(bv):
			out = append(out, Change{Change: "changed", Section: k.section, Key: k.key, Before: av, After: bv})
		}
	}
	for k, bv := range b {
		if _, ok := a[k]; !ok {
			out = append(out, Change{Change: "added", Section: k.section, Key: k.key, After: bv})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func canonical(v any) json.RawMessage {
	b, err := fact.Canonical(v)
	if err != nil {
		panic("drift: " + err.Error()) // attrs come from JSON; cannot fail
	}
	return b
}

// DriftGroup is one kind of fact in a drift, counted, with a few examples.
type DriftGroup struct {
	Section  string   `json:"section"`
	Label    string   `json:"label"`
	One      string   `json:"one"` // the label for a count of one ("1 data store")
	Added    int      `json:"added"`
	Removed  int      `json:"removed"`
	Changed  int      `json:"changed"`
	Examples []string `json:"examples"`
}

// SectionTransitive groups transitive dependencies apart from
// the direct ones.
const SectionTransitive = "dependency_transitive"

var driftOrder = []struct{ section, label, one string }{
	{fact.KindService, "services", "service"}, {fact.KindDatastore, "data stores", "data store"}, {fact.KindStack, "stacks", "stack"},
	{fact.KindRoute, "routes", "route"}, {fact.KindImageBuild, "image builds", "image build"}, {fact.KindImageRecipe, "image recipes", "image recipe"},
	{fact.KindOutboundHost, "outside hosts", "outside host"}, {fact.KindDependency, "dependencies", "dependency"},
	{SectionTransitive, "transitive dependencies", "transitive dependency"}, {fact.KindEnvVar, "env names", "env name"},
	{fact.KindDBTable, "tables", "table"}, {fact.KindDBColumn, "columns", "column"}, {fact.KindAgentConfig, "AI tool configs", "AI tool config"},
	{fact.KindOwner, "owners", "owner"}, {fact.KindCatalogEntity, "catalog entities", "catalog entity"},
}

var mapOrder = []struct{ section, label, one string }{
	{"stacks", "stack roles", "stack role"}, {"links", "code links", "code link"}, {"images", "images", "image"},
	{"ci_images", "images built in CI", "image built in CI"}, {"attached", "attached facts", "attached fact"},
	{"parties", "outside parties", "outside party"}, {"internal", "served inside", "served inside"},
	{"unknown", "unknown hosts", "unknown host"}, {"own", "own hosts", "own host"},
	{"routes", "routes", "route"}, {"ai_tools", "AI tools", "AI tool"}, {"findings", "findings", "finding"},
}

// Groups counts a drift's fact changes per kind, in a fixed order, with up to
// four examples each.
func Groups(d *Drift) []DriftGroup { return group(d.Facts, driftOrder) }

// MapGroups does the same for the map's changes, per map section.
func MapGroups(d *Drift) []DriftGroup { return group(d.Map, mapOrder) }

func group(changes []Change, order []struct{ section, label, one string }) []DriftGroup {
	by := map[string]*DriftGroup{}
	for _, c := range changes {
		g := by[c.Section]
		if g == nil {
			g = &DriftGroup{Section: c.Section, Examples: []string{}}
			by[c.Section] = g
		}
		switch c.Change {
		case "added":
			g.Added++
		case "removed":
			g.Removed++
		default:
			g.Changed++
		}
		if len(g.Examples) < 4 {
			g.Examples = append(g.Examples, map[string]string{"added": "+", "removed": "−", "changed": "~"}[c.Change]+" "+exampleName(c))
		}
	}
	out := []DriftGroup{}
	for _, o := range order {
		if g := by[o.section]; g != nil {
			g.Label, g.One = o.label, o.one
			out = append(out, *g)
		}
	}
	return out
}

func exampleName(c Change) string {
	var before, after map[string]any
	_ = json.Unmarshal(c.Before, &before)
	_ = json.Unmarshal(c.After, &after)
	a := after
	if a == nil {
		a = before
	}
	key := c.Key
	if mapSection[c.Section] { // a map item's key is its name
		if c.Change == "changed" {
			return key + ": " + c.Diff()
		}
		return key
	}
	switch c.Section {
	case fact.KindService, fact.KindDatastore:
		return strings.SplitN(key, ":", 3)[2]
	case fact.KindImageBuild, fact.KindImageRecipe, fact.KindStack:
		return strings.Replace(strings.SplitN(key, ":", 2)[1], "compose:", "", 1)
	case fact.KindRoute:
		if p := strings.SplitN(key, ":", 4); len(p) == 4 {
			return p[3]
		}
	case fact.KindDependency, SectionTransitive:
		n, _ := a["name"].(string)
		if c.Change == "changed" {
			bv, _ := before["version"].(string)
			av, _ := after["version"].(string)
			return n + " " + bv + " → " + av
		}
		return n
	}
	for _, k := range []string{"name", "host"} {
		if v, ok := a[k].(string); ok && v != "" {
			return v
		}
	}
	return key[strings.LastIndex(key, ":")+1:]
}

var mapSection = func() map[string]bool {
	out := map[string]bool{}
	for _, o := range mapOrder {
		out[o.section] = true
	}
	return out
}()

// Diff names what a changed item changed: each top-level field whose value
// differs, as field: old → new. Empty for an added or removed item.
func (c Change) Diff() string {
	if c.Change != "changed" {
		return ""
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(c.Before, &before)
	_ = json.Unmarshal(c.After, &after)
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		if string(before[k]) != string(after[k]) {
			parts = append(parts, k+": "+show(before[k])+" → "+show(after[k]))
		}
	}
	return strings.Join(parts, "; ")
}

func show(v json.RawMessage) string {
	var s string
	switch {
	case len(v) == 0 || string(v) == "null":
		return "none"
	case json.Unmarshal(v, &s) == nil:
		if s == "" {
			return "none"
		}
		return s
	}
	return string(v)
}

// Read parses a drift document written with SchemaID or the id before the
// rename. The result carries SchemaID, so writing it again uses the new id.
func Read(raw []byte) (*Drift, error) {
	var d Drift
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.Schema != SchemaID && d.Schema != legacySchemaID {
		return nil, fmt.Errorf("not a drift document: schema %q, want %s", d.Schema, SchemaID)
	}
	d.Schema = SchemaID
	return &d, nil
}
