// Package config reads the repo-side hint file and combines it with the
// operator's settings. The operator always wins, and every exclusion, from
// either side, is recorded in the scan output so a reader can see what was
// left out and who decided it.
package config

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
)

// FileName is the repo-side hint file, read only when tracked by git.
const FileName = ".cernly.yml"

// HintFile picks the hint file a commit uses: FileName when tracked, else the
// first tracked name in fallbacks (further file names a caller may read, in
// order, when .cernly.yml is absent; see Operator.HintFallbacks). ignored lists
// the tracked fallbacks the chosen file overrides.
func HintFile(has func(string) bool, fallbacks []string) (name string, ignored []string) {
	for _, n := range append([]string{FileName}, fallbacks...) {
		if n == "" || n == name || !has(n) {
			continue
		}
		if name == "" {
			name = n
		} else {
			ignored = append(ignored, n)
		}
	}
	return name, ignored
}

const (
	ByRepo     = "repo"
	ByOperator = "operator"
)

// Rule is one exclusion pattern and where it came from.
type Rule struct {
	Pattern string
	Reason  string
	By      string
	Line    int // line in the hint file; 0 for operator rules
}

// Repo is the parsed hint file (or the operator's file, same shape).
type Repo struct {
	Rules   []Rule
	Domains []fact.DeclaredDomain
	Stacks  []fact.DeclaredStack
	Gate    []fact.DeclaredGate
	Tags    []fact.DeclaredTag
	Errors  []fact.LocationError
}

// Gate rules and levels. The rules package applies them; the names live here
// so the hint file can be checked when it is read.
const (
	GateNewOutsideParty      = "new_outside_party"
	GateNewSensitiveColumn   = "new_sensitive_column"
	GateNewUnpinnedImage     = "new_unpinned_image"
	GateNewProductionStack   = "new_production_stack"
	GateNewDependency        = "new_dependency"
	GateNewTransitive        = "new_transitive_dependency"
	GateLicenceNotAllowed    = "licence_not_allowed"
	GateNewAnonymousEndpoint = "new_anonymous_public_endpoint"
	GateLoosened             = "gate_loosened"

	LevelOff   = "off"
	LevelWarn  = "warn"
	LevelBlock = "block"
)

// GateRules lists every gate rule, in report order.
var GateRules = []string{GateNewOutsideParty, GateNewSensitiveColumn, GateNewUnpinnedImage,
	GateNewProductionStack, GateNewDependency, GateNewTransitive, GateLicenceNotAllowed, GateNewAnonymousEndpoint, GateLoosened}

// GateDefault is a rule's level when no side names it: warn, except
// new_transitive_dependency, which is off (one direct dependency can bring
// dozens of transitive ones).
func GateDefault(rule string) string {
	if rule == GateNewTransitive {
		return LevelOff
	}
	return LevelWarn
}

// LevelRank orders the levels: a higher rank is stricter.
func LevelRank(level string) int {
	switch level {
	case LevelOff:
		return 0
	case LevelWarn:
		return 1
	case LevelBlock:
		return 2
	}
	return -1
}

// ParseRepo reads the hint file. Problems never stop a scan: they are
// returned as errors and the offending entry is skipped.
//
//	version: 1
//	exclude:
//	  - path: cmd/tool/testdata
//	    reason: test fixtures
//	  - examples/**
//	domains:
//	  own: [acme-widgets.be, acme-labs.be]   # and their subdomains
//	stacks:
//	  default:
//	    role: production                      # or none
//	    name: UAT                             # optional display name
//	gate:                                     # default warn
//	  new_outside_party: block                # off, warn or block
//	  licence_not_allowed:                    # a level and a list
//	    level: block
//	    licences: [AGPL-3.0-only, GPL-3.0-only]
//	tags:                                     # key: value
//	  team: payments
//	  product: webshop
func ParseRepo(data []byte) Repo { return parse(data, ByRepo, FileName) }

// ParseRepoFile reads the hint file under the name the commit uses.
func ParseRepoFile(data []byte, name string) Repo { return parse(data, ByRepo, name) }

// ParseOperator reads the operator's file, same keys as the hint file.
func ParseOperator(data []byte, name string) Repo { return parse(data, ByOperator, name) }

