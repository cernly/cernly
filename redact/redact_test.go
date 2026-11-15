package redact

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/schema"
)

func handMade() fact.Scan {
	return fact.Scan{
		Schema:    fact.SchemaID,
		Repo:      "example.com/acme/shop",
		Commit:    fact.Commit{SHA: strings.Repeat("a", 40), CommittedAt: "2026-11-01T10:00:00Z", Dirty: true},
		Extractor: fact.Extractor{Version: "1.2.3"},
		Config: &fact.ScanConfig{
			File:       ".cernly.yml",
			Exclusions: []fact.Exclusion{{Pattern: "docs/my notes", Reason: "old shop docs", By: "repo", Line: 3, Files: 4}},
			Include:    []string{"vendor/acme"},
			Declared: &fact.Declared{
				OwnDomains: []fact.DeclaredDomain{{Domain: "shop.example.com", By: "repo", Line: 5}},
				Stacks:     []fact.DeclaredStack{{Stack: "edge", Role: "production", Name: "UAT", By: "operator"}},
				Gate:       []fact.DeclaredGate{{Rule: "licence_not_allowed", Level: "block", By: "repo", Licences: []string{"GPL-3.0"}}},
				Tags:       []fact.DeclaredTag{{Key: "team", Value: "payments", By: "repo", Line: 7}},
			},
			Errors: []fact.LocationError{{Path: "acme/.cernly.yml", Line: 9, Message: "bad pattern in acme"}},
		},
		Plugins: []fact.PluginRun{{
			Name: "dotnet", Version: "0.1.0", Source: "wasm", SHA256: strings.Repeat("b", 64), Signer: "acme-key", Status: "partial",
			Errors: []fact.LocationError{{Path: "src/Shop.Web/Program.cs", Line: 12, Message: "cannot read Shop.Web"}},
		}, {Name: "compose", Version: "0.4.1", Source: "builtin", Status: "ok"}, {Name: "go", Version: "0.4.1", Source: "builtin", Status: "ok"},
			{Name: "prisma", Version: "0.4.1", Source: "builtin", Status: "ok"}, {Name: "caddy", Version: "0.4.1", Source: "builtin", Status: "ok"}},
		Facts: []fact.Fact{
			{
				Kind: "service", Key: "service:compose:production:api", Plugin: "compose",
				Attrs: map[string]any{
					"name": "api", "image": "ghcr.io/acme/api:1.0", "depends_on": []any{"db"},
					"build_context": "services/api", "dockerfile": "services/api/Dockerfile",
					"port": float64(8080), "tls": true, "note": nil,
					"mounts": []any{map[string]any{"source": "./data", "target": "/var/lib/acme", "kind": "bind"}},
					"url":    "https://api.example.com:8443/v1/orders?token=abc#top",
					"auth":   map[string]any{"state": "required", "markers": []any{"[Authorize]"}},
					"engine": "postgres",
				},
				Evidence: []fact.Evidence{{Path: "deploy/docker-compose.yml", Line: 4, EndLine: 9, Unit: "services/api"}},
			},
			{
				Kind: "service", Key: "service:compose:production:db", Plugin: "compose",
				Attrs:    map[string]any{"name": "db", "image": "postgres:16"},
				Evidence: []fact.Evidence{{Path: "deploy/docker-compose.yml", Line: 20}},
			},
			{
				Kind: "outbound_host", Key: "outbound_host:api.stripe.com", Plugin: "go",
				Attrs:    map[string]any{"host": "api.stripe.com", "usage": "request", "via": "sdk", "address": "api:8080", "registry": "ghcr.io"},
				Evidence: []fact.Evidence{{Path: "src/Shop.Web/Controllers/Catalog.cs", Line: 3}},
			},
			{
				Kind: "datastore", Key: "datastore:efcore:main", Plugin: "dotnet",
				Attrs:    map[string]any{"via": "Mollie.Api", "engine": "sqlserver"},
				Evidence: []fact.Evidence{{Path: "src/Shop.Web/*.csproj", Line: 1}},
			},
			{
				Kind: "db_column", Key: "db_column:prisma:db:Order.plan", Plugin: "prisma",
				Attrs:    map[string]any{"table": "Order", "name": "plan", "type": "PlanTier", "enum": true},
				Evidence: []fact.Evidence{{Path: "prisma/schema.prisma", Line: 8}},
			},
			{
				Kind: "route", Key: "route:caddy:/api", Plugin: "caddy",
				Attrs:    map[string]any{"source": "deploy/Caddyfile", "action": "proxy", "paths": []any{"/api/*"}},
				Evidence: []fact.Evidence{{Path: "deploy/Caddyfile", Line: 2}},
			},
		},
	}
}

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := fact.Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRules(t *testing.T) {
	in := handMade()
	before := canon(t, in)
	got, m := Scan(in)
	if canon(t, in) != before {
		t.Fatal("the input was changed")
	}
	if got.Schema != fact.SchemaID || got.Repo != "repo-1" {
		t.Errorf("schema %q repo %q", got.Schema, got.Repo)
	}
	if got.Commit != in.Commit || got.Extractor != in.Extractor {
		t.Errorf("commit or extractor changed: %+v %+v", got.Commit, got.Extractor)
	}
	p := got.Plugins[0]
	if p.Name != "dotnet" || p.Version != "0.1.0" || p.Source != "wasm" || p.SHA256 != in.Plugins[0].SHA256 || !strings.HasPrefix(p.Signer, "name-") || p.Status != "partial" {
		t.Errorf("plugin run changed: %+v", p)
	}
	if e := p.Errors[0]; e.Line != 12 || !strings.HasPrefix(e.Message, "name-") || !strings.HasSuffix(e.Path, ".cs") || strings.Contains(e.Path, "Shop") {
		t.Errorf("plugin error: %+v", e)
	}
	c := got.Config
	if c.File != ".cernly.yml" {
		t.Errorf("config file %q", c.File)
	}
	if x := c.Exclusions[0]; !strings.HasPrefix(x.Pattern, "path-") || !strings.HasPrefix(x.Reason, "name-") || x.By != "repo" || x.Line != 3 || x.Files != 4 {
		t.Errorf("exclusion: %+v", x)
	}
	if !strings.HasPrefix(c.Include[0], "path-") {
		t.Errorf("include %q", c.Include[0])
	}
	d := c.Declared
	if !strings.HasPrefix(d.OwnDomains[0].Domain, "name-") || d.OwnDomains[0].By != "repo" {
		t.Errorf("own domain %+v", d.OwnDomains[0])
	}
	if s := d.Stacks[0]; !strings.HasPrefix(s.Stack, "name-") || s.Role != "production" || !strings.HasPrefix(s.Name, "name-") {
		t.Errorf("stack %+v", s)
	}
	if g := d.Gate[0]; g.Rule != "licence_not_allowed" || g.Level != "block" || !strings.HasPrefix(g.Licences[0], "name-") {
		t.Errorf("gate %+v", g)
	}
	if tg := d.Tags[0]; !strings.HasPrefix(tg.Key, "name-") || !strings.HasPrefix(tg.Value, "name-") || tg.By != "repo" {
		t.Errorf("tag %+v", tg)
	}

	api, db := got.Facts[0], got.Facts[1]
	if api.Kind != "service" || api.Plugin != "compose" || !strings.HasPrefix(api.Key, "service:name-") {
		t.Errorf("api fact: %s %s %s", api.Kind, api.Plugin, api.Key)
	}
	ev := api.Evidence[0]
	if ev.Line != 4 || ev.EndLine != 9 || !regexp.MustCompile(`^path-\d+/path-\d+$`).MatchString(ev.Unit) {
		t.Errorf("evidence %+v", ev)
	}
	if !regexp.MustCompile(`^path-\d+/docker-compose\.yml$`).MatchString(ev.Path) {
		t.Errorf("evidence path %q", ev.Path)
	}
	a := api.Attrs
	for k, want := range map[string]any{
		"port": float64(8080), "tls": true, "note": nil, "engine": "postgres",
	} {
		if a[k] != want {
			t.Errorf("attr %s: %v, want %v", k, a[k], want)
		}
	}
	if !strings.HasPrefix(a["name"].(string), "name-") || !strings.HasPrefix(a["image"].(string), "name-") {
		t.Errorf("name or image: %v %v", a["name"], a["image"])
	}
	// depends_on names a service by its name attribute, so they share a token.
	if db.Attrs["name"] != a["depends_on"].([]any)[0] {
		t.Errorf("relation lost: %v vs %v", db.Attrs["name"], a["depends_on"])
	}
	// The context and the evidence unit are the same folder: one set of tokens.
	if a["build_context"] != ev.Unit || a["dockerfile"] != ev.Unit+"/Dockerfile" {
		t.Errorf("build paths: %v %v", a["build_context"], a["dockerfile"])
	}
	mount := a["mounts"].([]any)[0].(map[string]any)
	if strings.Contains(mount["source"].(string), "data") || strings.Contains(mount["target"].(string), "acme") || mount["kind"] != "bind" || !strings.HasPrefix(mount["source"].(string), "./path-") {
		t.Errorf("mount %v", mount)
	}
	if u := a["url"].(string); !regexp.MustCompile(`^https://host-1:8443/path-\d+/path-\d+\?name-\d+#name-\d+$`).MatchString(u) {
		t.Errorf("url %q", u)
	}
	au := a["auth"].(map[string]any)
	if au["state"] != "required" || au["markers"].([]any)[0] != "[Authorize]" {
		t.Errorf("auth %v", au)
	}

	out := got.Facts[2].Attrs
	if out["host"] != "host-3" || out["address"] != "host-2:8080" || out["registry"] != "host-4" || out["usage"] != "request" || out["via"] != "sdk" {
		t.Errorf("outbound host attrs %v", out)
	}
	if v := got.Facts[3].Attrs["via"].(string); !strings.HasPrefix(v, "name-") {
		t.Errorf("free-text via kept: %q", v)
	}
	col := got.Facts[4].Attrs
	if !strings.HasPrefix(col["type"].(string), "name-") || col["enum"] != true {
		t.Errorf("enum column %v", col)
	}
	rt := got.Facts[5].Attrs
	if strings.Contains(rt["source"].(string), "deploy") || !strings.HasSuffix(rt["source"].(string), "Caddyfile") || rt["action"] != "proxy" || !regexp.MustCompile(`^/path-\d+/\*$`).MatchString(rt["paths"].([]any)[0].(string)) {
		t.Errorf("route %v", rt)
	}
	if !regexp.MustCompile(`^path-\d+/path-\d+/\*\.csproj$`).MatchString(got.Facts[3].Evidence[0].Path) {
		t.Errorf("glob stem: %q", got.Facts[3].Evidence[0].Path)
	}

	// The list is sorted and complete.
	if m.Schema != SchemaID {
		t.Errorf("schema %q", m.Schema)
	}
	if m.Counts()["repo"] != 1 || m.Counts()["host"] != 4 {
		t.Errorf("counts %v", m.Counts())
	}
	if !sort.SliceIsSorted(m.Tokens, func(i, j int) bool { return tokenLess(m.Tokens[i].Token, m.Tokens[j].Token) }) {
		t.Errorf("tokens not sorted: %v", m.Tokens)
	}
}

