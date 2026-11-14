package rules

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
)

// Catalogue is the provider catalogue: data, versioned, embedded.
type Catalogue struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
}

type Entry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	Kind            string   `json:"kind"` // provider or protocol
	SDKs            []SDK    `json:"sdks"`
	EnvPrefixes     []string `json:"env_prefixes"`
	Hosts           []string `json:"hosts"`
	Images          []string `json:"images"`
	InternalEngines []string `json:"internal_engines,omitempty"` // protocol: a data store of this engine in the stack serves it
	ResolvedBy      []string `json:"resolved_by,omitempty"`      // protocol: a provider whose match on the same service names it
	Sources         []string `json:"sources"`
	Checked         string   `json:"checked"`
	Review          string   `json:"review"`
}

// SDK is a Go module (prefix up to a path boundary, so major versions and
// subpackages match) or an npm package (exact, or a scope when it ends in /).
type SDK struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// LoadCatalogue returns the embedded catalogue.
func LoadCatalogue() Catalogue {
	var c Catalogue
	if err := json.Unmarshal(defaults.Providers, &c); err != nil {
		panic("rules: embedded catalogue: " + err.Error()) // caught by any test run
	}
	return c
}

// Party grades.
const (
	GradeConfirmed = "confirmed" // two or more signal kinds agree, from two or more facts
	GradeSingle    = "single"    // one signal kind, or one fact
)

type Party struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Category  string      `json:"category"`
	Kind      string      `json:"kind"`
	Grade     string      `json:"grade"`
	Services  []string    `json:"services"` // empty: matched only by facts on no service
	Signals   []Signal    `json:"signals"`
	Confirmed *ConfirmRef `json:"confirmed,omitempty"` // approved by a confirmation
}

type Signal struct {
	Kind string `json:"kind"` // sdk, env, host, image
	Fact string `json:"fact"`
}

// InternalUse is a protocol served inside the stack (S3 by a MinIO data
// store), so it is left off the outside parties.
type InternalUse struct {
	ID        string   `json:"id"`
	Service   string   `json:"service"`
	Datastore string   `json:"datastore"`
	From      []string `json:"from"`
}

// UnknownHost is a host no catalogue entry claims: the unknown bucket. Usage
// is the strongest way it was seen: request, default (outbound), env_hint
// (an example env value), mention (a literal that is not shown to be called).
type UnknownHost struct {
	Host      string      `json:"host"`
	Usage     string      `json:"usage"`
	Confirmed *ConfirmRef `json:"confirmed,omitempty"` // approved as a supplier by a confirmation
	From      []string    `json:"from"`
}

var usageRank = map[string]int{"mention": 1, "env_hint": 2, "default": 3, "request": 4}

const FindingPartyUnconfirmed = "party_unconfirmed"

func (e Entry) matchSDK(f fact.Fact) bool {
	eco, name := f.Str("ecosystem"), f.Str("name")
	for _, s := range e.SDKs {
		if s.Ecosystem != eco {
			continue
		}
		switch {
		case eco == "go" && (name == s.Name || strings.HasPrefix(name, s.Name+"/")):
			return true
		case eco == "npm" && strings.HasSuffix(s.Name, "/") && strings.HasPrefix(name, s.Name):
			return true
		case eco == "npm" && name == s.Name:
			return true
		case eco == "pypi" && name == pypiName(s.Name):
			return true // both sides PEP 503-normalised
		case eco == "nuget" && (strings.EqualFold(name, s.Name) ||
			strings.HasSuffix(s.Name, ".*") && len(name) >= len(s.Name)-1 && strings.EqualFold(name[:len(s.Name)-1], s.Name[:len(s.Name)-1])):
			return true // NuGet ids ignore case; "Prefix.*" names a family
		}
	}
	return false
}

