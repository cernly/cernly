// Package rules derives the map (schema cernly.map/v1) from a scan's facts:
// links from services to their code, and which stack is production. Facts
// in, map out: no file, network or clock access, so the same facts and
// settings give the same map. See https://cernly.dev/how/map/.
package rules

import (
	"slices"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/repo"
)

const (
	SchemaID = "cernly.map/v1"
	// Bump on any change to the map output; see CHANGELOG.md.
	Version = "0.17.1"
)

type Settings struct {
	DefaultBranch string `json:"default_branch"`
	// Confirmations in effect: decisions a person made outside the repo, passed by the caller. Absent
	// when there are none, so older maps keep their bytes.
	Confirmations []Confirmation `json:"confirmations,omitempty"`
}

type ScanRef struct {
	Repo      string `json:"repo"`
	Commit    string `json:"commit"`
	Schema    string `json:"schema"`
	Extractor string `json:"extractor"`
	SHA256    string `json:"sha256"`
}

type Map struct {
	Schema           string        `json:"schema"`
	Scan             ScanRef       `json:"scan"`
	RulesVersion     string        `json:"rules_version"`
	CatalogueVersion string        `json:"catalogue_version"`
	Settings         Settings      `json:"settings"`
	Stacks           []StackRole   `json:"stacks"`
	Links            []Link        `json:"links"`
	Images           []Image       `json:"images"`
	CIImages         []CIImage     `json:"ci_images"`
	Attached         []Attached    `json:"attached"`
	Parties          []Party       `json:"parties"`
	Internal         []InternalUse `json:"internal"`
	Unknown          []UnknownHost `json:"unknown"`
	Own              []OwnHost     `json:"own"`
	Routes           []RouteEdge   `json:"routes"`
	// Endpoints of the app code, one row per subject. Absent when the scan has no endpoint facts.
	Endpoints []EndpointRow `json:"endpoints,omitempty"`
	AITools   []AITool      `json:"ai_tools"`
	Findings  []Finding     `json:"findings"`
	// Database tables merged across schema sources, with the columns to
	// check. Absent when the scan has no db_table facts.
	Tables []Table `json:"tables,omitempty"`
	// Test suites per service and the commands that run them. Absent
	// without test_suite facts.
	Tests []TestPresence `json:"tests,omitempty"`
	// What each confirmation in settings did, and the parties and hosts a
	// person dismissed. Absent without confirmations.
	Confirmations []ConfirmationUse `json:"confirmations,omitempty"`
	Dismissed     []Dismissed       `json:"dismissed,omitempty"`
	// The import graph of the repo's own packages. Absent when the scan has
	// no import facts.
	Modules *ModuleGraph `json:"modules,omitempty"`
	// Surface metrics in total, in production and per service, with size
	// and coupling as context.
	Metrics *Metrics `json:"metrics"`
}

// Stack roles.
const (
	RoleProduction  = "production"
	RoleUnconfirmed = "production (unconfirmed)"
	RoleNone        = "none"
)

type StackRole struct {
	Stack       string      `json:"stack"`
	Name        string      `json:"name,omitempty"` // declared display name
	Role        string      `json:"role"`
	DerivedRole string      `json:"derived_role,omitempty"` // what the rules derived, when a declaration replaced it
	Declared    *Declarant  `json:"declared,omitempty"`
	Confirmed   *ConfirmRef `json:"confirmed,omitempty"` // a confirmation from the caller
	Manual      bool        `json:"manual"`
	Reasons     []Reason    `json:"reasons"`
}

// Declarant is who declared something and where.
type Declarant struct {
	By   string `json:"by"` // repo (.cernly.yml) or operator
	Line int    `json:"line,omitempty"`
}

// AITool is an AI coding tool the repo is set up for (agent_config facts):
// repo-level proof, not proof of which change it wrote.
type AITool struct {
	Tool  string   `json:"tool"`
	Name  string   `json:"name"`
	Files int      `json:"files"`
	From  []string `json:"from"`
}