func tokenLess(a, b string) bool {
	ca, na, _ := strings.Cut(a, "-")
	cb, nb, _ := strings.Cut(b, "-")
	if ca != cb {
		return ca < cb
	}
	return len(na) < len(nb) || len(na) == len(nb) && na < nb
}

func TestPathSegments(t *testing.T) {
	for in, want := range map[string]string{
		"Controllers/Catalog.cs":      "path-1/path-2.cs",
		"/api/Catalog/{id}":           "/path-1/path-2/path-3",
		"../*/**/x.go":                "../*/**/path-1.go",
		"*.go":                        "*.go",
		".github/workflows/ci.yml":    ".github/workflows/path-1.yml",
		"a/Dockerfile":                "path-1/Dockerfile",
		"deploy/go.mod":               "path-1/go.mod",
		"src/Shop.Web":                "path-1/path-2",
		"Catalog.cs":                  "path-1.cs",
		"https://example.com/a/b?q=1": "https://host-1/path-1/path-2?name-1",
		"https://u:pw@example.com/":   "https://host-1/",
	} {
		if got := newRedactor().pathText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	// A folder and a file stem with the same name share a token.
	r := newRedactor()
	if a, b := r.pathText("a/Catalog.cs"), r.pathText("Catalog/x"); a != "path-1/path-2.cs" || b != "path-2/path-3" {
		t.Errorf("%q %q", a, b)
	}
}

func TestDeterministic(t *testing.T) {
	a, ma := Scan(handMade())
	b, mb := Scan(handMade())
	if canon(t, a) != canon(t, b) || canon(t, ma) != canon(t, mb) {
		t.Fatal("two runs differ")
	}
}

func TestEmptyScan(t *testing.T) {
	got, m := Scan(fact.Scan{Schema: fact.SchemaID})
	if got.Repo != "" || len(m.Tokens) != 0 || m.Tokens == nil {
		t.Errorf("%+v %+v", got, m)
	}
}

// goldens are the scan goldens and the schema's example.
func goldens(t *testing.T) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "cli", "testdata", "*.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, filepath.Join("..", "schema", "example.facts.json"))
	out := map[string][]byte{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Schema string `json:"schema"`
		}
		if json.Unmarshal(raw, &head) != nil || head.Schema != fact.SchemaID {
			continue // a map, docs or SBOM golden
		}
		out[filepath.Base(p)] = raw
	}
	if len(out) < 8 {
		t.Fatalf("only %d scan goldens found", len(out))
	}
	return out
}

func scanSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ScanV1))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schema.ScanV1ID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schema.ScanV1ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decode(t *testing.T, raw []byte) fact.Scan {
	t.Helper()
	var s fact.Scan
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

var tokenRe = regexp.MustCompile(`\b(?:host|name|path|repo)-[0-9]+\b`)

// restore puts the original values back for every token in s.
func restore(s string, m Map) string {
	by := map[string]string{}
	for _, t := range m.Tokens {
		by[t.Token] = t.Value
	}
	return tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		if v, ok := by[tok]; ok {
			return v
		}
		return tok
	})
}

func TestGoldens(t *testing.T) {
	sch := scanSchema(t)
	for name, raw := range goldens(t) {
		t.Run(name, func(t *testing.T) {
			in := decode(t, raw)
			out, m := Scan(in)
			text, err := fact.Canonical(out)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(text))
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(doc); err != nil {
				t.Fatalf("the redacted scan breaks the schema: %v", err)
			}
			// Nothing distinctive survives.
			var secrets []string
			secrets = append(secrets, in.Repo)
			for _, f := range in.Facts {
				for _, e := range f.Evidence {
					secrets = append(secrets, e.Path)
				}
				if h, ok := f.Attrs["host"].(string); ok {
					secrets = append(secrets, h)
				}
			}
			for _, s := range secrets {
				if len(s) < 4 || newRedactor().pathText(s) == s {
					continue // nothing in it is redacted by design
				}
				if strings.Contains(string(text), `"`+s+`"`) {
					t.Errorf("%q is still in the redacted scan", s)
				}
			}
			if bytes.Contains(text, []byte(in.Repo)) && in.Repo != "" {
				t.Errorf("repo id %q survives", in.Repo)
			}
			// Keys and hosts come back through the list.
			for i, f := range in.Facts {
				o := out.Facts[i]
				if got := restore(o.Key, m); got != f.Key {
					t.Errorf("key %q restores to %q", f.Key, got)
				}
				if h, ok := f.Attrs["host"].(string); ok {
					if got := restore(o.Attrs["host"].(string), m); got != h {
						t.Errorf("host %q restores to %q", h, got)
					}
				}
				for j, e := range f.Evidence {
					if got := restore(o.Evidence[j].Path, m); got != e.Path {
						t.Errorf("path %q restores to %q", e.Path, got)
					}
				}
			}
			if got := restore(out.Repo, m); got != in.Repo {
				t.Errorf("repo restores to %q", got)
			}
			// The numbers are an unbroken run from 1 in every category.
			seen := map[string]int{}
			for _, tk := range m.Tokens {
				c, n, _ := strings.Cut(tk.Token, "-")
				seen[c]++
				if n != strconv.Itoa(seen[c]) {
					t.Fatalf("token %s out of order", tk.Token)
				}
			}
		})
	}
}