func parse(data []byte, by, file string) Repo {
	var out Repo
	fail := func(line int, format string, args ...any) {
		out.Errors = append(out.Errors, fact.LocationError{Path: file, Line: line, Message: fmt.Sprintf(format, args...)})
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		fail(0, "invalid YAML: %v", err)
		return out
	}
	if len(doc.Content) == 0 {
		return out
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		fail(root.Line, "top level must be a mapping")
		return out
	}
	var excludeNode, domainsNode, stacksNode, gateNode, tagsNode *yaml.Node
	version := ""
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "version":
			version = v.Value
		case "exclude":
			excludeNode = v
		case "domains":
			domainsNode = v
		case "stacks":
			stacksNode = v
		case "gate":
			gateNode = v
		case "tags":
			tagsNode = v
		default:
			fail(k.Line, "unknown key %q, ignored", k.Value)
		}
	}
	if version != "1" {
		fail(root.Line, "version must be 1, got %q; file ignored", version)
		return out
	}
	parseDeclared(domainsNode, stacksNode, by, fail, &out)
	parseGate(gateNode, by, fail, &out)
	parseTags(tagsNode, by, fail, &out)
	if excludeNode == nil {
		return out
	}
	if excludeNode.Kind != yaml.SequenceNode {
		fail(excludeNode.Line, "exclude must be a list")
		return out
	}
	for _, item := range excludeNode.Content {
		r := Rule{By: by, Line: item.Line}
		switch item.Kind {
		case yaml.ScalarNode:
			r.Pattern = item.Value
		case yaml.MappingNode:
			for i := 0; i+1 < len(item.Content); i += 2 {
				k, v := item.Content[i], item.Content[i+1]
				switch k.Value {
				case "path":
					r.Pattern = v.Value
				case "reason":
					r.Reason = v.Value
				default:
					fail(k.Line, "unknown key %q in exclude entry, ignored", k.Value)
				}
			}
		default:
			fail(item.Line, "exclude entry must be a path or a mapping")
			continue
		}
		if err := ValidPattern(r.Pattern); err != nil {
			fail(item.Line, "%v; entry ignored", err)
			continue
		}
		out.Rules = append(out.Rules, r)
	}
	return out
}

// parseDeclared reads domains.own and stacks.
func parseDeclared(domains, stacks *yaml.Node, by string, fail func(int, string, ...any), out *Repo) {
	if domains != nil {
		if domains.Kind != yaml.MappingNode {
			fail(domains.Line, "domains must be a mapping")
		} else {
			for i := 0; i+1 < len(domains.Content); i += 2 {
				k, v := domains.Content[i], domains.Content[i+1]
				if k.Value != "own" {
					fail(k.Line, "unknown key %q in domains, ignored", k.Value)
					continue
				}
				if v.Kind != yaml.SequenceNode {
					fail(v.Line, "domains.own must be a list")
					continue
				}
				for _, d := range v.Content {
					name := strings.ToLower(strings.TrimSpace(d.Value))
					if d.Kind != yaml.ScalarNode || !strings.Contains(name, ".") || strings.ContainsAny(name, "/: ") {
						fail(d.Line, "domains.own entry %q is not a domain name; ignored", d.Value)
						continue
					}
					out.Domains = append(out.Domains, fact.DeclaredDomain{Domain: name, By: by, Line: d.Line})
				}
			}
		}
	}
	if stacks == nil {
		return
	}
	if stacks.Kind != yaml.MappingNode {
		fail(stacks.Line, "stacks must be a mapping of stack name to settings")
		return
	}
	for i := 0; i+1 < len(stacks.Content); i += 2 {
		k, v := stacks.Content[i], stacks.Content[i+1]
		st := fact.DeclaredStack{Stack: k.Value, By: by, Line: k.Line}
		if v.Kind != yaml.MappingNode {
			fail(v.Line, "stacks.%s must be a mapping", k.Value)
			continue
		}
		for j := 0; j+1 < len(v.Content); j += 2 {
			kk, vv := v.Content[j], v.Content[j+1]
			switch kk.Value {
			case "role":
				if vv.Value != "production" && vv.Value != "none" {
					fail(vv.Line, "stacks.%s.role must be production or none; ignored", k.Value)
					continue
				}
				st.Role = vv.Value
			case "name":
				st.Name = vv.Value
			default:
				fail(kk.Line, "unknown key %q in stacks.%s, ignored", kk.Value, k.Value)
			}
		}
		if st.Role != "" || st.Name != "" {
			out.Stacks = append(out.Stacks, st)
		}
	}
}

