// Package sbom writes a CycloneDX 1.6 bill of materials from one scan and
// its map. Facts and map in, document out: no file, network or clock access.
// The serial number comes from the facts file's hash and the timestamp from
// the commit, so the same scan gives the same bytes.
//
// What it lists: per service, the dependencies the attach rule puts on it,
// the transitive dependencies its manifest folders reach (from lockfiles),
// and the images it runs or builds from. What it does not: the contents of
// those images (OS packages), which Cernly never opens; each image says so
// in a property. See https://cernly.dev/how/sbom/.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

const SpecVersion = "1.6"

type BOM struct {
	BOMFormat    string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	SerialNumber string       `json:"serialNumber"`
	Version      int          `json:"version"`
	Metadata     Metadata     `json:"metadata"`
	Components   []Component  `json:"components"`
	Dependencies []Dependency `json:"dependencies"`
}

type Metadata struct {
	Timestamp  string     `json:"timestamp"`
	Tools      Tools      `json:"tools"`
	Component  Component  `json:"component"`
	Properties []Property `json:"properties"`
}

type Tools struct {
	Components []Component `json:"components"`
}

type Component struct {
	Type       string     `json:"type"`
	BOMRef     string     `json:"bom-ref,omitempty"`
	Name       string     `json:"name"`
	Version    string     `json:"version,omitempty"`
	Scope      string     `json:"scope,omitempty"`
	PURL       string     `json:"purl,omitempty"`
	Licenses   []License  `json:"licenses,omitempty"`
	Properties []Property `json:"properties,omitempty"`
}

type License struct {
	Expression string `json:"expression"`
}

type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

const repoRef = "repo"

// Build writes the BOM for the whole repo, or for one service (its key) when
// service is set. extractor is the version of the binary that writes it.
func Build(s *fact.Scan, m *rules.Map, service, extractor string) (*BOM, error) {
	return BuildScope(s, m, Scope{Service: service}, extractor)
}

// Scope narrows a BOM: one service, or the services of one stack (its key,
// stack:compose:<name>). Neither: the whole repo.
type Scope struct {
	Service string
	Stack   string
}

// StackServices lists the services the map knows on a stack, by the
// stack attribute of their service facts.
func StackServices(s *fact.Scan, m *rules.Map, stack string) []string {
	known := mapServices(m)
	var out []string
	for _, f := range s.Facts {
		if f.Kind == fact.KindService && "stack:compose:"+f.Str("stack") == stack && known[f.Key] {
			out = append(out, f.Key)
		}
	}
	sort.Strings(out)
	return out
}

func mapServices(m *rules.Map) map[string]bool {
	services := map[string]bool{}
	for _, a := range m.Attached {
		services[a.Service] = true
	}
	for _, l := range m.Links {
		services[l.Service] = true
	}
	for _, i := range m.Images {
		services[i.Service] = true
	}
	for _, c := range m.CIImages {
		services[c.Image] = true
	}
	return services
}