// structural are the schema properties with an enum or a pattern that Scan
// keeps by position, not by the vocabulary: plugin run fields, the commit,
// the declared gate and sides, and tag keys.
var structural = setOf("name", "sha", "sha256", "by", "level", "key", "status")

// TestVocabularyFollowsSchema fails when the schema restricts an attribute
// to an enum or a short-identifier pattern and the vocabulary does not know
// its name, so a new scanner word is never redacted by accident.
func TestVocabularyFollowsSchema(t *testing.T) {
	var doc any
	if err := json.Unmarshal(schema.ScanV1, &doc); err != nil {
		t.Fatal(err)
	}
	var restrictedByName func(m map[string]any) bool
	restrictedByName = func(m map[string]any) bool {
		if _, ok := m["enum"].([]any); ok {
			return true
		}
		if _, ok := m["pattern"].(string); ok {
			return true
		}
		if it, ok := m["items"].(map[string]any); ok {
			return restrictedByName(it)
		}
		return false
	}
	missing := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].(map[string]any); ok {
				for name, sub := range props {
					m, _ := sub.(map[string]any)
					if m != nil && restrictedByName(m) && words[name] == nil && checked[name] == nil && !structural[name] {
						missing[name] = true
					}
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc)
	for name := range missing {
		t.Errorf("attribute %q is restricted by the schema but has no word list", name)
	}
	for name := range words {
		if pathKeys[name] || hostKeys[name] {
			t.Errorf("%q is both vocabulary and a path or host key", name)
		}
	}
}

