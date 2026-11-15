// Package redact makes the copy of a scan that can leave the customer's
// machine: file paths, names, hosts and the repo id become stable tokens
// (path-1, name-1, host-1, repo-1), and the token list stays behind. It
// denies by default: every string is tokenized unless it is listed as the
// scanner's own vocabulary. Numbers, booleans, line numbers, the commit and
// the fact kinds stay. The same input always gives the same output.
package redact

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
)

// SchemaID names the token list's format.
const SchemaID = "cernly.redaction/v1"

// Token categories; the token is the category, a dash and a number from 1.
const (
	catHost = "host"
	catName = "name"
	catPath = "path"
	catRepo = "repo"
)

var categories = []string{catHost, catName, catPath, catRepo}

// Token is one redacted value and what it stands for.
type Token struct {
	Token string `json:"token"`
	Value string `json:"value"`
}

// Map is the token list, sorted by category and then number. It never
// leaves the customer.
type Map struct {
	Schema string  `json:"schema"`
	Tokens []Token `json:"tokens"`
}

// Counts is the number of distinct redacted values per category.
func (m Map) Counts() map[string]int {
	out := make(map[string]int, len(categories))
	for _, c := range categories {
		out[c] = 0
	}
	for _, t := range m.Tokens {
		c, _, _ := strings.Cut(t.Token, "-")
		out[c]++
	}
	return out
}

// How an attribute is handled, chosen by its key name at any depth.
const (
	classVocab = iota
	classPath
	classHost
	className
)

// vocabulary lists the attribute keys whose string values are the scanner's
// own words (enums and short identifiers of schema/scan.v1.schema.json),
// kept as they are. TestVocabularyFollowsSchema fails when the schema
// restricts another attribute this list lacks.
var vocabulary = setOf(
	"kind", "type", "engine", "language", "framework", "method", "scope",
	"ecosystem", "tool", "usage", "action", "match_type", "state", "pinning",
	"scheme", "version", "normalised", "lifecycle", "dialect", "provider",
	"item", "origins", "role", "markers", "native_type", "events", "status",
)

// restricted keys are vocabulary only for the listed values: the same key
// carries free text in other facts (via names an SDK package in a datastore
// fact; source is a file in a route fact). Any other value falls to the class
// given.
var restricted = map[string]struct {
	values map[string]bool
	other  int
}{
	"via":    {setOf("literal_url", "sdk", "env_var"), className},
	"source": {setOf("builtin", "wasm", "codeowners", "convention", "invocation"), classPath},
}

// pathKeys are attributes whose strings are file or URL paths, tokenized
// per segment. target and source inside mounts are handled in classOf.
var pathKeys = setOf(
	"file", "files", "dir", "dirs", "dockerfile", "lockfile", "roots", "from",
	"to", "from_dir", "to_dir", "path", "paths", "sources", "contexts",
	"build_context", "env_files", "patterns", "app_unit",
)

// hostKeys are attributes whose strings are hosts, with an optional port.
var hostKeys = setOf("host", "host_hints", "address", "registry")

