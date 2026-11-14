// Package fact defines the scan output (schema cernly.scan/v1)
// and its canonical, deterministic encoding.
package fact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// SchemaID names the facts format in every scan.
const SchemaID = "cernly.scan/v1"

// Kinds allowed by scan.v1.schema.json.
const (
	KindDependency   = "dependency"
	KindService      = "service"
	KindDatastore    = "datastore"
	KindEnvVar       = "env_var"
	KindOutboundHost = "outbound_host"
	KindDBTable      = "db_table"
	KindDBColumn     = "db_column"
	KindSecretFound  = "secret_found"
	KindImageBuild   = "image_build"
	KindImageRecipe  = "image_recipe"
	KindStack        = "stack"
	KindAgentConfig  = "agent_config"
	KindRoute        = "route"
	// Ownership: who owns the repo, as the repo says it.
	KindOwner         = "owner"
	KindCatalogEntity = "catalog_entity"
	// Tests: test files per module, and the commands
	// that run them.
	KindTestSuite = "test_suite"
	KindTestRun   = "test_run"
	// Size: files and lines per folder, and imports between
	// the repo's own packages.
	KindSourceSize = "source_size"
	KindImport     = "import"
)

type Evidence struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type Fact struct {
	Kind     string         `json:"kind"`
	Key      string         `json:"key"`
	Attrs    map[string]any `json:"attrs"`
	Plugin   string         `json:"plugin"`
	Evidence []Evidence     `json:"evidence"`
}

type LocationError struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Plugin sources. Built-in plugins are compiled into the binary; wasm
// plugins are loaded from signed modules and carry their hash and signer.
const (
	SourceBuiltin = "builtin"
	SourceWasm    = "wasm"
)

type PluginRun struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Source  string          `json:"source"`
	SHA256  string          `json:"sha256,omitempty"`
	Signer  string          `json:"signer,omitempty"`
	Status  string          `json:"status"`
	Errors  []LocationError `json:"errors,omitempty"`
}

// ScanConfig records what was excluded from a scan and who decided it: the
// repo's hint file or the operator. Present only when either said something.
type ScanConfig struct {
	File              string          `json:"file,omitempty"`
	IgnoredByOperator bool            `json:"ignored_by_operator,omitempty"`
	Exclusions        []Exclusion     `json:"exclusions,omitempty"`
	Include           []string        `json:"include,omitempty"`
	Declared          *Declared       `json:"declared,omitempty"`
	Errors            []LocationError `json:"errors,omitempty"`
}

// Declared holds what a person declared about the repo: never a fact, always
// shown as declared with who said it. The rules read it.
type Declared struct {
	OwnDomains []DeclaredDomain `json:"own_domains,omitempty"`
	Stacks     []DeclaredStack  `json:"stacks,omitempty"`
	// Gate levels per rule, from the repo and the operator. Both sides are
	// kept: the gate takes the stricter one.
	Gate []DeclaredGate `json:"gate,omitempty"`
	// Tags on the repo: key:value labels; per key the operator's entry
	// replaces the repo's.
	Tags []DeclaredTag `json:"tags,omitempty"`
}

// DeclaredTag is one key:value label on the repo.
type DeclaredTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	By    string `json:"by"`
	Line  int    `json:"line,omitempty"`
}

// DeclaredGate is one gate rule's level as one side declared it.
type DeclaredGate struct {
	Rule  string `json:"rule"`
	Level string `json:"level"` // off, warn or block
	By    string `json:"by"`
	Line  int    `json:"line,omitempty"`
	// Licences is the licence ids licence_not_allowed watches.
	Licences []string `json:"licences,omitempty"`
}

type DeclaredDomain struct {
	Domain string `json:"domain"`
	By     string `json:"by"`
	Line   int    `json:"line,omitempty"`
}

type DeclaredStack struct {
	Stack string `json:"stack"`          // the stack's name
	Role  string `json:"role,omitempty"` // production or none
	Name  string `json:"name,omitempty"` // a display name, e.g. UAT
	By    string `json:"by"`
	Line  int    `json:"line,omitempty"`
}

type Exclusion struct {
	Pattern    string `json:"pattern"`
	Reason     string `json:"reason,omitempty"`
	By         string `json:"by"`
	Line       int    `json:"line,omitempty"`
	Files      int    `json:"files"`
	Reincluded int    `json:"reincluded,omitempty"`
}

type Commit struct {
	SHA         string `json:"sha"`
	CommittedAt string `json:"committed_at"`
	Dirty       bool   `json:"dirty,omitempty"` // checkout with uncommitted changes; never stored
}

type Extractor struct {
	Version string `json:"version"`
}

type Scan struct {
	Schema    string      `json:"schema"`
	Repo      string      `json:"repo"`
	Commit    Commit      `json:"commit"`
	Extractor Extractor   `json:"extractor"`
	Config    *ScanConfig `json:"config,omitempty"`
	Plugins   []PluginRun `json:"plugins"`
	Facts     []Fact      `json:"facts"`
}

