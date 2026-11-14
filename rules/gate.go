package rules

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
)

// The gate: what a scan adds compared with a base scan the caller passes,
// judged by the policy in the head scan's config section. Two scans and
// their maps in, a result out: no file, network or clock access, so it is
// the same pure function wherever it is called.

const GateSchemaID = "cernly.gate/v1"

// GateResult is the outcome of one comparison.
type GateResult struct {
	Schema       string        `json:"schema"`
	Base         ScanRef       `json:"base"`
	Head         ScanRef       `json:"head"`
	RulesVersion string        `json:"rules_version"`
	Policy       []GatePolicy  `json:"policy"`
	Findings     []GateFinding `json:"findings"`
	Blocked      bool          `json:"blocked"` // a finding at level block
}

// GatePolicy is one rule's level in effect, and who set it. By is default
// when neither side declared the rule.
type GatePolicy struct {
	Rule  string `json:"rule"`
	Level string `json:"level"`
	By    string `json:"by"`
	Line  int    `json:"line,omitempty"`
	// Licences: licence_not_allowed only, the ids from both sides (a deny
	// list is stricter the longer it is).
	Licences []string `json:"licences,omitempty"`
}

// GateFinding is one thing the head adds that the policy watches. Evidence is
// the head scan's file and line for the facts in From.
type GateFinding struct {
	Rule     string          `json:"rule"`
	Level    string          `json:"level"`
	Subject  string          `json:"subject"`
	Detail   string          `json:"detail"`
	From     []string        `json:"from"`
	Evidence []fact.Evidence `json:"evidence"`
}

const policyDefault = "default"

// Policy is the level per rule a scan's config section sets: the stricter of
// the repo's and the operator's, or the rule's default (config.GateDefault)
// when neither names it. Without
// an operator entry the repo may also set off; the gate shows that as
// gate_loosened when the base was stricter.
func Policy(s *fact.Scan) []GatePolicy {
	var declared []fact.DeclaredGate
	if s.Config != nil && s.Config.Declared != nil {
		declared = s.Config.Declared.Gate
	}
	out := make([]GatePolicy, 0, len(config.GateRules))
	for _, rule := range config.GateRules {
		p := GatePolicy{Rule: rule, Level: config.GateDefault(rule), By: policyDefault}
		var repo, op *fact.DeclaredGate
		for i := range declared {
			d := &declared[i]
			if d.Rule != rule {
				continue
			}
			for _, id := range d.Licences {
				if !slices.Contains(p.Licences, id) {
					p.Licences = append(p.Licences, id)
				}
			}
			if d.By == config.ByOperator {
				op = d
			} else {
				repo = d
			}
		}
		switch {
		case op != nil && (repo == nil || config.LevelRank(op.Level) >= config.LevelRank(repo.Level)):
			p.Level, p.By, p.Line = op.Level, op.By, op.Line
		case repo != nil:
			p.Level, p.By, p.Line = repo.Level, repo.By, repo.Line
		}
		out = append(out, p)
	}
	return out
}

// Gate compares head with base. The maps are the ones derived from each scan
// with the same settings.
func Gate(base, head *fact.Scan, bm, hm *Map) *GateResult {
	r := &GateResult{Schema: GateSchemaID, Base: bm.Scan, Head: hm.Scan, RulesVersion: Version,
		Policy: Policy(head), Findings: []GateFinding{}}
	level := map[string]string{}
	for _, p := range r.Policy {
		level[p.Rule] = p.Level
	}
	headFacts := map[string]fact.Fact{}
	for _, f := range head.Facts {
		headFacts[f.Key] = f
	}
	add := func(rule, subject, detail string, from []string, ev ...fact.Evidence) {
		if level[rule] == config.LevelOff {
			return
		}
		from = uniq(append([]string(nil), from...)) // never sort the map's own slice
		if from == nil {
			from = []string{}
		}
		for _, k := range from {
			if f, ok := headFacts[k]; ok && len(f.Evidence) > 0 {
				ev = append(ev, f.Evidence[0])
			}
		}
		if ev == nil {
			ev = []fact.Evidence{}
		}
		r.Findings = append(r.Findings, GateFinding{Rule: rule, Level: level[rule], Subject: subject, Detail: detail, From: from, Evidence: ev})
	}

	gateParties(bm, hm, add)
	gateColumns(bm, hm, add)
	gateImages(bm, hm, add)
	gateStacks(bm, hm, add)
	gateDependencies(base, head, add)
	gateLicences(base, head, r.Policy, add)
	gateLoosened(base, head, r.Policy, add)

	order := map[string]int{}
	for i, rule := range config.GateRules {
		order[rule] = i
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Rule != b.Rule {
			return order[a.Rule] < order[b.Rule]
		}
		return a.Subject < b.Subject
	})
	for _, f := range r.Findings {
		if f.Level == config.LevelBlock {
			r.Blocked = true
		}
	}
	return r
}

