// Package mcp serves one scan to AI agents over the Model Context Protocol.
// The tools are read-only and answer from the facts file and the map
// derived from it, each fact with its file and line evidence. Nothing here
// adds structure: an agent reads what the scanner found, never a guess. See
// https://cernly.dev/how/mcp/.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// Source is what the server answers from: one scan and its map.
type Source struct {
	Scan *fact.Scan
	Map  *rules.Map
}

const (
	defaultLimit = 50
	maxLimit     = 500
)

// Sections of the map the get_map tool serves, in the map's own order.
// metrics and modules are objects in the map, so each is served as one item;
// their lists are served as sections of their own too, so they can be paged.
var sections = []string{"stacks", "links", "images", "ci_images", "attached", "parties", "internal", "unknown", "own", "routes", "ai_tools", "findings",
	"tables", "tests", "confirmations", "dismissed", "metrics", "modules",
	"metrics_services", "module_packages", "module_edges", "module_cycles"}

const instructions = `Architecture facts for one commit of one repository, extracted deterministically from its git-tracked files.
Every fact carries evidence: the file and line it was read from. Cite that evidence when you answer.
Start with summary, then find_facts or get_map; get_fact returns one fact by kind and key.
The facts are what the files say, not what runs: an unreadable file is reported in summary, never guessed.`

// NewServer returns an MCP server with the read-only tools over src.
func NewServer(src *Source, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "cernly", Version: version}, &sdk.ServerOptions{Instructions: instructions})
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(bool)}
	sdk.AddTool(s, &sdk.Tool{Name: "summary", Annotations: readOnly,
		Description: "What was scanned: repo, commit, fact counts per kind, plugins that did not finish, exclusions, stacks with their production role, and the size of each map section."},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, Summary, error) {
			return nil, summarise(src), nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "find_facts", Annotations: readOnly,
		Description: "Search the facts. Filter by kind, by text in the key or attributes, and by evidence path prefix; results are in canonical order and paged."},
		func(_ context.Context, _ *sdk.CallToolRequest, in FindInput) (*sdk.CallToolResult, FindOutput, error) {
			out, err := find(src, in)
			return nil, out, err
		})
	sdk.AddTool(s, &sdk.Tool{Name: "get_fact", Annotations: readOnly,
		Description: "One fact by kind and key, with all its attributes and evidence."},
		func(_ context.Context, _ *sdk.CallToolRequest, in GetInput) (*sdk.CallToolResult, GetOutput, error) {
			for _, f := range src.Scan.Facts {
				if f.Kind == in.Kind && f.Key == in.Key {
					return nil, GetOutput{Fact: f}, nil
				}
			}
			return nil, GetOutput{}, fmt.Errorf("no %s fact with key %q", in.Kind, in.Key)
		})
	sdk.AddTool(s, &sdk.Tool{Name: "get_map", Annotations: readOnly,
		Description: "One section of the map derived from the facts: " + strings.Join(sections, ", ") + ". Optionally filtered by text; paged."},
		func(_ context.Context, _ *sdk.CallToolRequest, in MapInput) (*sdk.CallToolResult, MapOutput, error) {
			out, err := mapSection(src, in)
			return nil, out, err
		})
	return s
}

type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type SectionCount struct {
	Section string `json:"section"`
	Count   int    `json:"count"`
}

type PluginProblem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Errors int    `json:"errors"`
}

type Summary struct {
	Repo           string            `json:"repo"`
	Commit         string            `json:"commit"`
	CommittedAt    string            `json:"committed_at"`
	Extractor      string            `json:"extractor"`
	RulesVersion   string            `json:"rules_version"`
	Facts          int               `json:"facts"`
	Kinds          []KindCount       `json:"kinds"`
	PluginProblems []PluginProblem   `json:"plugin_problems"`
	Exclusions     []fact.Exclusion  `json:"exclusions"`
	Stacks         []rules.StackRole `json:"stacks"`
	Map            []SectionCount    `json:"map"`
	// Surface metrics in total and in production; per service in the
	// metrics section.
	Total      *rules.Surface `json:"total,omitempty"`
	Production *rules.Surface `json:"production,omitempty"`
}

func summarise(src *Source) Summary {
	s := Summary{
		Repo: src.Scan.Repo, Commit: src.Scan.Commit.SHA, CommittedAt: src.Scan.Commit.CommittedAt,
		Extractor: src.Scan.Extractor.Version, RulesVersion: src.Map.RulesVersion, Facts: len(src.Scan.Facts),
		Kinds: []KindCount{}, PluginProblems: []PluginProblem{}, Exclusions: []fact.Exclusion{},
		Stacks: append([]rules.StackRole{}, src.Map.Stacks...), Map: []SectionCount{},
	}
	counts := map[string]int{}
	for _, f := range src.Scan.Facts {
		counts[f.Kind]++
	}
	for k, n := range counts {
		s.Kinds = append(s.Kinds, KindCount{k, n})
	}
	sort.Slice(s.Kinds, func(i, j int) bool { return s.Kinds[i].Kind < s.Kinds[j].Kind })
	for _, p := range src.Scan.Plugins {
		if p.Status != "ok" {
			s.PluginProblems = append(s.PluginProblems, PluginProblem{p.Name, p.Status, len(p.Errors)})
		}
	}
	if src.Scan.Config != nil {
		s.Exclusions = append(s.Exclusions, src.Scan.Config.Exclusions...)
	}
	all := sectionItems(src.Map)
	for _, name := range sections {
		s.Map = append(s.Map, SectionCount{name, len(all[name])})
	}
	if src.Map.Metrics != nil {
		s.Total, s.Production = &src.Map.Metrics.Total, &src.Map.Metrics.Production
	}
	return s
}