// The review's cases (RES 831): strings under keys the scanner also uses for
// its own words are kept only when they are those words.
func TestFreeTextUnderVocabularyKeys(t *testing.T) {
	secret := []string{
		`[Authorize(Roles = "PayrollAdmins")]`, `RequireAuthorization() (if User.IsInRole("PayrollAdmins"))`,
		"PlanTier", "Acme.Billing.OrderStatus", `Unsupported("acme_type")`, "varchar(64) check (org = 'acme')", "Acme.Money",
		"acmeproduct", "acme-prod", "billing-portal", "LicenseRef-Acme-Internal", "acme_division", "acme-key",
		"github.com/acme/payroll", ".acme",
	}
	in := fact.Scan{
		Schema: fact.SchemaID, Repo: "example.com/acme/shop",
		Commit: fact.Commit{SHA: strings.Repeat("a", 40), CommittedAt: "2026-11-01T10:00:00Z"},
		Config: &fact.ScanConfig{Declared: &fact.Declared{
			Gate: []fact.DeclaredGate{{Rule: "licence_not_allowed", Level: "block", By: "repo", Licences: []string{"LicenseRef-Acme-Internal", "GPL-3.0"}}},
			Tags: []fact.DeclaredTag{{Key: "acme_division", Value: "x", By: "repo"}},
		}},
		Plugins: []fact.PluginRun{{Name: "dotnet", Version: "0.1.0", Source: "wasm", Signer: "acme-key", Status: "ok"}},
		Facts: []fact.Fact{
			{Kind: "endpoint", Key: "endpoint:a", Plugin: "dotnet", Attrs: map[string]any{"auth": map[string]any{"state": "required", "markers": []any{
				`[Authorize(Roles = "PayrollAdmins")]`, `RequireAuthorization() (if User.IsInRole("PayrollAdmins"))`, "[Authorize]"}}},
				Evidence: []fact.Evidence{{Path: "charts/billing.acme", Line: 1}}},
			{Kind: "db_column", Key: "db_column:a", Plugin: "dotnet", Attrs: map[string]any{"type": "PlanTier", "native_type": "varchar(64) check (org = 'acme')"}},
			{Kind: "db_column", Key: "db_column:b", Plugin: "dotnet", Attrs: map[string]any{"type": "Acme.Billing.OrderStatus", "native_type": "Acme.Money"}},
			{Kind: "db_column", Key: "db_column:c", Plugin: "dotnet", Attrs: map[string]any{"type": `Unsupported("acme_type")`, "native_type": "nvarchar(320)"}},
			{Kind: "db_column", Key: "db_column:d", Plugin: "dotnet", Attrs: map[string]any{"type": "String[]", "native_type": "Decimal(10,2)"}},
			{Kind: "catalog_entity", Key: "catalog_entity:a", Plugin: "github.com/acme/payroll", Attrs: map[string]any{"kind": "acmeproduct", "lifecycle": "acme-prod", "type": "billing-portal"}},
			{Kind: "catalog_entity", Key: "catalog_entity:b", Plugin: "dotnet", Attrs: map[string]any{"kind": "component", "lifecycle": "production", "type": "service"}},
		},
	}
	got, _, readable := ScanReadable(in)
	text := canon(t, got)
	for _, s := range secret {
		if strings.Contains(text, s) || slices.Contains(readable, s) {
			t.Errorf("%q survived", s)
		}
	}
	for _, s := range []string{"[Authorize]", "nvarchar(320)", "String[]", "Decimal(10,2)", "component", "production", "service", "dotnet"} {
		if !slices.Contains(readable, s) {
			t.Errorf("%q was tokenized; readable: %q", s, readable)
		}
	}
	// Licence ids are the customer's policy: every one is a token.
	for _, l := range got.Config.Declared.Gate[0].Licences {
		if !strings.HasPrefix(l, "name-") {
			t.Errorf("licence %q kept", l)
		}
	}
}