// wellKnown are file and folder names that reveal nothing private; a path
// segment with one of these names stays readable.
var wellKnown = setOf(
	"Dockerfile", "Containerfile", "Makefile", "Caddyfile", "CODEOWNERS",
	"go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml",
	"yarn.lock", "pom.xml", "build.gradle", "Chart.yaml", "values.yaml",
	"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml",
	"catalog-info.yaml", ".cernly.yml", ".github", "workflows",
)

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var (
	wholeURL = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S+$`)
	hostPort = regexp.MustCompile(`^([^:]+):([0-9]+)$`)
	extRe    = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
)

type redactor struct {
	ids  map[string]map[string]int // category -> value -> number
	vals map[string][]string       // category -> values by number
	// The fact being walked, for the one attribute whose meaning depends on it.
	enumColumn bool
}

func newRedactor() *redactor {
	r := &redactor{ids: map[string]map[string]int{}, vals: map[string][]string{}}
	for _, c := range categories {
		r.ids[c] = map[string]int{}
	}
	return r
}

// token returns the value's token in a category, numbering it on first use.
// An empty value stays empty.
func (r *redactor) token(cat, v string) string {
	if v == "" {
		return ""
	}
	n, ok := r.ids[cat][v]
	if !ok {
		r.vals[cat] = append(r.vals[cat], v)
		n = len(r.vals[cat])
		r.ids[cat][v] = n
	}
	return cat + "-" + strconv.Itoa(n)
}

// Scan returns a redacted deep copy of s and the token list. The input is
// not changed.
func Scan(s fact.Scan) (fact.Scan, Map) {
	r := newRedactor()
	out := fact.Scan{
		Schema:    s.Schema,
		Repo:      r.token(catRepo, s.Repo),
		Commit:    s.Commit,
		Extractor: s.Extractor,
		Config:    r.config(s.Config),
	}
	if s.Plugins != nil {
		out.Plugins = make([]fact.PluginRun, len(s.Plugins))
		for i, p := range s.Plugins {
			p.Errors = r.errors(p.Errors)
			out.Plugins[i] = p
		}
	}
	if s.Facts != nil {
		out.Facts = make([]fact.Fact, len(s.Facts))
		for i, f := range s.Facts {
			out.Facts[i] = r.fact(f)
		}
	}
	return out, r.tokens()
}

func (r *redactor) tokens() Map {
	m := Map{Schema: SchemaID, Tokens: []Token{}}
	for _, c := range categories {
		for i, v := range r.vals[c] {
			m.Tokens = append(m.Tokens, Token{Token: c + "-" + strconv.Itoa(i+1), Value: v})
		}
	}
	return m
}

func (r *redactor) errors(in []fact.LocationError) []fact.LocationError {
	if in == nil {
		return nil
	}
	out := make([]fact.LocationError, len(in))
	for i, e := range in {
		out[i] = fact.LocationError{Path: r.pathText(e.Path), Line: e.Line, Message: r.name(e.Message)}
	}
	return out
}

func (r *redactor) config(c *fact.ScanConfig) *fact.ScanConfig {
	if c == nil {
		return nil
	}
	out := &fact.ScanConfig{
		File:              r.pathText(c.File),
		IgnoredByOperator: c.IgnoredByOperator,
	}
	for _, e := range c.Exclusions {
		e.Pattern = r.pathText(e.Pattern)
		e.Reason = r.name(e.Reason)
		out.Exclusions = append(out.Exclusions, e)
	}
	for _, p := range c.Include {
		out.Include = append(out.Include, r.pathText(p))
	}
	if d := c.Declared; d != nil {
		nd := &fact.Declared{}
		for _, x := range d.OwnDomains {
			x.Domain = r.name(x.Domain)
			nd.OwnDomains = append(nd.OwnDomains, x)
		}
		for _, x := range d.Stacks {
			x.Stack = r.name(x.Stack)
			x.Name = r.name(x.Name)
			nd.Stacks = append(nd.Stacks, x)
		}
		nd.Gate = append(nd.Gate, d.Gate...)
		for _, x := range d.Tags {
			x.Value = r.name(x.Value)
			nd.Tags = append(nd.Tags, x)
		}
		out.Declared = nd
	}
	out.Errors = r.errors(c.Errors)
	return out
}

func (r *redactor) fact(f fact.Fact) fact.Fact {
	r.enumColumn = f.Kind == fact.KindDBColumn && f.Attrs["enum"] == true
	out := fact.Fact{Kind: f.Kind, Plugin: f.Plugin}
	if rest, ok := strings.CutPrefix(f.Key, f.Kind+":"); ok {
		out.Key = f.Kind + ":" + r.name(rest)
	} else {
		out.Key = r.name(f.Key)
	}
	if f.Attrs != nil {
		out.Attrs = r.object("", f.Attrs)
	}
	if f.Evidence != nil {
		out.Evidence = make([]fact.Evidence, len(f.Evidence))
		for i, e := range f.Evidence {
			out.Evidence[i] = fact.Evidence{Path: r.pathText(e.Path), Line: e.Line, EndLine: e.EndLine, Unit: r.pathText(e.Unit)}
		}
	}
	return out
}

// object copies a map with its keys in sorted order, so that tokens are
// numbered the same way every time; parent is the key the map sits under.
func (r *redactor) object(parent string, m map[string]any) map[string]any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(m))
	for _, k := range keys {
		out[k] = r.value(parent, k, m[k])
	}
	return out
}

// value copies one attribute value. Lists take the key of the attribute they
// belong to; maps pass their key on as the parent of their own.
func (r *redactor) value(parent, key string, v any) any {
	switch x := v.(type) {
	case string:
		return r.str(parent, key, x)
	case map[string]any:
		return r.object(key, x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.value(parent, key, e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.str(parent, key, e)
		}
		return out
	}
	return v // numbers, booleans, null
}

func (r *redactor) classOf(parent, key, s string) int {
	if parent == "mounts" && (key == "target" || key == "source") {
		return classPath
	}
	if rs, ok := restricted[key]; ok {
		if rs.values[s] {
			return classVocab
		}
		return rs.other
	}
	switch {
	case key == "type" && r.enumColumn:
		return className // an enum's own name
	case pathKeys[key]:
		return classPath
	case hostKeys[key]:
		return classHost
	case vocabulary[key]:
		// A vocabulary word has no slash; one that does is free text.
		if strings.ContainsAny(s, `/\`) {
			return className
		}
		return classVocab
	}
	return className
}

func (r *redactor) str(parent, key, s string) string {
	if s == "" {
		return s
	}
	class := r.classOf(parent, key, s)
	if class != classVocab {
		if out, ok := r.url(s); ok {
			return out
		}
	}
	switch class {
	case classVocab:
		return s
	case classPath:
		return r.pathText(s)
	case classHost:
		return r.hostText(s)
	}
	return r.name(s)
}

func (r *redactor) name(s string) string { return r.token(catName, s) }

// url redacts a string that is one URL with a host: the scheme and the port
// stay, the host and every path segment become tokens, the query and the
// fragment each become one name token, and any user name and password are
// dropped.
func (r *redactor) url(s string) (string, bool) {
	if !wholeURL.MatchString(s) {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	out := u.Scheme + "://" + r.token(catHost, u.Hostname())
	if p := u.Port(); p != "" {
		out += ":" + p
	}
	out += r.pathText(u.Path)
	if u.RawQuery != "" {
		out += "?" + r.name(u.RawQuery)
	}
	if u.Fragment != "" {
		out += "#" + r.name(u.Fragment)
	}
	return out, true
}

// hostText redacts a host with an optional port, or a URL.
func (r *redactor) hostText(s string) string {
	if out, ok := r.url(s); ok {
		return out
	}
	if m := hostPort.FindStringSubmatch(s); m != nil {
		return r.token(catHost, m[1]) + ":" + m[2]
	}
	return r.token(catHost, s)
}

// pathText redacts a path (or a URL) segment by segment. Empty segments,
// ".", "..", "*" and "**" stay, and so do well-known file names. In the last
// segment a short lower-case extension stays and only the stem is replaced.
func (r *redactor) pathText(s string) string {
	if s == "" {
		return s
	}
	if out, ok := r.url(s); ok {
		return out
	}
	segs := strings.Split(s, "/")
	for i, seg := range segs {
		segs[i] = r.segment(seg, i == len(segs)-1)
	}
	return strings.Join(segs, "/")
}

func (r *redactor) segment(seg string, last bool) string {
	switch seg {
	case "", ".", "..", "*", "**":
		return seg
	}
	if wellKnown[seg] {
		return seg
	}
	if last {
		if i := strings.LastIndex(seg, "."); i > 0 && extRe.MatchString(seg[i:]) {
			stem := seg[:i]
			if strings.Trim(stem, "*?") == "" {
				return seg
			}
			return r.token(catPath, stem) + seg[i:]
		}
	}
	return r.token(catPath, seg)
}