// BuildScope writes the BOM for a scope. A stack's BOM lists its services
// only, and records the stack's role from the map.
func BuildScope(s *fact.Scan, m *rules.Map, sc Scope, extractor string) (*BOM, error) {
	service := sc.Service
	facts := map[string]fact.Fact{}
	for _, f := range s.Facts {
		facts[f.Key] = f
	}
	b := &builder{facts: facts, comps: map[string]*Component{}, edges: map[string]map[string]bool{}}

	// Services: everything the map puts a dependency or an image on.
	services := mapServices(m)
	if service != "" && !services[service] {
		return nil, fmt.Errorf("service %s: not in the map", service)
	}
	want := func(svc string) bool { return service == "" || svc == service }
	role := ""
	if sc.Stack != "" {
		if service != "" {
			return nil, fmt.Errorf("pass a service or a stack, not both")
		}
		for _, st := range m.Stacks {
			if st.Stack == sc.Stack {
				role = st.Role
			}
		}
		if role == "" {
			return nil, fmt.Errorf("stack %s: not in the map", sc.Stack)
		}
		on := map[string]bool{}
		for _, k := range StackServices(s, m, sc.Stack) {
			on[k] = true
		}
		want = func(svc string) bool { return on[svc] }
	}

	// Transitive dependencies by the manifest folder that reaches them.
	byRoot := map[string][]fact.Fact{}
	for _, f := range s.Facts {
		if rules.Transitive(f) {
			for _, r := range f.Strs("roots") {
				byRoot[r] = append(byRoot[r], f)
			}
		}
	}

	onService := map[string]bool{} // dependency facts some service carries
	for _, svc := range slices.Sorted(maps.Keys(services)) {
		if !want(svc) {
			continue
		}
		ref := b.service(svc)
		b.edge(repoRef, ref)
		folders := map[string]bool{}
		for _, a := range m.Attached {
			if a.Service != svc || a.Kind != fact.KindDependency {
				continue
			}
			f, ok := facts[a.Fact]
			if !ok || f.Attrs["internal"] != nil {
				continue
			}
			onService[f.Key] = true
			b.edge(ref, b.pkg(f))
			for _, e := range f.Evidence {
				if e.Unit != "" {
					folders[e.Unit] = true
				}
			}
		}
		for _, dir := range slices.Sorted(maps.Keys(folders)) {
			for _, t := range byRoot[dir] {
				onService[t.Key] = true
				b.edge(ref, b.pkg(t))
			}
		}
		for _, img := range b.images(svc, m) {
			b.edge(ref, img)
		}
	}
	// The whole repo also lists the dependencies no service carries.
	if service == "" && sc.Stack == "" {
		for _, f := range s.Facts {
			if f.Kind == fact.KindDependency && !onService[f.Key] && f.Attrs["internal"] == nil {
				b.edge(repoRef, b.pkg(f))
			}
		}
	}

	subject, seed := s.Repo, service
	switch {
	case service != "":
		subject += " " + service
	case sc.Stack != "":
		subject += " " + sc.Stack
		seed = sc.Stack
	}
	sum := sha256.Sum256([]byte(m.Scan.SHA256 + "|" + seed))
	u := sum[:16]
	u[6] = (u[6] & 0x0f) | 0x50 // a name-based UUID: version 5 bits, RFC 4122 variant
	u[8] = (u[8] & 0x3f) | 0x80
	h := hex.EncodeToString(u)
	bom := &BOM{
		BOMFormat: "CycloneDX", SpecVersion: SpecVersion, Version: 1,
		SerialNumber: "urn:uuid:" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32],
		Metadata: Metadata{
			Timestamp: s.Commit.CommittedAt,
			Tools:     Tools{Components: []Component{{Type: "application", Name: "cernly", Version: extractor}}},
			Component: Component{Type: "application", BOMRef: repoRef, Name: subject, Version: s.Commit.SHA},
			Properties: []Property{
				{"cernly:facts_sha256", m.Scan.SHA256},
				{"cernly:rules_version", m.RulesVersion},
				{"cernly:scope", "Declared and locked dependencies and image references in the repository. Image contents are not inspected."},
			},
		},
		Components:   []Component{},
		Dependencies: []Dependency{},
	}
	if sc.Stack != "" {
		bom.Metadata.Properties = append(bom.Metadata.Properties, Property{"cernly:stack", sc.Stack}, Property{"cernly:stack_role", role})
	}
	for _, ref := range slices.Sorted(maps.Keys(b.comps)) {
		bom.Components = append(bom.Components, *b.comps[ref])
	}
	for _, ref := range slices.Sorted(maps.Keys(b.edges)) {
		bom.Dependencies = append(bom.Dependencies, Dependency{Ref: ref, DependsOn: slices.Sorted(maps.Keys(b.edges[ref]))})
	}
	// Every component appears in dependencies, as CycloneDX advises.
	for _, c := range bom.Components {
		if b.edges[c.BOMRef] == nil {
			bom.Dependencies = append(bom.Dependencies, Dependency{Ref: c.BOMRef, DependsOn: []string{}})
		}
	}
	sort.Slice(bom.Dependencies, func(i, j int) bool { return bom.Dependencies[i].Ref < bom.Dependencies[j].Ref })
	return bom, nil
}

type builder struct {
	facts map[string]fact.Fact
	comps map[string]*Component
	edges map[string]map[string]bool
}

func (b *builder) edge(from, to string) {
	if b.edges[from] == nil {
		b.edges[from] = map[string]bool{}
	}
	b.edges[from][to] = true
}

func (b *builder) service(key string) string {
	if b.comps[key] == nil {
		name := key
		if f, ok := b.facts[key]; ok {
			if n := f.Str("name"); n != "" {
				name = n
				if st := f.Str("stack"); st != "" {
					name = st + "/" + n
				}
			}
		}
		b.comps[key] = &Component{Type: "application", BOMRef: key, Name: name, Properties: []Property{{"cernly:fact", key}}}
	}
	return key
}

// pkg adds a dependency as a library, one component per package URL, with
// every fact behind it as evidence.
func (b *builder) pkg(f fact.Fact) string {
	eco, name, version := f.Str("ecosystem"), f.Str("name"), f.Str("version")
	exact := Exact(version)
	purl := PURL(eco, name, version, exact)
	c := b.comps[purl]
	if c == nil {
		c = &Component{Type: "library", BOMRef: purl, Name: name, Version: version, PURL: purl}
		if !exact && version != "" {
			c.Properties = append(c.Properties, Property{"cernly:version", "declared range, not locked"})
		}
		if l := f.Str("license"); l != "" {
			c.Licenses = []License{{Expression: l}}
		}
		b.comps[purl] = c
	}
	direct := "true"
	if rules.Transitive(f) {
		direct = "false"
	}
	c.Properties = addProp(c.Properties, "cernly:direct", direct)
	c.Properties = addProp(c.Properties, "cernly:fact", f.Key)
	if sc := f.Str("scope"); sc != "" {
		c.Properties = addProp(c.Properties, "cernly:dependency_scope", sc)
	}
	for _, e := range f.Evidence {
		c.Properties = addProp(c.Properties, "cernly:evidence", fmt.Sprintf("%s:%d", e.Path, e.Line))
	}
	c.Scope = componentScope(c.Properties)
	return purl
}