type gateAdd func(rule, subject, detail string, from []string, ev ...fact.Evidence)

// gateParties: a catalogue party, or a host nobody claims that the code
// requests or uses as a default, that the base did not have. A mention alone
// is too weak to stop a change.
func gateParties(bm, hm *Map, add gateAdd) {
	had := map[string]bool{}
	for _, p := range bm.Parties {
		had["party:"+p.ID] = true
	}
	for _, u := range bm.Unknown {
		had["host:"+u.Host] = true
	}
	for _, p := range hm.Parties {
		if had["party:"+p.ID] {
			continue
		}
		var from []string
		for _, s := range p.Signals {
			from = append(from, s.Fact)
		}
		add(config.GateNewOutsideParty, "party:"+p.ID, fmt.Sprintf("new outside party %s (%s)", p.Name, p.Category), from)
	}
	for _, u := range hm.Unknown {
		if had["host:"+u.Host] || (u.Usage != "request" && u.Usage != "default") {
			continue
		}
		add(config.GateNewOutsideParty, "host:"+u.Host, fmt.Sprintf("new host %s that no one claims (%s)", u.Host, u.Usage), u.From)
	}
}

// gateColumns: a column to check the base did not have.
func gateColumns(bm, hm *Map, add gateAdd) {
	had := map[string]bool{}
	for _, t := range bm.Tables {
		for _, c := range t.Check {
			had[t.Table+"."+c.Column] = true
		}
	}
	for _, t := range hm.Tables {
		for _, c := range t.Check {
			if had[t.Table+"."+c.Column] {
				continue
			}
			add(config.GateNewSensitiveColumn, t.Table+"."+c.Column,
				fmt.Sprintf("new column %s.%s matches %q: check encryption and access", t.Table, c.Column, c.Word), c.From)
		}
	}
}

// gateImages: an image reference not pinned by digest that the base did not
// have unpinned. A new tag of an image repository the same subject already
// ran unpinned in the base is an upgrade, not new; a repository newly
// unpinned, or pinned by digest in the base, fires. Maps from before rules
// 0.14.0 have no image: compared as written.
func gateImages(bm, hm *Map, add gateAdd) {
	had := map[string]bool{}
	for _, f := range bm.Findings {
		if f.Rule == FindingImagePinning {
			had[f.Subject+"|"+f.Detail] = true
			if f.Image != "" {
				had[f.Subject+"|image:"+f.Image] = true
			}
		}
	}
	for _, f := range hm.Findings {
		if f.Rule != FindingImagePinning || had[f.Subject+"|"+f.Detail] || f.Image != "" && had[f.Subject+"|image:"+f.Image] {
			continue
		}
		add(config.GateNewUnpinnedImage, f.Subject, f.Detail, f.From)
	}
}

// gateStacks: a stack that is production, confirmed or not, and was not in
// the base.
func gateStacks(bm, hm *Map, add gateAdd) {
	prod := func(role string) bool { return role == RoleProduction || role == RoleUnconfirmed }
	had := map[string]bool{}
	for _, s := range bm.Stacks {
		if prod(s.Role) {
			had[s.Stack] = true
		}
	}
	for _, s := range hm.Stacks {
		if !prod(s.Role) || had[s.Stack] {
			continue
		}
		add(config.GateNewProductionStack, s.Stack, fmt.Sprintf("stack %s is now %s", strings.TrimPrefix(s.Stack, "stack:compose:"), s.Role), []string{s.Stack})
	}
}