// parseGate reads gate: a mapping of rule name to level. licence_not_allowed
// also takes a mapping with level and licences, the licence ids it watches.
func parseGate(n *yaml.Node, by string, fail func(int, string, ...any), out *Repo) {
	if n == nil {
		return
	}
	if n.Kind != yaml.MappingNode {
		fail(n.Line, "gate must be a mapping of rule to level")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !slices.Contains(GateRules, k.Value) {
			fail(k.Line, "unknown gate rule %q, ignored", k.Value)
			continue
		}
		g := fact.DeclaredGate{Rule: k.Value, By: by, Line: k.Line}
		if k.Value == GateLicenceNotAllowed && v.Kind == yaml.MappingNode {
			if !parseLicenceGate(v, fail, &g) {
				continue
			}
			out.Gate = append(out.Gate, g)
			continue
		}
		if v.Kind != yaml.ScalarNode || LevelRank(v.Value) < 0 {
			fail(v.Line, "gate.%s must be off, warn or block; ignored", k.Value)
			continue
		}
		g.Level = v.Value
		out.Gate = append(out.Gate, g)
	}
}

// TagKey is the form of a tag key: lower case, starting with a letter.
var TagKey = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)

// ValidTag checks a tag; the value is trimmed by the caller.
func ValidTag(key, value string) error {
	if !TagKey.MatchString(key) {
		return fmt.Errorf("tag key %q must be lower case letters, digits, '.', '-' or '_', starting with a letter, at most 63", key)
	}
	if value == "" || len([]rune(value)) > 200 || strings.ContainsAny(value, "\n\r\t") {
		return fmt.Errorf("tag %s needs a value of one line, at most 200 characters", key)
	}
	return nil
}

// parseTags reads tags: a mapping of key to value, or a list of key:value
// strings. A key named twice keeps the last one.
func parseTags(n *yaml.Node, by string, fail func(int, string, ...any), out *Repo) {
	if n == nil {
		return
	}
	add := func(key, value string, line int) {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if err := ValidTag(key, value); err != nil {
			fail(line, "%v; ignored", err)
			return
		}
		out.Tags = append(out.Tags, fact.DeclaredTag{Key: key, Value: value, By: by, Line: line})
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind != yaml.ScalarNode {
				fail(v.Line, "tags.%s must be a single value", k.Value)
				continue
			}
			add(k.Value, v.Value, k.Line)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			// "- team: payments" is a one-pair mapping in YAML: read it as meant.
			if item.Kind == yaml.MappingNode && len(item.Content) == 2 && item.Content[1].Kind == yaml.ScalarNode {
				add(item.Content[0].Value, item.Content[1].Value, item.Line)
				continue
			}
			key, value, ok := strings.Cut(item.Value, ":")
			if item.Kind != yaml.ScalarNode || !ok {
				fail(item.Line, "tags entry %q must be key:value; ignored", item.Value)
				continue
			}
			add(key, value, item.Line)
		}
	default:
		fail(n.Line, "tags must be a mapping of key to value")
	}
}

// parseLicenceGate reads licence_not_allowed: {level, licences}. The level
// defaults to warn; licence ids are kept as written, one entry per id.
func parseLicenceGate(v *yaml.Node, fail func(int, string, ...any), g *fact.DeclaredGate) bool {
	g.Level = LevelWarn
	for j := 0; j+1 < len(v.Content); j += 2 {
		kk, vv := v.Content[j], v.Content[j+1]
		switch kk.Value {
		case "level":
			if vv.Kind != yaml.ScalarNode || LevelRank(vv.Value) < 0 {
				fail(vv.Line, "gate.%s.level must be off, warn or block; ignored", GateLicenceNotAllowed)
				return false
			}
			g.Level = vv.Value
		case "licences":
			if vv.Kind != yaml.SequenceNode {
				fail(vv.Line, "gate.%s.licences must be a list of licence ids", GateLicenceNotAllowed)
				return false
			}
			for _, x := range vv.Content {
				id := strings.TrimSpace(x.Value)
				if x.Kind != yaml.ScalarNode || id == "" || strings.ContainsAny(id, " ()") {
					fail(x.Line, "gate.%s.licences: %q is not a licence id, ignored", GateLicenceNotAllowed, x.Value)
					continue
				}
				if !slices.Contains(g.Licences, id) {
					g.Licences = append(g.Licences, id)
				}
			}
		default:
			fail(kk.Line, "unknown key gate.%s.%s, ignored", GateLicenceNotAllowed, kk.Value)
		}
	}
	return true
}

