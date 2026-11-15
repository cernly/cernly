package rules

import (
	"slices"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
)

// Metrics are the surface metrics: what can go wrong and where, counted
// from the map itself, for the whole repo, for its production stacks and
// per service. Size (files, lines, languages) is context next to them,
// never the headline. Coupling comes from the module graph.
type Metrics struct {
	Total      Surface          `json:"total"`
	Production Surface          `json:"production"` // stacks whose role is production or production (unconfirmed)
	Services   []ServiceMetrics `json:"services"`
}

type Surface struct {
	OutsideParties   int       `json:"outside_parties"`
	DataStores       int       `json:"data_stores"`
	ColumnsToCheck   int       `json:"columns_to_check"`
	UnpinnedImages   int       `json:"unpinned_images"`
	ProductionStacks int       `json:"production_stacks"`
	Routes           int       `json:"routes"`
	Size             Size      `json:"size"`
	Coupling         *Coupling `json:"coupling,omitempty"`
	Items            Items     `json:"items"`
}

// Items are what each count counts (rules 0.10.0), so a reader can trace a
// number to its facts: party ids (each party lists its signal facts), data
// store keys, columns as table.column (the table lists its facts), the
// image_pinning findings by subject and image, production stack keys and
// route keys. Each list is as long as its count.
type Items struct {
	ServiceItems
	ProductionStacks []string `json:"production_stacks"`
}

// ServiceItems are a service's items: production stacks are not counted
// per service.
type ServiceItems struct {
	OutsideParties []string `json:"outside_parties"`
	DataStores     []string `json:"data_stores"`
	ColumnsToCheck []string `json:"columns_to_check"`
	UnpinnedImages []Pin    `json:"unpinned_images"`
	Routes         []string `json:"routes"`
	Endpoints      []string `json:"endpoints,omitempty"` // endpoint keys; per service only, absent when it has none
}

// EndpointCounts are a service's endpoints by auth state.
type EndpointCounts struct {
	Total     int `json:"total"`
	Required  int `json:"required"`
	Anonymous int `json:"anonymous"`
	Unknown   int `json:"unknown"`
}

// Add counts one endpoint by its auth state; any state but required and
// anonymous counts as unknown.
func (c *EndpointCounts) Add(state string) {
	c.Total++
	switch state {
	case "required":
		c.Required++
	case "anonymous":
		c.Anonymous++
	default:
		c.Unknown++
	}
}

// Pin is one image_pinning finding: its subject and its detail.
type Pin struct {
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

// ServiceMetrics is one service's, or one image built in CI's, share.
// DataStores are the data stores it depends on in its stack.
type ServiceMetrics struct {
	Service        string          `json:"service"`
	Role           string          `json:"role"` // its stack's role; none for an image built in CI
	OutsideParties int             `json:"outside_parties"`
	DataStores     int             `json:"data_stores"`
	ColumnsToCheck int             `json:"columns_to_check"`
	UnpinnedImages int             `json:"unpinned_images"`
	Routes         int             `json:"routes"`
	Endpoints      *EndpointCounts `json:"endpoints,omitempty"`
	Size           Size            `json:"size"`
	Coupling       *Coupling       `json:"coupling,omitempty"`
	Items          ServiceItems    `json:"items"`
}

// Size counts every source file; the test files and lines among them (by
// each language's convention) are counted again on their own.
type Size struct {
	Files     int            `json:"files"`
	Lines     int            `json:"lines"`
	TestFiles int            `json:"test_files"`
	TestLines int            `json:"test_lines"`
	Languages []LanguageSize `json:"languages"`
}

type LanguageSize struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
	Lines    int    `json:"lines"`
}

// sizer sums source files by path, so a file counts once however many
// sources hold it.
type sizer map[string]sized

type sized struct {
	lang  string
	lines int
	test  bool
}