// Normalise merges facts with the same (kind, key) and sorts everything into
// the canonical order. Two facts with the same key but different attributes are
// an extractor bug: the first one (in plugin output order) wins and the clash is
// reported, so the output never silently depends on map iteration order.
func (s *Scan) Normalise() []string {
	var clashes []string
	type id struct{ kind, key string }
	idx := map[id]int{}
	var out []Fact
	for _, f := range s.Facts {
		k := id{f.Kind, f.Key}
		if i, ok := idx[k]; ok {
			if merged, ok := mergeShared(out[i], f); ok {
				out[i].Attrs = merged
			} else if !sameAttrs(out[i].Attrs, f.Attrs) {
				clashes = append(clashes, fmt.Sprintf("%s %s: conflicting attrs, kept first", f.Kind, f.Key))
			}
			out[i].Evidence = append(out[i].Evidence, f.Evidence...)
			continue
		}
		idx[k] = len(out)
		f.Evidence = append([]Evidence(nil), f.Evidence...)
		out = append(out, f)
	}
	for i := range out {
		out[i].Evidence = dedupeEvidence(out[i].Evidence)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	s.Facts = out
	sort.Slice(s.Plugins, func(i, j int) bool { return s.Plugins[i].Name < s.Plugins[j].Name })
	for i := range s.Plugins {
		sortErrors(s.Plugins[i].Errors)
	}
	if s.Config != nil {
		sortErrors(s.Config.Errors)
	}
	sort.Strings(clashes)
	return clashes
}

// usageRank orders outbound_host usage from weakest to strongest proof.
var usageRank = map[string]int{"mention": 1, "default": 2, "request": 3}

// mergeShared merges two facts of a kind several plugins may emit for the
// same key: env_var (one per name across the repo)
// takes the union of origins and host hints;
// outbound_host keeps the strongest usage with its via and sdk. Other kinds
// have one owner.
func mergeShared(a, b Fact) (map[string]any, bool) {
	if a.Plugin == b.Plugin {
		return nil, false
	}
	switch a.Kind {
	case KindEnvVar:
		m := copyAttrs(a.Attrs)
		for _, k := range []string{"origins", "host_hints"} {
			if u := unionStrings(a.Attrs[k], b.Attrs[k]); len(u) > 0 {
				m[k] = u
			}
		}
		return m, true
	case KindOutboundHost:
		ua, _ := a.Attrs["usage"].(string)
		ub, _ := b.Attrs["usage"].(string)
		if usageRank[ub] > usageRank[ua] {
			m := copyAttrs(a.Attrs)
			for _, k := range []string{"usage", "via", "sdk"} {
				delete(m, k)
				if v, ok := b.Attrs[k]; ok {
					m[k] = v
				}
			}
			return m, true
		}
		return a.Attrs, true
	}
	return nil, false
}

func copyAttrs(a map[string]any) map[string]any {
	m := make(map[string]any, len(a))
	for k, v := range a {
		m[k] = v
	}
	return m
}

// unionStrings merges two string lists ([]string or []any from JSON) into a
// sorted, unique []any.
func unionStrings(a, b any) []any {
	seen := map[string]bool{}
	for _, v := range []any{a, b} {
		switch xs := v.(type) {
		case []string:
			for _, x := range xs {
				seen[x] = true
			}
		case []any:
			for _, x := range xs {
				if s, ok := x.(string); ok {
					seen[s] = true
				}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

func sortErrors(errs []LocationError) {
	sort.Slice(errs, func(a, b int) bool {
		if errs[a].Path != errs[b].Path {
			return errs[a].Path < errs[b].Path
		}
		if errs[a].Line != errs[b].Line {
			return errs[a].Line < errs[b].Line
		}
		return errs[a].Message < errs[b].Message
	})
}

// dedupeEvidence sorts on every field, so the order is total and equal
// entries are adjacent, then drops the repeats.
func dedupeEvidence(ev []Evidence) []Evidence {
	sort.Slice(ev, func(i, j int) bool {
		a, b := ev[i], ev[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.EndLine != b.EndLine {
			return a.EndLine < b.EndLine
		}
		return a.Unit < b.Unit
	})
	out := ev[:0]
	for _, e := range ev {
		if len(out) > 0 && e == out[len(out)-1] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func sameAttrs(a, b map[string]any) bool {
	x, _ := Canonical(a)
	y, _ := Canonical(b)
	return bytes.Equal(x, y)
}

// Canonical encodes v as canonical JSON: object keys sorted, no insignificant
// whitespace, no HTML escaping. Values in this schema are strings, booleans,
// integers, arrays and objects only, which keeps this equivalent to RFC 8785.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil { // maps encode with sorted keys
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