// OwnHost is a host on one of the organisation's own declared domains: never an
// outside party, never unknown. OwnHost is a host of the organisation's own: on
// a declared own domain, or served by a route of the repo (proof), or
// both. Domain is the declared domain, or
// the host itself when it is only served.
type OwnHost struct {
	Host      string      `json:"host"`
	Domain    string      `json:"domain"`
	Declared  *Declarant  `json:"declared,omitempty"`
	ServedBy  []string    `json:"served_by,omitempty"`
	Confirmed *ConfirmRef `json:"confirmed,omitempty"` // claimed as own by a confirmation
	From      []string    `json:"from"`
}

// Reason is the verdict on one workflow job that starts the stack.
type Reason struct {
	Workflow   string   `json:"workflow"`
	Job        string   `json:"job"`
	Role       string   `json:"role"`
	Detail     string   `json:"detail"`
	Unreadable []string `json:"unreadable,omitempty"`
	TestJob    *TestJob `json:"test_job,omitempty"`
}

type Link struct {
	Service   string   `json:"service"`
	Recipe    string   `json:"recipe"`
	Contexts  []string `json:"contexts"`
	Sources   []string `json:"sources"` // repo paths: the recipe's sources inside each build context
	Precision string   `json:"precision"`
	// Narrowing: whether the sources were cut to the entrypoint's project
	// closure; Narrowed names those project folders, Excluded the code-unit
	// folders left out.
	Narrowing Narrowing `json:"narrowing"`
	Narrowed  []string  `json:"narrowed,omitempty"`
	Excluded  []string  `json:"excluded,omitempty"`
	Via       []string  `json:"via"`
	From      []string  `json:"from"`
}

// Attached puts a fact that lives in a service's code on that service: a
// dependency whose manifest, an env var or outbound host whose file, lies
// in the sources of a recipe the service is linked to. Nothing is filtered;
// scope is shown as the fact has it. Service is a service key, or a
// ci_image key for an image built in CI.
type Attached struct {
	Service  string   `json:"service"`
	Fact     string   `json:"fact"`
	Kind     string   `json:"kind"`
	Scope    string   `json:"scope,omitempty"`
	Internal string   `json:"internal,omitempty"` // repo folder, when the dependency is an internal package
	Source   string   `json:"source"`             // the link source the evidence lies in
	From     []string `json:"from"`
}

// Kinds the attach rule puts on services.
var attachKinds = map[string]bool{fact.KindDependency: true, fact.KindEnvVar: true, fact.KindOutboundHost: true}

type Image struct {
	Service   string   `json:"service"`
	ImageRepo string   `json:"image_repo"`
	Class     string   `json:"class"`
	From      []string `json:"from"`
}

// Finding rules.
const (
	FindingNoCodeLink            = "no_code_link"
	FindingLinkConflict          = "link_conflict"
	FindingProductionUnconfirmed = "production_unconfirmed"
	FindingImagePinning          = "image_pinning"
)

type Finding struct {
	Rule    string   `json:"rule"`
	Subject string   `json:"subject"`
	Grade   string   `json:"grade,omitempty"` // image_pinning: floating, unknown or exact
	Image   string   `json:"image,omitempty"` // image_pinning: registry/repository, no tag
	Detail  string   `json:"detail"`
	From    []string `json:"from"`
}

// Derive builds the map with the embedded catalogue. sha256 is the hash of
// the facts file as read.
func Derive(s *fact.Scan, sha256 string, set Settings) *Map {
	return DeriveWith(s, sha256, set, LoadCatalogue())
}