func newSizer(facts []fact.Fact) sizer {
	s := sizer{}
	for _, f := range facts {
		if f.Kind != fact.KindSourceSize {
			continue
		}
		for _, e := range f.Evidence {
			s[e.Path] = sized{f.Str("language"), max(e.EndLine, e.Line), f.Attrs["test"] == true}
		}
	}
	return s
}

// size sums the files that keep says yes to; nil keep counts every file.
func (s sizer) size(keep func(string) bool) Size {
	by := map[string]*LanguageSize{}
	out := Size{Languages: []LanguageSize{}}
	for p, f := range s {
		if keep != nil && !keep(p) {
			continue
		}
		l := by[f.lang]
		if l == nil {
			l = &LanguageSize{Language: f.lang}
			by[f.lang] = l
		}
		l.Files++
		l.Lines += f.lines
		out.Files++
		out.Lines += f.lines
		if f.test {
			out.TestFiles++
			out.TestLines += f.lines
		}
	}
	for _, l := range by {
		out.Languages = append(out.Languages, *l)
	}
	sort.Slice(out.Languages, func(i, j int) bool {
		a, b := out.Languages[i], out.Languages[j]
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return a.Language < b.Language
	})
	return out
}

func metrics(m *Map, facts []fact.Fact) *Metrics {
	byKey := map[string]fact.Fact{}
	for _, f := range facts {
		byKey[f.Key] = f
	}
	role := map[string]string{} // stack key -> role
	for _, st := range m.Stacks {
		role[st.Stack] = st.Role
	}
	prod := func(stack string) bool { r := role[stack]; return r == RoleProduction || r == RoleUnconfirmed }

	// Subjects and the sources of their code.
	type subject struct {
		key, stack, recipe string
		stackKey           string
		sources            []string
		scopes             []scope
	}
	var subs []subject
	sources := map[string][]string{}
	scopes := map[string][]scope{}
	recipeOf := map[string]string{}
	for _, l := range m.Links {
		sources[l.Service] = append(sources[l.Service], l.Sources...)
		scopes[l.Service] = append(scopes[l.Service], scope{l.Sources, l.Excluded})
		recipeOf[l.Service] = l.Recipe
	}
	for _, c := range m.CIImages {
		sources[c.Image] = append(sources[c.Image], c.Sources...)
		scopes[c.Image] = append(scopes[c.Image], scope{c.Sources, c.Excluded})
		recipeOf[c.Image] = c.Recipe
	}
	for _, f := range facts {
		if f.Kind == fact.KindService {
			subs = append(subs, subject{f.Key, f.Str("stack"), recipeOf[f.Key], stackKeyOf(f), uniq(sources[f.Key]), scopes[f.Key]})
		}
	}
	for _, c := range m.CIImages {
		subs = append(subs, subject{c.Image, "", c.Recipe, "", uniq(sources[c.Image]), scopes[c.Image]})
	}
	inSources := func(ss []scope) func(string) bool {
		return func(p string) bool { return coversAny(p, ss) }
	}
	evIn := func(key string, keep func(string) bool) bool {
		for _, e := range byKey[key].Evidence {
			if keep(e.Path) {
				return true
			}
		}
		return false
	}
	// Columns to check per table, keyed by every db_table fact behind it.
	var checks []struct {
		from []string
		cols []string
	}
	for _, t := range m.Tables {
		var cols []string
		for _, c := range t.Check {
			cols = append(cols, t.Table+"."+c.Column)
		}
		if len(cols) > 0 {
			checks = append(checks, struct {
				from []string
				cols []string
			}{t.From, cols})
		}
	}
	colsIn := func(keep func(string) bool) []string {
		var out []string
		for _, c := range checks {
			for _, k := range c.from {
				if evIn(k, keep) {
					out = append(out, c.cols...)
					break
				}
			}
		}
		return uniq(out)
	}
	var pinning []Finding
	for _, f := range m.Findings {
		if f.Rule == FindingImagePinning {
			pinning = append(pinning, f)
		}
	}
	pinsOf := func(s subject) []string {
		var out []string
		for _, f := range pinning {
			if f.Subject == s.key || (s.recipe != "" && f.Subject == s.recipe) {
				out = append(out, f.Subject+"|"+f.Detail)
			}
		}
		return out
	}
	storesOf := func(s subject) []string {
		var out []string
		for _, d := range byKey[s.key].Strs("depends_on") {
			k := "datastore:compose:" + s.stack + ":" + d
			if _, ok := byKey[k]; ok {
				out = append(out, k)
			}
		}
		return uniq(out)
	}
	partiesOf := func(key string) []string {
		var out []string
		for _, p := range m.Parties {
			for _, sv := range p.Services {
				if sv == key {
					out = append(out, p.ID)
				}
			}
		}
		return out
	}
	routesTo := func(key string) []string {
		var out []string
		for _, r := range m.Routes {
			if r.To == key {
				out = append(out, r.Route)
			}
		}
		return out
	}

	sz := newSizer(facts)
	g := m.Modules
	reach := map[string][]string{}
	if g != nil {
		for _, sm := range g.Services {
			reach[sm.Service] = sm.Packages
		}
	}
	out := &Metrics{Services: []ServiceMetrics{}}
	var prodSrc, prodPins, prodParties, prodPkgs []string
	var prodScopes []scope
	for _, s := range subs {
		r := RoleNone
		if s.stackKey != "" {
			r = role[s.stackKey]
			if r == "" {
				r = RoleNone
			}
		}
		keep := inSources(s.scopes)
		it := ServiceItems{OutsideParties: uniq(partiesOf(s.key)), DataStores: storesOf(s), UnpinnedImages: pinPairs(pinsOf(s)), Routes: routesTo(s.key)}
		sm := ServiceMetrics{Service: s.key, Role: r, Size: Size{Languages: []LanguageSize{}}}
		if len(s.sources) > 0 {
			it.ColumnsToCheck = colsIn(keep)
			sm.Size = sz.size(keep)
		}
		var ec EndpointCounts
		for _, row := range m.Endpoints {
			if row.Service != s.key {
				continue
			}
			it.Endpoints = append(it.Endpoints, row.Endpoint)
			ec.Add(row.Auth.State)
		}
		if ec.Total > 0 {
			sm.Endpoints = &ec
		}
		sm.Items = it.norm()
		sm.OutsideParties, sm.DataStores, sm.UnpinnedImages = len(sm.Items.OutsideParties), len(sm.Items.DataStores), len(sm.Items.UnpinnedImages)
		sm.Routes, sm.ColumnsToCheck = len(sm.Items.Routes), len(sm.Items.ColumnsToCheck)
		if pk, ok := reach[s.key]; ok {
			sm.Coupling = g.coupling(pk)
		}
		out.Services = append(out.Services, sm)
		if s.stackKey != "" && prod(s.stackKey) {
			prodSrc = append(prodSrc, s.sources...)
			prodScopes = append(prodScopes, s.scopes...)
			prodPins = append(prodPins, pinsOf(s)...)
			prodParties = append(prodParties, partiesOf(s.key)...)
			prodPkgs = append(prodPkgs, reach[s.key]...)
		}
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Service < out.Services[j].Service })

	// The whole repo.
	var all, prodIt Items
	for _, f := range facts {
		if f.Kind == fact.KindDatastore {
			all.DataStores = append(all.DataStores, f.Key)
			if prod(stackKeyOf(f)) {
				prodIt.DataStores = append(prodIt.DataStores, f.Key)
				prodPins = append(prodPins, pinsOf(subject{key: f.Key})...)
			}
		}
	}
	for _, st := range m.Stacks {
		if st.Role == RoleProduction || st.Role == RoleUnconfirmed {
			all.ProductionStacks = append(all.ProductionStacks, st.Stack)
		}
	}
	prodIt.ProductionStacks = all.ProductionStacks
	for _, c := range checks {
		all.ColumnsToCheck = append(all.ColumnsToCheck, c.cols...)
	}
	for _, r := range m.Routes {
		all.Routes = append(all.Routes, r.Route)
		if r.Stack != "" && prod(r.Stack) {
			prodIt.Routes = append(prodIt.Routes, r.Route)
		}
	}
	for _, p := range m.Parties {
		all.OutsideParties = append(all.OutsideParties, p.ID)
	}
	for _, f := range pinning {
		all.UnpinnedImages = append(all.UnpinnedImages, Pin{f.Subject, f.Detail})
	}
	all = all.norm()
	out.Total = Surface{OutsideParties: len(all.OutsideParties), DataStores: len(all.DataStores), ColumnsToCheck: len(all.ColumnsToCheck),
		UnpinnedImages: len(all.UnpinnedImages), ProductionStacks: len(all.ProductionStacks), Routes: len(all.Routes),
		Size: sz.size(nil), Coupling: g.coupling(nil), Items: all}
	prodSrc = uniq(prodSrc)
	prodIt.OutsideParties = uniq(prodParties)
	prodIt.UnpinnedImages = pinPairs(uniq(prodPins))
	out.Production = Surface{Size: Size{Languages: []LanguageSize{}}}
	if len(prodSrc) > 0 {
		prodIt.ColumnsToCheck = colsIn(inSources(prodScopes))
		out.Production.Size = sz.size(inSources(prodScopes))
	}
	prodIt = prodIt.norm()
	out.Production.Items = prodIt
	out.Production.OutsideParties, out.Production.DataStores, out.Production.ColumnsToCheck = len(prodIt.OutsideParties), len(prodIt.DataStores), len(prodIt.ColumnsToCheck)
	out.Production.UnpinnedImages, out.Production.ProductionStacks, out.Production.Routes = len(prodIt.UnpinnedImages), len(prodIt.ProductionStacks), len(prodIt.Routes)
	if len(prodPkgs) > 0 {
		out.Production.Coupling = g.coupling(uniq(prodPkgs))
	}
	return out
}