// maxPatternSegs bounds a pattern's length; no sensible exclusion is deeper.
const maxPatternSegs = 64

// ValidPattern accepts repo-relative glob patterns with forward slashes.
// `**` matches any number of folders; other segments use path.Match syntax.
func ValidPattern(p string) error {
	if p == "" {
		return fmt.Errorf("empty pattern")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("pattern %q must be repo-relative with forward slashes", p)
	}
	segs := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(segs) > maxPatternSegs {
		return fmt.Errorf("pattern %q has more than %d segments", p, maxPatternSegs)
	}
	for _, seg := range segs {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("pattern %q has an empty, . or .. segment", p)
		}
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("pattern %q: %v", p, err)
		}
	}
	return nil
}

// Matches reports whether a pattern covers a repo-relative file: the pattern
// matches the file itself or one of its parent folders, so `examples` covers
// everything under examples/.
//
// Dynamic programming over (pattern segment, name segment): row i, column j
// says whether p[:i] matches n[:j]. Linear in each, however many `**`.
func Matches(pattern, file string) bool {
	p := strings.Split(strings.TrimSuffix(pattern, "/"), "/")
	n := strings.Split(file, "/")
	prev := make([]bool, len(n)+1)
	cur := make([]bool, len(n)+1)
	prev[0] = true
	for _, seg := range p {
		cur[0] = seg == "**" && prev[0]
		for j := 1; j <= len(n); j++ {
			if seg == "**" {
				cur[j] = prev[j] || cur[j-1]
				continue
			}
			cur[j] = false
			if prev[j-1] {
				ok, err := path.Match(seg, n[j-1])
				cur[j] = err == nil && ok
			}
		}
		prev, cur = cur, prev
	}
	for _, ok := range prev[1:] {
		if ok {
			return true
		}
	}
	return false
}

// Operator holds the scanner-side settings. They override the repo file.
type Operator struct {
	Exclude      []string // extra exclusions
	Include      []string // re-include paths a repo rule excluded
	NoRepoConfig bool     // ignore the repo file entirely (recorded)
	File         []byte   // the operator's configuration file (--config), same keys as the hint file
	FileName     string
	// HintFallbacks are further file names a caller may read, in order, when
	// .cernly.yml is absent. Not a command-line flag.
	HintFallbacks []string
}

// Plan is the combined decision for one scan.
type Plan struct {
	Record fact.ScanConfig
	rules  []Rule
	incl   []string
	hints  []string // hint file names that always stay visible
}

// Build combines repo rules and operator settings. repoData is nil when the
// hint file is not tracked.
func Build(repoData []byte, op Operator) (Plan, error) { return BuildFrom(FileName, repoData, op) }

// BuildFrom is Build for a hint file under the given name (see HintFile).
func BuildFrom(repoFile string, repoData []byte, op Operator) (Plan, error) {
	pl := Plan{hints: append([]string{FileName}, op.HintFallbacks...)}
	for _, p := range append(append([]string(nil), op.Exclude...), op.Include...) {
		if err := ValidPattern(p); err != nil {
			return pl, fmt.Errorf("operator: %v", err)
		}
	}
	var declared [2]Repo // repo, operator
	if repoData != nil {
		pl.Record.File = repoFile
		if op.NoRepoConfig {
			pl.Record.IgnoredByOperator = true
		} else {
			rc := ParseRepoFile(repoData, repoFile)
			pl.rules = append(pl.rules, rc.Rules...)
			pl.Record.Errors = rc.Errors
			declared[0] = rc
		}
	}
	if op.File != nil {
		// The operator's own file must be right: a skipped entry would
		// silently change what the scan covers. Only the repo's file is
		// forgiven (recorded, not fatal).
		oc := ParseOperator(op.File, op.FileName)
		if len(oc.Errors) > 0 {
			var msgs []string
			for _, e := range oc.Errors {
				msgs = append(msgs, fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message))
			}
			return pl, fmt.Errorf("operator config: %s", strings.Join(msgs, "; "))
		}
		pl.rules = append(pl.rules, oc.Rules...)
		declared[1] = oc
	}
	pl.Record.Declared = merge(declared[0], declared[1])
	for _, p := range op.Exclude {
		pl.rules = append(pl.rules, Rule{Pattern: p, By: ByOperator})
	}
	pl.incl = op.Include
	return pl, nil
}