// DeriveWith builds the map with a given catalogue.
func DeriveWith(s *fact.Scan, sha256 string, set Settings, cat Catalogue) *Map {
	m := &Map{
		Schema:           SchemaID,
		Scan:             ScanRef{Repo: s.Repo, Commit: s.Commit.SHA, Schema: s.Schema, Extractor: s.Extractor.Version, SHA256: sha256},
		RulesVersion:     Version,
		CatalogueVersion: cat.Version,
		Settings:         set,
		Stacks:           []StackRole{},
		Links:            []Link{},
		Images:           []Image{},
		CIImages:         []CIImage{},
		Attached:         []Attached{},
		Parties:          []Party{},
		Internal:         []InternalUse{},
		Unknown:          []UnknownHost{},
		Own:              []OwnHost{},
		Routes:           []RouteEdge{},
		AITools:          []AITool{},
		Findings:         []Finding{},
	}
	facts := append([]fact.Fact(nil), s.Facts...)
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Kind != facts[j].Kind {
			return facts[i].Kind < facts[j].Kind
		}
		return facts[i].Key < facts[j].Key
	})
	link(m, facts)
	m.CIImages = ciImages(m, facts)
	attach(m, facts)
	routes(m, facts)
	endpoints(m, facts)
	var decl *fact.Declared
	if s.Config != nil {
		decl = s.Config.Declared
	}
	parties(m, facts, cat, decl)
	for _, f := range facts {
		if f.Kind == fact.KindAgentConfig {
			n, _ := f.Attrs["files"].(float64)
			if i, ok := f.Attrs["files"].(int); ok {
				n = float64(i)
			}
			m.AITools = append(m.AITools, AITool{Tool: f.Str("tool"), Name: f.Str("name"), Files: int(n), From: []string{f.Key}})
		}
	}
	roles(m, facts, set.DefaultBranch, decl)
	endpointFindings(m, facts)
	pins(m, facts)
	m.Tables = tables(facts)
	m.Tests = tests(m, facts)
	drivers(m, facts)
	confirm(m)
	m.Modules = modules(m, facts)
	m.Metrics = metrics(m, facts)
	sort.Slice(m.Findings, func(i, j int) bool {
		a, b := m.Findings[i], m.Findings[j]
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Detail < b.Detail
	})
	return m
}

// Transitive reports a dependency from a lockfile that no manifest declares.
// The rules read direct dependencies only: the attach rule, the provider
// catalogue's SDK signal and the gate's new_dependency. The SBOM reads
// both.
func Transitive(f fact.Fact) bool {
	direct, ok := f.Attrs["direct"].(bool)
	return f.Kind == fact.KindDependency && ok && !direct
}

func uniq(xs []string) []string {
	slices.Sort(xs)
	return slices.Compact(xs)
}

// recipes indexes image_recipe facts by Dockerfile and target; "" is the
// final stage, built when no target is given.
type recipes map[string]map[string]fact.Fact

func (r recipes) find(dockerfile, target string) (fact.Fact, bool) {
	stages := r[dockerfile]
	if target != "" {
		f, ok := stages[target]
		return f, ok
	}
	f, ok := stages[""]
	return f, ok
}