// pinPairs reads pinsOf's subject|detail pairs back.
func pinPairs(pairs []string) []Pin {
	var out []Pin
	for _, p := range pairs {
		i := strings.Index(p, "|")
		out = append(out, Pin{p[:i], p[i+1:]})
	}
	return out
}

// norm gives every list a value (an empty list, not null) in a fixed order.
func (it ServiceItems) norm() ServiceItems {
	it.OutsideParties, it.DataStores, it.ColumnsToCheck, it.Routes = sorted(it.OutsideParties), sorted(it.DataStores), sorted(it.ColumnsToCheck), sorted(it.Routes)
	if len(it.Endpoints) > 0 {
		it.Endpoints = sorted(it.Endpoints)
	}
	it.UnpinnedImages = append([]Pin{}, it.UnpinnedImages...)
	sort.SliceStable(it.UnpinnedImages, func(i, j int) bool {
		a, b := it.UnpinnedImages[i], it.UnpinnedImages[j]
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Detail < b.Detail
	})
	return it
}

func (it Items) norm() Items {
	it.ServiceItems = it.ServiceItems.norm()
	it.ProductionStacks = sorted(it.ProductionStacks)
	return it
}

// sorted is a sorted copy, never nil.
func sorted(xs []string) []string {
	out := append([]string{}, xs...)
	slices.Sort(out)
	return out
}

// stackKeyOf is the key of the stack a service or datastore fact belongs to:
// its kind from the fact key (service:<kind>:<stack>:<name>) and its stack
// attribute, so stacks of the same name in different kinds stay apart.
func stackKeyOf(f fact.Fact) string {
	st := f.Str("stack")
	parts := strings.SplitN(f.Key, ":", 3)
	if st == "" || len(parts) < 3 {
		return ""
	}
	return "stack:" + parts[1] + ":" + st
}
