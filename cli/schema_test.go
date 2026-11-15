package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/rules"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compileSchema loads one of schema/*.schema.json (draft 2020-12).
func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "schema", name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(name, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, name string, data []byte) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Errorf("%s does not match its schema:\n%v", name, err)
	}
}

// Every scan and map the tests produce, and the committed example, match the
// published schemas (scan.v1, map.v1).
func TestSchemas(t *testing.T) {
	scan := compileSchema(t, "scan.v1.schema.json")
	mp := compileSchema(t, "map.v1.schema.json")
	example, err := os.ReadFile(filepath.Join("..", "schema", "example.facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	validate(t, scan, "schema/example.facts.json", example)
	goldens, err := filepath.Glob(filepath.Join("testdata", "*.golden.json"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("no goldens: %v", err)
	}
	for _, g := range goldens {
		data, err := os.ReadFile(g)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(g, ".sbom.golden.json") {
			continue // CycloneDX, checked against its own schema by hand
		}
		if strings.HasSuffix(g, ".map.golden.json") {
			validate(t, mp, g, data)
		} else {
			validate(t, scan, g, data)
		}
	}
	// The live output too, so a change that has not been written to the
	// goldens yet is checked as well.
	for _, name := range goldenFixtures {
		validate(t, scan, name+" (scanned now)", scanDir(t, name))
	}
}

// The schema rejects what it should: a fact without evidence.
func TestSchemaRejects(t *testing.T) {
	scan := compileSchema(t, "scan.v1.schema.json")
	data := scanDir(t, "fixture")
	bad := bytes.Replace(data, []byte(`"evidence":[{"end_line":21,"line":20,"path":"docker-compose.yml","unit":"postgres"}]`), []byte(`"evidence":[]`), 1)
	if bytes.Equal(bad, data) {
		t.Fatal("fixture changed: update the replacement")
	}
	doc, _ := jsonschema.UnmarshalJSON(bytes.NewReader(bad))
	if scan.Validate(doc) == nil {
		t.Error("a fact without evidence passed the schema")
	}
}

// ASP.NET endpoint facts match attrs_endpoint, including the
// optional ext keys, and an unknown attribute is rejected.
func TestSchemaEndpoint(t *testing.T) {
	scan := compileSchema(t, "scan.v1.schema.json")
	dir := t.TempDir()
	files := map[string]string{
		"src/Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"src/Api/Program.cs": "var app = builder.Build();\napp.MapGet(\"/h\", H).AllowAnonymous();\n#if DEBUG\napp.MapGet(\"/d\", H);\n#endif\nstatic void M(IEndpointRouteBuilder e) { e.MapGet(\"/u\", H); }\nif (a) app.MapGet(\"/r\", A); else app.MapGet(\"/r\", B);\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, _, err := pipeline.ScanWorkTree(dir, "local/endpoints", fixtureSHA, fixtureAt, plugin.All(), config.Operator{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := fact.Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"endpoint"`, `"prefix_unresolved":true`, `"conditional":"#if DEBUG"`, `"app_unit":"src/Api"`, `"file":"src/Api/Program.cs"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("scan lacks %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), `"markers":null`) {
		t.Fatalf("null markers: %s", data)
	}
	validate(t, scan, "endpoints (scanned now)", data)
	mdata, err := json.Marshal(rules.Derive(s, strings.Repeat("a", 64), rules.Settings{DefaultBranch: "main"}))
	if err != nil || !strings.Contains(string(mdata), `"endpoints":[`) {
		t.Fatalf("map lacks endpoints: %v %s", err, mdata)
	}
	validate(t, compileSchema(t, "map.v1.schema.json"), "endpoints map (derived now)", mdata)
	bad := bytes.Replace(data, []byte(`"framework":"aspnet-minimal"`), []byte(`"framework":"aspnet-minimal","surprise":1`), 1)
	doc, _ := jsonschema.UnmarshalJSON(bytes.NewReader(bad))
	if scan.Validate(doc) == nil {
		t.Error("an endpoint with an unknown attribute passed the schema")
	}
}