// link applies the link rule: compose build: and the image's build command,
// both to an image_recipe.
func link(m *Map, facts []fact.Fact) {
	rec := recipes{}
	builds := map[string][]fact.Fact{}
	for _, f := range facts {
		switch f.Kind {
		case fact.KindImageRecipe:
			df := f.Str("dockerfile")
			if rec[df] == nil {
				rec[df] = map[string]fact.Fact{}
			}
			rec[df][f.Str("target")] = f
			if f.Attrs["final"] == true {
				rec[df][""] = f
			}
		case fact.KindImageBuild:
			repo := f.Str("image_repo")
			builds[repo] = append(builds[repo], f)
		}
	}
	for _, svc := range facts {
		if svc.Kind != fact.KindService {
			continue
		}
		type cand struct {
			recipe   fact.Fact
			via      []string
			from     []string
			contexts []string
		}
		found := map[string]*cand{}
		var order []string
		add := func(r fact.Fact, via string, contexts []string, from ...string) {
			c := found[r.Key]
			if c == nil {
				c = &cand{recipe: r}
				found[r.Key] = c
				order = append(order, r.Key)
			}
			c.via = append(c.via, via)
			c.from = append(c.from, from...)
			c.contexts = append(c.contexts, contexts...)
		}
		var missing []string
		if df := svc.Str("dockerfile"); df != "" {
			if r, ok := rec.find(df, svc.Str("target")); ok {
				add(r, "compose_build", svc.Strs("build_context"), svc.Key, r.Key)
			} else {
				missing = append(missing, "build: names "+stageRef(df, svc.Str("target"))+", no such recipe")
			}
		}
		image := svc.Str("image")
		repo := ""
		if image != "" {
			repo = imageref.Repo(image)
			for _, b := range builds[repo] {
				if r, ok := rec.find(b.Str("dockerfile"), b.Str("target")); ok && b.Attrs["dockerfile_resolved"] == true {
					add(r, "image_build", b.Strs("contexts"), svc.Key, b.Key, r.Key)
				} else {
					missing = append(missing, b.Key+" names "+stageRef(b.Str("dockerfile"), b.Str("target"))+", no such recipe")
				}
			}
		}
		sort.Strings(order)
		for _, k := range order {
			c := found[k]
			ctxs := uniq(c.contexts)
			srcs := inContexts(c.recipe.Strs("sources"), ctxs)
			nw, narrowed, excluded := narrow(c.recipe, srcs, facts)
			m.Links = append(m.Links, Link{Service: svc.Key, Recipe: k, Contexts: ctxs, Sources: srcs,
				Precision: precisionOf(nw), Narrowing: nw, Narrowed: narrowed, Excluded: excluded, Via: uniq(c.via), From: uniq(c.from)})
		}
		switch {
		case len(order) > 1:
			m.Findings = append(m.Findings, Finding{Rule: FindingLinkConflict, Subject: svc.Key,
				Detail: "builds point to " + strings.Join(order, ", "), From: uniq(append([]string{svc.Key}, order...))})
		case len(order) == 0 && len(missing) > 0:
			m.Findings = append(m.Findings, Finding{Rule: FindingNoCodeLink, Subject: svc.Key,
				Detail: strings.Join(missing, "; "), From: []string{svc.Key}})
		case len(order) == 0 && repo != "":
			m.Images = append(m.Images, Image{Service: svc.Key, ImageRepo: repo, Class: "third_party", From: []string{svc.Key}})
		}
	}
}

