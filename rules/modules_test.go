package rules

import (
	"fmt"
	"testing"

	"github.com/cernly/cernly/fact"
)

func imp(from, to string) fact.Fact {
	return fact.Fact{Kind: fact.KindImport, Key: "import:go:x/" + from + ":x/" + to,
		Attrs:    map[string]any{"language": "go", "from": "x/" + from, "to": "x/" + to, "from_dir": from, "to_dir": to, "from_module": "x", "to_module": "x"},
		Evidence: []fact.Evidence{{Path: from + "/a.go", Line: 3}}}
}

// Fan-in, fan-out, depth over cycles, and what a service reaches from its Go
// entrypoint.
func TestModules(t *testing.T) {
	facts := []fact.Fact{
		imp("cmd/api", "billing"), imp("cmd/api", "store"),
		imp("billing", "store"), imp("billing", "tax"), imp("tax", "billing"), // a cycle
		imp("cmd/worker", "queue"),
		{Kind: fact.KindImageRecipe, Key: "image_recipe:Dockerfile:api", Attrs: map[string]any{"dockerfile": "Dockerfile", "target": "api",
			"entrypoints": []any{map[string]any{"kind": "go", "raw": "go build ./cmd/api", "path": "cmd/api", "resolved": true}}}},
	}
	m := &Map{Links: []Link{
		{Service: "service:compose:prod:api", Recipe: "image_recipe:Dockerfile:api", Sources: []string{"."}},
		{Service: "service:compose:prod:other", Recipe: "image_recipe:Dockerfile:other", Sources: []string{"cmd/worker", "queue"}},
	}}
	g := modules(m, facts)
	var got []string
	for _, p := range g.Packages {
		got = append(got, fmt.Sprintf("%s in%d out%d d%d %v", p.Dir, p.FanIn, p.FanOut, p.Depth, p.Cycle))
	}
	want := "[billing in2 out2 d1 true cmd/api in0 out2 d2 false cmd/worker in0 out1 d1 false queue in1 out0 d0 false store in2 out0 d0 false tax in1 out1 d1 true]"
	if fmt.Sprint(got) != want {
		t.Errorf("packages\n got %v\nwant %s", got, want)
	}
	if len(g.Cycles) != 1 || fmt.Sprint(g.Cycles[0].Packages) != "[x/billing x/tax]" || len(g.Cycles[0].From) != 2 {
		t.Errorf("cycles: %+v", g.Cycles)
	}
	if len(g.Services) != 2 {
		t.Fatalf("services: %+v", g.Services)
	}
	api, other := g.Services[0], g.Services[1]
	if api.Reach != "entrypoints" || fmt.Sprint(api.Packages) != "[x/billing x/cmd/api x/store x/tax]" {
		t.Errorf("api reaches %s %v; the worker's packages are in its sources but not imported", api.Reach, api.Packages)
	}
	if other.Reach != "sources" || fmt.Sprint(other.Packages) != "[x/cmd/worker x/queue]" {
		t.Errorf("other: %+v", other)
	}
	c := g.coupling(api.Packages)
	if *c != (Coupling{Packages: 4, Imports: 5, MaxFanIn: 2, MaxFanOut: 2, Cycles: 1, Depth: 2}) {
		t.Errorf("coupling %+v", *c)
	}
	if modules(&Map{}, nil) != nil {
		t.Error("no import facts: no graph")
	}
}

// Surface metrics count production stacks only in production, and size counts a
// file once.
func TestMetrics(t *testing.T) {
	facts := []fact.Fact{
		{Kind: fact.KindService, Key: "service:compose:prod:api", Attrs: map[string]any{"stack": "prod", "name": "api", "depends_on": []any{"db", "web"}}},
		{Kind: fact.KindService, Key: "service:compose:dev:api", Attrs: map[string]any{"stack": "dev", "name": "api"}},
		{Kind: fact.KindDatastore, Key: "datastore:compose:prod:db", Attrs: map[string]any{"stack": "prod", "name": "db"}},
		{Kind: fact.KindDatastore, Key: "datastore:compose:dev:db", Attrs: map[string]any{"stack": "dev", "name": "db"}},
		{Kind: fact.KindSourceSize, Key: "source_size:api:go", Attrs: map[string]any{"dir": "api", "language": "go", "files": 2, "lines": 12},
			Evidence: []fact.Evidence{{Path: "api/a.go", Line: 1, EndLine: 10}, {Path: "api/b.go", Line: 1}}},
		{Kind: fact.KindSourceSize, Key: "source_size:api:go:test", Attrs: map[string]any{"dir": "api", "language": "go", "test": true, "files": 1, "lines": 4},
			Evidence: []fact.Evidence{{Path: "api/a_test.go", Line: 1, EndLine: 4}}},
		{Kind: fact.KindSourceSize, Key: "source_size:tools:python", Attrs: map[string]any{"dir": "tools", "language": "python", "files": 1, "lines": 5},
			Evidence: []fact.Evidence{{Path: "tools/x.py", Line: 1, EndLine: 5}}},
		{Kind: fact.KindDBTable, Key: "db_table:sql:api/schema.sql:users", Evidence: []fact.Evidence{{Path: "api/schema.sql", Line: 1}}},
	}
	m := &Map{
		Stacks:   []StackRole{{Stack: "stack:compose:prod", Role: RoleProduction}, {Stack: "stack:compose:dev", Role: RoleNone}},
		Links:    []Link{{Service: "service:compose:prod:api", Recipe: "r", Sources: []string{"api", "api"}}, {Service: "service:compose:dev:api", Recipe: "r", Sources: []string{"api"}}},
		Parties:  []Party{{ID: "stripe", Services: []string{"service:compose:prod:api"}}, {ID: "openai", Services: []string{"service:compose:dev:api"}}},
		Findings: []Finding{{Rule: FindingImagePinning, Subject: "datastore:compose:prod:db", Detail: "image postgres:16"}, {Rule: FindingImagePinning, Subject: "r", Detail: "stage x uses go"}},
		Tables:   []Table{{Table: "users", From: []string{"db_table:sql:api/schema.sql:users"}, Check: []SensitiveColumn{{Column: "email"}, {Column: "iban"}}}},
		Routes:   []RouteEdge{{Route: "route:a", Stack: "prod", To: "service:compose:prod:api"}, {Route: "route:b", Stack: "dev"}},
	}
	x := metrics(m, facts)
	tot, prod := x.Total, x.Production
	if tot.OutsideParties != 2 || tot.DataStores != 2 || tot.ColumnsToCheck != 2 || tot.UnpinnedImages != 2 || tot.ProductionStacks != 1 || tot.Routes != 2 || tot.Size.Files != 4 || tot.Size.Lines != 20 || tot.Size.TestFiles != 1 || tot.Size.TestLines != 4 {
		t.Errorf("total %+v", tot)
	}
	if prod.OutsideParties != 1 || prod.DataStores != 1 || prod.ColumnsToCheck != 2 || prod.UnpinnedImages != 2 || prod.Routes != 1 || prod.Size.Files != 3 || prod.Size.Lines != 15 || prod.Size.TestLines != 4 {
		t.Errorf("production %+v", prod)
	}
	if fmt.Sprint(tot.Size.Languages) != "[{go 3 15} {python 1 5}]" {
		t.Errorf("languages %v", tot.Size.Languages)
	}
	api := x.Services[1]
	if api.Service != "service:compose:prod:api" || api.Role != RoleProduction || api.DataStores != 1 || api.Routes != 1 || api.UnpinnedImages != 1 || api.OutsideParties != 1 {
		t.Errorf("prod api %+v", api)
	}
}