// componentScope: required when any fact says prod, excluded when every fact
// says dev, test or build (not shipped), else unset.
func componentScope(ps []Property) string {
	scopes := map[string]bool{}
	for _, p := range ps {
		if p.Name == "cernly:dependency_scope" {
			scopes[p.Value] = true
		}
	}
	switch {
	case scopes["prod"]:
		return "required"
	case scopes["unknown"] || len(scopes) == 0:
		return ""
	}
	return "excluded"
}

// images adds the images a service runs or builds from: its own image when it
// is a third-party one, and the base images of the recipes it is linked to.
func (b *builder) images(svc string, m *rules.Map) []string {
	var refs []string
	add := func(ref map[string]any, role, from string) {
		raw, _ := ref["raw"].(string)
		if raw == "" {
			return
		}
		repoName, _ := ref["repository"].(string)
		registry, _ := ref["registry"].(string)
		tag, _ := ref["tag"].(string)
		digest, _ := ref["digest"].(string)
		version := digest
		if version == "" {
			version = tag
		}
		key := "image:" + raw
		c := b.comps[key]
		if c == nil {
			c = &Component{Type: "container", BOMRef: key, Name: raw, Version: version,
				Properties: []Property{{"cernly:contents", "not-inspected"}}}
			if repoName != "" && version != "" && !strings.Contains(raw, "${") {
				c.PURL = "pkg:docker/" + repoName + "@" + strings.ReplaceAll(version, ":", "%3A")
				if registry != "" && registry != "docker.io" {
					c.PURL += "?repository_url=" + registry
				}
			}
			if p, _ := ref["pinning"].(string); p != "" {
				c.Properties = append(c.Properties, Property{"cernly:pinning", p})
			}
			b.comps[key] = c
		}
		c.Properties = addProp(c.Properties, "cernly:image_role", role)
		c.Properties = addProp(c.Properties, "cernly:fact", from)
		refs = append(refs, key)
	}
	for _, i := range m.Images {
		if i.Service != svc || i.Class != "third_party" {
			continue
		}
		if f, ok := b.facts[svc]; ok {
			if ref, ok := f.Attrs["image_ref"].(map[string]any); ok {
				add(ref, "runs", svc)
			}
		}
	}
	recipes := map[string]bool{}
	for _, l := range m.Links {
		if l.Service == svc {
			recipes[l.Recipe] = true
		}
	}
	for _, c := range m.CIImages {
		if c.Image == svc {
			recipes[c.Recipe] = true
		}
	}
	for _, r := range slices.Sorted(maps.Keys(recipes)) {
		f, ok := b.facts[r]
		if !ok {
			continue
		}
		bases, _ := f.Attrs["base_images"].([]any)
		for _, x := range bases {
			bi, _ := x.(map[string]any)
			if art, ok := bi["artifact"].(map[string]any); ok {
				add(art, "base", r)
			}
		}
	}
	return refs
}

// Exact reports a version that names one release, not a range or a spec.
func Exact(v string) bool {
	if v == "" || strings.ContainsAny(v, "^~<>=*| ,") || strings.Contains(v, ":") {
		return false
	}
	return !strings.EqualFold(v, "x") && !strings.HasSuffix(v, ".x")
}

// PURL is the package URL of a dependency; the version only when exact.
func PURL(eco, name, version string, exact bool) string {
	typ := map[string]string{"go": "golang", "npm": "npm", "pypi": "pypi", "nuget": "nuget"}[eco]
	if typ == "" {
		typ = "generic"
	}
	n := name
	if typ == "npm" && strings.HasPrefix(n, "@") {
		n = "%40" + n[1:]
	}
	// nuget (purl-spec types/nuget-definition.json, https://github.com/package-url/purl-spec):
	// "Technically the name is case-preserving, but case-insensitive." So the name stays as written.
	if typ == "pypi" {
		n = strings.ToLower(strings.ReplaceAll(n, "_", "-"))
	}
	p := "pkg:" + typ + "/" + n
	if exact {
		p += "@" + strings.ReplaceAll(version, "+", "%2B")
	}
	return p
}

func addProp(ps []Property, name, value string) []Property {
	for _, p := range ps {
		if p.Name == name && p.Value == value {
			return ps
		}
	}
	ps = append(ps, Property{name, value})
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].Value < ps[j].Value
	})
	return ps
}