// merge combines declarations: own domains from both sides (sorted, one
// entry per domain, the operator's when both name it); per stack the
// operator's entry replaces the repo's; gate levels from both sides, since the
// gate takes the stricter; per tag key the operator's entry replaces the repo's. Nil when nothing was declared.
func merge(repo, op Repo) *fact.Declared {
	d := &fact.Declared{}
	domains := map[string]fact.DeclaredDomain{}
	for _, x := range append(append([]fact.DeclaredDomain(nil), repo.Domains...), op.Domains...) {
		domains[x.Domain] = x
	}
	for _, x := range domains {
		d.OwnDomains = append(d.OwnDomains, x)
	}
	sort.Slice(d.OwnDomains, func(i, j int) bool { return d.OwnDomains[i].Domain < d.OwnDomains[j].Domain })
	stacks := map[string]fact.DeclaredStack{}
	for _, x := range append(append([]fact.DeclaredStack(nil), repo.Stacks...), op.Stacks...) {
		stacks[x.Stack] = x
	}
	for _, x := range stacks {
		d.Stacks = append(d.Stacks, x)
	}
	sort.Slice(d.Stacks, func(i, j int) bool { return d.Stacks[i].Stack < d.Stacks[j].Stack })
	// Gate levels: both sides kept, one entry per rule and side (the last
	// one when a file names a rule twice), repo before operator.
	gate := map[string]fact.DeclaredGate{}
	for _, x := range append(append([]fact.DeclaredGate(nil), repo.Gate...), op.Gate...) {
		gate[x.Rule+"|"+x.By] = x
	}
	for _, x := range gate {
		d.Gate = append(d.Gate, x)
	}
	sort.Slice(d.Gate, func(i, j int) bool {
		if d.Gate[i].Rule != d.Gate[j].Rule {
			return d.Gate[i].Rule < d.Gate[j].Rule
		}
		return d.Gate[i].By > d.Gate[j].By // repo before operator
	})
	// Tags: per key the operator's entry replaces the repo's.
	tags := map[string]fact.DeclaredTag{}
	for _, x := range append(append([]fact.DeclaredTag(nil), repo.Tags...), op.Tags...) {
		tags[x.Key] = x
	}
	for _, x := range tags {
		d.Tags = append(d.Tags, x)
	}
	sort.Slice(d.Tags, func(i, j int) bool { return d.Tags[i].Key < d.Tags[j].Key })
	if len(d.OwnDomains) == 0 && len(d.Stacks) == 0 && len(d.Gate) == 0 && len(d.Tags) == 0 {
		return nil
	}
	return d
}

// Empty reports whether the plan has nothing to record.
func (pl *Plan) Empty() bool {
	r := pl.Record
	return r.File == "" && len(pl.rules) == 0 && len(pl.incl) == 0 && r.Declared == nil && len(r.Errors) == 0
}

// Apply decides which of the tracked files stay visible and fills the record
// with, per rule, how many files it removed. The hint file itself always
// stays visible.
func (pl *Plan) Apply(files []string) (keep []string) {
	removed := make([]int, len(pl.rules))
	reincluded := make([]int, len(pl.rules))
	for _, f := range files {
		if slices.Contains(pl.hints, f) {
			keep = append(keep, f)
			continue
		}
		out := false
		for i, r := range pl.rules {
			if !Matches(r.Pattern, f) {
				continue
			}
			if r.By == ByRepo && pl.included(f) {
				reincluded[i]++
				continue
			}
			removed[i]++
			out = true
		}
		if !out {
			keep = append(keep, f)
		}
	}
	pl.Record.Exclusions = nil
	for i, r := range pl.rules {
		pl.Record.Exclusions = append(pl.Record.Exclusions, fact.Exclusion{
			Pattern: r.Pattern, Reason: r.Reason, By: r.By, Line: r.Line,
			Files: removed[i], Reincluded: reincluded[i],
		})
	}
	pl.Record.Include = append([]string(nil), pl.incl...)
	return keep
}

func (pl *Plan) included(f string) bool {
	for _, p := range pl.incl {
		if Matches(p, f) {
			return true
		}
	}
	return false
}