func (e Entry) matchEnv(name string) bool {
	for _, p := range e.EnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (e Entry) matchHost(h string) bool {
	for _, d := range e.Hosts {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// parties applies the catalogue. It runs after attach: a party is used by a
// service when one of its matching facts is attached to it.
func parties(m *Map, facts []fact.Fact, cat Catalogue, decl *fact.Declared) {
	subjects := map[string][]string{} // fact key -> services and data stores it is attached to
	for _, a := range m.Attached {
		subjects[a.Fact] = append(subjects[a.Fact], a.Service)
	}
	byKey := map[string]fact.Fact{}
	stackEngines := map[string]map[string]string{} // stack -> engine -> data store key
	for _, f := range facts {
		byKey[f.Key] = f
		if f.Kind == fact.KindDatastore {
			st := f.Str("stack")
			if stackEngines[st] == nil {
				stackEngines[st] = map[string]string{}
			}
			if _, ok := stackEngines[st][f.Str("engine")]; !ok {
				stackEngines[st][f.Str("engine")] = f.Key
			}
		}
	}

	// entry id -> subject ("" = no service) -> signals
	found := map[string]map[string]*partyUse{}
	claimed := map[string]bool{} // host hints a catalogue entry claims
	add := func(id, subject string, s Signal) {
		if found[id] == nil {
			found[id] = map[string]*partyUse{}
		}
		u := found[id][subject]
		if u == nil {
			u = &partyUse{signals: map[Signal]bool{}}
			found[id][subject] = u
		}
		u.signals[s] = true
	}
	onSubjects := func(id, factKey string, s Signal) {
		subs := subjects[factKey]
		if len(subs) == 0 {
			add(id, "", s)
		}
		for _, sub := range subs {
			add(id, sub, s)
		}
	}
	for _, f := range facts {
		for _, e := range cat.Entries {
			switch f.Kind {
			case fact.KindDependency:
				if internal, _ := f.Attrs["internal"].(map[string]any); internal == nil && !Transitive(f) && e.matchSDK(f) {
					onSubjects(e.ID, f.Key, Signal{Kind: "sdk", Fact: f.Key})
				}
			case fact.KindEnvVar:
				if e.matchEnv(f.Str("name")) {
					onSubjects(e.ID, f.Key, Signal{Kind: "env", Fact: f.Key})
				}
				for _, h := range f.Strs("host_hints") {
					if e.matchHost(h) {
						claimed[h] = true
						onSubjects(e.ID, f.Key, Signal{Kind: "host", Fact: f.Key})
					}
				}
			case fact.KindOutboundHost:
				// A mention never makes a party; a request or a default does.
				if h := f.Str("host"); f.Str("usage") != "mention" && e.matchHost(h) {
					claimed[h] = true
					onSubjects(e.ID, f.Key, Signal{Kind: "host", Fact: f.Key})
				}
			case fact.KindService, fact.KindDatastore:
				ref, _ := f.Attrs["image_ref"].(map[string]any)
				for _, img := range e.Images {
					if anyStr(ref["repository"]) == img {
						add(e.ID, f.Key, Signal{Kind: "image", Fact: f.Key})
					}
				}
			}
		}
	}

	for _, e := range cat.Entries {
		uses := found[e.ID]
		if len(uses) == 0 {
			continue
		}
		subs := make([]string, 0, len(uses))
		for s := range uses {
			subs = append(subs, s)
		}
		// Services first, facts on no service ("") last: a protocol's
		// service uses decide whether those count.
		sort.Slice(subs, func(i, j int) bool { return subs[i] != "" && (subs[j] == "" || subs[i] < subs[j]) })
		var services []string
		signals := map[Signal]bool{}
		resolvedInside := false
		for _, sub := range subs {
			if e.Kind == "protocol" && sub != "" {
				// A data store in the same stack is more specific than a
				// repo-wide host hint, so it decides first.
				if ds := internalStore(byKey[sub], e.InternalEngines, stackEngines); ds != "" {
					resolvedInside = true
					if ds == sub {
						continue // the data store that serves the protocol is not its user
					}
					from := []string{sub, ds}
					for s := range uses[sub].signals {
						from = append(from, s.Fact)
					}
					m.Internal = append(m.Internal, InternalUse{ID: e.ID, Service: sub, Datastore: ds, From: uniq(from)})
					continue
				}
				if resolvedOn(found, e.ResolvedBy, sub) {
					continue // the provider that matched on this service names it
				}
			}
			if sub == "" && e.Kind == "protocol" && resolvedInside {
				continue // facts on no service add nothing to a protocol served inside
			}
			if sub != "" {
				services = append(services, sub)
			}
			for s := range uses[sub].signals {
				signals[s] = true
			}
		}
		if len(signals) == 0 {
			continue
		}
		p := Party{ID: e.ID, Name: e.Name, Category: e.Category, Kind: e.Kind, Services: []string{}, Signals: []Signal{}}
		p.Services = append(p.Services, services...)
		kinds, facts := map[string]bool{}, map[string]bool{}
		for s := range signals {
			p.Signals = append(p.Signals, s)
			kinds[s.Kind] = true
			facts[s.Fact] = true
		}
		sort.Slice(p.Signals, func(i, j int) bool {
			if p.Signals[i].Kind != p.Signals[j].Kind {
				return p.Signals[i].Kind < p.Signals[j].Kind
			}
			return p.Signals[i].Fact < p.Signals[j].Fact
		})
		// One fact can carry two kinds (an env name and its host hint), so
		// it never confirms on its own.
		p.Grade = GradeSingle
		if len(kinds) >= 2 && len(facts) >= 2 {
			p.Grade = GradeConfirmed
		}
		m.Parties = append(m.Parties, p)
		if p.Grade == GradeSingle {
			var from []string
			for _, s := range p.Signals {
				from = append(from, s.Fact)
			}
			detail := e.Name + " rests on one kind of signal (" + p.Signals[0].Kind + "); confirm once"
			if len(kinds) >= 2 {
				detail = e.Name + " rests on one fact (" + p.Signals[0].Fact + "); confirm once"
			}
			m.Findings = append(m.Findings, Finding{Rule: FindingPartyUnconfirmed, Subject: "party:" + e.ID, Detail: detail, From: uniq(from)})
		}
	}
	sort.Slice(m.Internal, func(i, j int) bool {
		if m.Internal[i].ID != m.Internal[j].ID {
			return m.Internal[i].ID < m.Internal[j].ID
		}
		return m.Internal[i].Service < m.Internal[j].Service
	})

	unknown := map[string]*UnknownHost{}
	own := map[string]*OwnHost{}
	declaredFor := func(h string) (string, *Declarant) {
		if decl != nil {
			for _, d := range decl.OwnDomains {
				if h == d.Domain || strings.HasSuffix(h, "."+d.Domain) {
					return d.Domain, &Declarant{By: d.By, Line: d.Line}
				}
			}
		}
		return "", nil
	}
	for h, by := range servedHosts(facts) {
		o := &OwnHost{Host: h, Domain: h, ServedBy: uniq(by)}
		if d, who := declaredFor(h); who != nil {
			o.Domain, o.Declared = d, who
		}
		o.From = append([]string(nil), o.ServedBy...)
		own[h] = o
	}
	seen := func(h, usage, key string) {
		if claimed[h] {
			return
		}
		if o := own[h]; o != nil && len(o.ServedBy) > 0 {
			o.From = append(o.From, key)
			return
		}
		if decl != nil {
			for _, d := range decl.OwnDomains {
				if h == d.Domain || strings.HasSuffix(h, "."+d.Domain) {
					o := own[h]
					if o == nil {
						o = &OwnHost{Host: h, Domain: d.Domain, Declared: &Declarant{By: d.By, Line: d.Line}}
						own[h] = o
					}
					o.From = append(o.From, key)
					return
				}
			}
		}
		u := unknown[h]
		if u == nil {
			u = &UnknownHost{Host: h, Usage: usage}
			unknown[h] = u
		}
		if usageRank[usage] > usageRank[u.Usage] {
			u.Usage = usage
		}
		u.From = append(u.From, key)
	}
	for _, f := range facts {
		switch f.Kind {
		case fact.KindEnvVar:
			for _, h := range f.Strs("host_hints") {
				seen(h, "env_hint", f.Key)
			}
		case fact.KindOutboundHost:
			seen(f.Str("host"), f.Str("usage"), f.Key)
		}
	}
	ownHosts := make([]string, 0, len(own))
	for h := range own {
		ownHosts = append(ownHosts, h)
	}
	sort.Strings(ownHosts)
	for _, h := range ownHosts {
		o := own[h]
		o.From = uniq(o.From)
		m.Own = append(m.Own, *o)
	}
	hosts := make([]string, 0, len(unknown))
	for h := range unknown {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		u := unknown[h]
		u.From = uniq(u.From)
		m.Unknown = append(m.Unknown, *u)
	}
}

type partyUse struct {
	signals map[Signal]bool
}

// resolvedOn reports whether one of the providers that name a protocol
// matched on the same service.
func resolvedOn(found map[string]map[string]*partyUse, ids []string, sub string) bool {
	for _, id := range ids {
		if found[id][sub] != nil {
			return true
		}
	}
	return false
}

// internalStore returns the data store in the subject's stack whose engine
// serves the protocol, if any.
func internalStore(subject fact.Fact, engines []string, stackEngines map[string]map[string]string) string {
	for _, e := range engines {
		if ds := stackEngines[subject.Str("stack")][e]; ds != "" {
			return ds
		}
	}
	return ""
}

var pypiSep = regexp.MustCompile(`[-_.]+`)

// pypiName is PEP 503's normalisation, as deps-python writes names.
func pypiName(n string) string { return pypiSep.ReplaceAllString(strings.ToLower(n), "-") }