type FindInput struct {
	Kind   string `json:"kind,omitempty" jsonschema:"fact kind, e.g. service, dependency, env_var, outbound_host, route; empty for all"`
	Text   string `json:"text,omitempty" jsonschema:"case-insensitive text to find in the key or attributes"`
	Path   string `json:"path,omitempty" jsonschema:"only facts with evidence in this file or under this directory"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, default 50, at most 500"`
	Offset int    `json:"offset,omitempty" jsonschema:"number of matches to skip"`
}

type FindOutput struct {
	Total       int         `json:"total"`
	Facts       []fact.Fact `json:"facts"`
	KindsInScan []string    `json:"kinds_in_scan,omitempty"`
}

func find(src *Source, in FindInput) (FindOutput, error) {
	text := strings.ToLower(in.Text)
	var match []fact.Fact
	for _, f := range src.Scan.Facts {
		if in.Kind != "" && f.Kind != in.Kind {
			continue
		}
		if in.Path != "" && !evidenceUnder(f, in.Path) {
			continue
		}
		if text != "" && !containsText(f.Key, f.Attrs, text) {
			continue
		}
		match = append(match, f)
	}
	lo, hi, err := page(len(match), in.Limit, in.Offset)
	if err != nil {
		return FindOutput{}, err
	}
	out := FindOutput{Total: len(match), Facts: append([]fact.Fact{}, match[lo:hi]...)}
	if len(match) == 0 {
		out.KindsInScan = kinds(src.Scan) // a wrong kind is the usual cause
	}
	return out, nil
}

type GetInput struct {
	Kind string `json:"kind" jsonschema:"the fact's kind"`
	Key  string `json:"key" jsonschema:"the fact's key, as find_facts returns it"`
}

type GetOutput struct {
	Fact fact.Fact `json:"fact"`
}

type MapInput struct {
	Section string `json:"section" jsonschema:"one of stacks, links, images, ci_images, attached, parties, internal, unknown, own, routes, ai_tools, findings, tables, tests, confirmations, dismissed, metrics, modules, metrics_services, module_packages, module_edges, module_cycles"`
	Text    string `json:"text,omitempty" jsonschema:"case-insensitive text to find in an item"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size, default 50, at most 500"`
	Offset  int    `json:"offset,omitempty" jsonschema:"number of matches to skip"`
}

type MapOutput struct {
	Section string `json:"section"`
	Total   int    `json:"total"`
	Items   []any  `json:"items"`
}

func mapSection(src *Source, in MapInput) (MapOutput, error) {
	items, ok := sectionItems(src.Map)[in.Section]
	if !ok {
		return MapOutput{}, fmt.Errorf("no map section %q; sections: %s", in.Section, strings.Join(sections, ", "))
	}
	text := strings.ToLower(in.Text)
	var match []any
	for _, it := range items {
		if text != "" && !containsText("", it, text) {
			continue
		}
		match = append(match, it)
	}
	lo, hi, err := page(len(match), in.Limit, in.Offset)
	if err != nil {
		return MapOutput{}, err
	}
	return MapOutput{Section: in.Section, Total: len(match), Items: append([]any{}, match[lo:hi]...)}, nil
}

// sectionItems decodes the map's sections into plain JSON values, so the
// tools serve exactly what map.v1 says.
func sectionItems(m *rules.Map) map[string][]any {
	raw, _ := json.Marshal(m)
	var all map[string]any
	_ = json.Unmarshal(raw, &all)
	if mx, ok := all["metrics"].(map[string]any); ok {
		all["metrics_services"] = mx["services"]
	}
	if g, ok := all["modules"].(map[string]any); ok {
		all["module_packages"], all["module_edges"], all["module_cycles"] = g["packages"], g["edges"], g["cycles"]
	}
	out := map[string][]any{}
	for _, name := range sections {
		switch v := all[name].(type) {
		case []any:
			out[name] = v
		case map[string]any:
			out[name] = []any{v} // metrics and modules: one object
		default:
			out[name] = nil
		}
	}
	return out
}

func page(n, limit, offset int) (int, int, error) {
	if limit < 0 || offset < 0 {
		return 0, 0, fmt.Errorf("limit and offset must not be negative")
	}
	if limit == 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	lo := min(offset, n)
	return lo, min(lo+limit, n), nil
}

func containsText(key string, v any, text string) bool {
	if strings.Contains(strings.ToLower(key), text) {
		return true
	}
	raw, _ := json.Marshal(v)
	return strings.Contains(strings.ToLower(string(raw)), text)
}

func evidenceUnder(f fact.Fact, p string) bool {
	p = strings.TrimSuffix(p, "/")
	for _, e := range f.Evidence {
		if e.Path == p || strings.HasPrefix(e.Path, p+"/") {
			return true
		}
	}
	return false
}

func kinds(s *fact.Scan) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range s.Facts {
		if !seen[f.Kind] {
			seen[f.Kind] = true
			out = append(out, f.Kind)
		}
	}
	sort.Strings(out)
	return out
}