// attach applies the attach rule to every link and every image built in CI.
func attach(m *Map, facts []fact.Fact) {
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
	units := map[string]bool{} // the .NET code units narrow counts
	for _, f := range facts {
		addUnits(units, f)
	}
	seen := map[string]bool{}
	for _, l := range ts {
		for _, f := range facts {
			if !attachKinds[f.Kind] || Transitive(f) {
				continue
			}
			src := ""
			for _, e := range f.Evidence {
				if f.Kind == fact.KindDependency && units[e.Unit] && insideAny(e.Unit, l.excluded) {
					continue // a dependency evidence of a project the narrowing left out (other kinds name services as units)
				}
				for _, s := range l.sources {
					if covers(e.Path, []string{s}, l.excluded) && len(s) > len(src) {
						src = s
					}
				}
			}
			if src == "" || seen[l.subject+"|"+f.Key] {
				continue
			}
			seen[l.subject+"|"+f.Key] = true
			in, _ := f.Attrs["internal"].(map[string]any)
			from := []string{f.Key, l.recipe}
			if !strings.HasPrefix(l.subject, "ci_image:") {
				from = append(from, l.subject) // a service is a fact; a CI image is derived from its recipe
			}
			m.Attached = append(m.Attached, Attached{Service: l.subject, Fact: f.Key, Kind: f.Kind, Scope: f.Str("scope"),
				Internal: anyStr(in["path"]), Source: src, From: uniq(from)})
		}
	}
	attachCompose(m, facts, seen)
	sort.Slice(m.Attached, func(i, j int) bool {
		a, b := m.Attached[i], m.Attached[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Fact < b.Fact
	})
}

// attachCompose puts env vars declared or interpolated on a Compose service
// on that service in every stack whose files include the compose file: the
// evidence unit names the service.
func attachCompose(m *Map, facts []fact.Fact, seen map[string]bool) {
	byStack := map[string]map[string]string{} // stack name -> compose name -> fact key
	inStack := map[string][]string{}          // compose file -> stack names
	for _, f := range facts {
		switch f.Kind {
		case fact.KindService, fact.KindDatastore:
			if !strings.HasPrefix(f.Key, "service:compose:") && !strings.HasPrefix(f.Key, "datastore:compose:") {
				continue // another tool's stack may share the name
			}
			st := f.Str("stack")
			if byStack[st] == nil {
				byStack[st] = map[string]string{}
			}
			byStack[st][f.Str("name")] = f.Key
		case fact.KindStack:
			if f.Str("tool") != "compose" {
				continue
			}
			for _, file := range f.Strs("files") {
				inStack[file] = append(inStack[file], f.Str("name"))
			}
		}
	}
	for _, f := range facts {
		if f.Kind != fact.KindEnvVar {
			continue
		}
		for _, e := range f.Evidence {
			if e.Unit == "" {
				continue
			}
			for _, st := range inStack[e.Path] {
				svc := byStack[st][e.Unit]
				if svc == "" || seen[svc+"|"+f.Key] {
					continue
				}
				seen[svc+"|"+f.Key] = true
				m.Attached = append(m.Attached, Attached{Service: svc, Fact: f.Key, Kind: f.Kind, Source: e.Path,
					From: uniq([]string{f.Key, svc, "stack:compose:" + st})})
			}
		}
	}
}

// pins grades every image reference that is not pinned by digest: service
// and data store images, and Dockerfile base images once per stage that
// names them.
func pins(m *Map, facts []fact.Fact) {
	add := func(subject string, ref map[string]any, what string) {
		grade, _ := ref["pinning"].(string)
		if grade == "" || grade == "digest" {
			return
		}
		raw, _ := ref["raw"].(string)
		image, _ := ref["repository"].(string)
		if reg, _ := ref["registry"].(string); reg != "" && image != "" {
			image = reg + "/" + image
		}
		m.Findings = append(m.Findings, Finding{Rule: FindingImagePinning, Subject: subject, Grade: grade, Image: image,
			Detail: what + " " + raw + " is not pinned by digest", From: []string{subject}})
	}
	// A base image is reported on the recipe of the stage that names it, or,
	// for an unnamed intermediate stage, on the first recipe that inherits it.
	type base struct {
		subject, stage string
		ref            map[string]any
	}
	bases := map[string]*base{}
	for _, f := range facts {
		switch f.Kind {
		case fact.KindService, fact.KindDatastore:
			if ref, ok := f.Attrs["image_ref"].(map[string]any); ok {
				add(f.Key, ref, "image")
			}
		case fact.KindImageRecipe:
			bs, _ := f.Attrs["base_images"].([]any)
			for _, b := range bs {
				bm, _ := b.(map[string]any)
				ref, ok := bm["artifact"].(map[string]any)
				if !ok {
					continue
				}
				stage := anyStr(bm["stage"])
				k := f.Str("dockerfile") + "|" + stage + "|" + anyStr(bm["role"]) + "|" + anyStr(bm["ref"])
				if bases[k] == nil || stage == f.Str("target") {
					bases[k] = &base{subject: f.Key, stage: stage, ref: ref}
				}
			}
		}
	}
	keys := make([]string, 0, len(bases))
	for k := range bases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := bases[k]
		add(b.subject, b.ref, "stage "+b.stage+" uses")
	}
}

// inside reports whether repo path p is the source s or lies in it.
func inside(p, s string) bool {
	return s == "." || p == s || strings.HasPrefix(p, s+"/")
}

// inContexts maps a recipe's sources, written relative to the build
// context, to repo paths in every context the link was built with. Sources
// that would leave the repo are dropped.
func inContexts(sources, contexts []string) []string {
	out := []string{}
	for _, ctx := range contexts {
		for _, src := range sources {
			if p, ok := repo.Join(ctx, src); ok {
				out = append(out, p)
			}
		}
	}
	return uniq(out)
}

func stageRef(dockerfile, target string) string {
	if target == "" {
		return dockerfile + " (final stage)"
	}
	return dockerfile + ":" + target
}