// gateDependencies: a dependency whose fact the base did not have, direct
// (new_dependency) or transitive (new_transitive_dependency). The key holds
// the manifest or lockfile folder and the name, not the version, so an
// upgrade is not new.
func gateDependencies(base, head *fact.Scan, add gateAdd) {
	had := map[string]bool{}
	for _, f := range base.Facts {
		if f.Kind == fact.KindDependency {
			had[depSide(f)+f.Key] = true // a Go // indirect requirement that becomes direct is new
		}
	}
	for _, f := range head.Facts {
		if f.Kind != fact.KindDependency || had[depSide(f)+f.Key] {
			continue
		}
		if Transitive(f) {
			add(config.GateNewTransitive, f.Key, fmt.Sprintf("new transitive %s dependency %s %s", f.Str("ecosystem"), f.Str("name"), f.Str("version")), []string{f.Key})
			continue
		}
		add(config.GateNewDependency, f.Key, fmt.Sprintf("new %s dependency %s %s", f.Str("ecosystem"), f.Str("name"), f.Str("version")), []string{f.Key})
	}
}

func depSide(f fact.Fact) string {
	if Transitive(f) {
		return "t|"
	}
	return "d|"
}

// gateLicences: a dependency, direct or transitive, whose declared licence
// cannot be met without a licence on the list, and that the base did not
// already have with the same licence. Undeclared licences never match. The
// gate only reports what a change adds: dependencies already in the base are
// the inventory's to show, even when the list grows.
func gateLicences(base, head *fact.Scan, policy []GatePolicy, add gateAdd) {
	var deny []string
	for _, p := range policy {
		if p.Rule == config.GateLicenceNotAllowed {
			deny = p.Licences
		}
	}
	if len(deny) == 0 {
		return
	}
	had := map[string]bool{}
	for _, f := range base.Facts {
		if f.Kind == fact.KindDependency {
			had[f.Key+"|"+f.Str("license")] = true
		}
	}
	for _, f := range head.Facts {
		lic := f.Str("license")
		if f.Kind != fact.KindDependency || lic == "" || had[f.Key+"|"+lic] {
			continue
		}
		hit, ok := LicenceDenied(lic, deny)
		if !ok || len(hit) == 0 {
			continue
		}
		add(config.GateLicenceNotAllowed, f.Key, fmt.Sprintf("%s dependency %s %s declares %s (not allowed: %s)",
			f.Str("ecosystem"), f.Str("name"), f.Str("version"), lic, strings.Join(hit, ", ")), []string{f.Key})
	}
}

// gateLoosened: the head relaxes what the base had. A rule whose level went
// down, and a repo exclusion the base did not have (exclusions hide files
// from every rule).
func gateLoosened(base, head *fact.Scan, policy []GatePolicy, add gateAdd) {
	file := ""
	if head.Config != nil {
		file = head.Config.File
	}
	// The hint file line is the evidence; it is not a fact.
	ev := func(line int) []fact.Evidence {
		if file == "" || line == 0 {
			return nil
		}
		return []fact.Evidence{{Path: file, Line: line}}
	}
	was := map[string]GatePolicy{}
	for _, p := range Policy(base) {
		was[p.Rule] = p
	}
	for _, p := range policy {
		b := was[p.Rule]
		if config.LevelRank(p.Level) < config.LevelRank(b.Level) {
			add(config.GateLoosened, "gate:"+p.Rule, fmt.Sprintf("gate rule %s lowered from %s to %s", p.Rule, b.Level, p.Level), nil, ev(p.Line)...)
		}
		for _, id := range b.Licences {
			if !slices.Contains(p.Licences, id) {
				add(config.GateLoosened, "gate:"+p.Rule+":"+id, fmt.Sprintf("licence %s removed from gate rule %s", id, p.Rule), nil, ev(p.Line)...)
			}
		}
	}
	had := map[string]bool{}
	if base.Config != nil {
		for _, e := range base.Config.Exclusions {
			had[e.By+"|"+e.Pattern] = true
		}
	}
	if head.Config == nil {
		return
	}
	for _, e := range head.Config.Exclusions {
		if e.By != config.ByRepo || had[e.By+"|"+e.Pattern] {
			continue
		}
		add(config.GateLoosened, "exclude:"+e.Pattern, fmt.Sprintf("new exclusion %s hides %d files from the scan", e.Pattern, e.Files), nil, ev(e.Line)...)
	}
}
