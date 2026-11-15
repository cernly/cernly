package drift

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

func TestCompare(t *testing.T) {
	dep := func(v string, line int) fact.Fact {
		return fact.Fact{Kind: "dependency", Key: "dependency:go:backend:x", Attrs: map[string]any{"version": v},
			Evidence: []fact.Evidence{{Path: "backend/go.mod", Line: line}}}
	}
	a := &fact.Scan{Repo: "r", Commit: fact.Commit{SHA: "a"}, Facts: []fact.Fact{
		dep("v1", 3), {Kind: "env_var", Key: "env_var:OLD", Attrs: map[string]any{"name": "OLD"}},
		{Kind: "env_var", Key: "env_var:SAME", Attrs: map[string]any{"name": "SAME"}, Evidence: []fact.Evidence{{Path: "x", Line: 1}}}}}
	b := &fact.Scan{Repo: "r", Commit: fact.Commit{SHA: "b"}, Facts: []fact.Fact{
		dep("v2", 3), {Kind: "env_var", Key: "env_var:NEW", Attrs: map[string]any{"name": "NEW"}},
		// moved to another line: not drift
		{Kind: "env_var", Key: "env_var:SAME", Attrs: map[string]any{"name": "SAME"}, Evidence: []fact.Evidence{{Path: "x", Line: 9}}}}}
	am := &rules.Map{Parties: []rules.Party{{ID: "sentry", Grade: "single", Services: []string{"api"}}}}
	bm := &rules.Map{Parties: []rules.Party{{ID: "sentry", Grade: "confirmed", Services: []string{"api"}}, {ID: "openai", Grade: "single"}}}
	d := Compare(a, b, am, bm)
	var got []string
	for _, c := range append(d.Facts, d.Map...) {
		got = append(got, c.Change+" "+c.Key)
	}
	want := "[changed dependency:go:backend:x added env_var:NEW removed env_var:OLD added openai changed sentry]"
	if fmt.Sprint(got) != want {
		t.Errorf("got  %v\nwant %s", got, want)
	}
}

// Transitive dependencies are their own group, after the direct
// ones, so a lockfile update never floods "dependencies".
func TestTransitiveSection(t *testing.T) {
	dep := func(key string, direct bool, v string) fact.Fact {
		return fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: map[string]any{"name": key, "version": v, "direct": direct}}
	}
	a := &fact.Scan{Facts: []fact.Fact{dep("express", true, "4.21.1"), dep("qs", false, "6.13.0")}}
	b := &fact.Scan{Facts: []fact.Fact{dep("express", true, "4.21.2"), dep("qs", false, "6.13.1"), dep("ms", false, "2.1.3")}}
	d := diff(factItems(a), factItems(b))
	var got []string
	for _, g := range Groups(&Drift{Facts: d}) {
		got = append(got, fmt.Sprintf("%s+%d~%d", g.Section, g.Added, g.Changed))
	}
	if fmt.Sprint(got) != "[dependency+0~1 dependency_transitive+1~1]" {
		t.Errorf("groups = %v", got)
	}
}

// A finding whose detail changes is one change, not removed and added; own
// hosts, inside uses, routes and AI tools are compared too.
func TestCompareMap(t *testing.T) {
	s := &fact.Scan{Repo: "r"}
	am := &rules.Map{
		Findings: []rules.Finding{
			{Rule: "party_unconfirmed", Subject: "party:aws", Detail: "AWS rests on one kind of signal (env); confirm once"},
			{Rule: "image_pinning", Subject: "image_recipe:Dockerfile:_final", Grade: "floating", Detail: "base golang:1 is not pinned by digest"},
			{Rule: "image_pinning", Subject: "image_recipe:Dockerfile:_final", Grade: "floating", Detail: "base alpine is not pinned by digest"},
		},
		Own:      []rules.OwnHost{{Host: "app.example.be", Domain: "app.example.be", ServedBy: []string{"route:a"}}},
		Internal: []rules.InternalUse{{ID: "s3", Service: "service:compose:prod:api", Datastore: "datastore:compose:prod:minio"}},
		Routes:   []rules.RouteEdge{{Route: "route:caddy:Caddyfile:app/", Stack: "stack:compose:prod", Upstream: "api:8080"}},
		AITools:  []rules.AITool{{Tool: "claude", Name: "Claude Code", Files: 1}},
	}
	bm := &rules.Map{
		Findings: []rules.Finding{
			{Rule: "party_unconfirmed", Subject: "party:aws", Detail: "AWS rests on one fact (env_var:AWS_REGION); confirm once"},
			{Rule: "image_pinning", Subject: "image_recipe:Dockerfile:_final", Grade: "floating", Detail: "base golang:1 is not pinned by digest"},
			{Rule: "image_pinning", Subject: "image_recipe:Dockerfile:_final", Grade: "floating", Detail: "base alpine is not pinned by digest"},
		},
		Own: []rules.OwnHost{{Host: "app.example.be", Domain: "example.be", Declared: &rules.Declarant{By: "repo", Line: 2}, ServedBy: []string{"route:a"}}},
		Routes: []rules.RouteEdge{{Route: "route:caddy:Caddyfile:app/", Stack: "stack:compose:prod", Upstream: "web:3000"},
			{Route: "route:caddy:Caddyfile:api/", Stack: "stack:compose:prod", Upstream: "api:8080"}},
		AITools: []rules.AITool{{Tool: "claude", Name: "Claude Code", Files: 3}, {Tool: "cursor", Name: "Cursor", Files: 1}},
	}
	d := Compare(s, s, am, bm)
	var got []string
	for _, c := range d.Map {
		got = append(got, c.Change+" "+c.Section+" "+c.Key+" | "+c.Diff())
	}
	want := []string{
		"changed ai_tools claude | files: 1 → 3",
		"added ai_tools cursor | ",
		"changed findings party_unconfirmed party:aws | detail: AWS rests on one kind of signal (env); confirm once → AWS rests on one fact (env_var:AWS_REGION); confirm once",
		"removed internal s3 service:compose:prod:api | ",
		"changed own app.example.be | declared: none → repo; domain: app.example.be → example.be",
		"added routes route:caddy:Caddyfile:api/ @ stack:compose:prod | ",
		"changed routes route:caddy:Caddyfile:app/ @ stack:compose:prod | upstream: api:8080 → web:3000",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	g := MapGroups(d)
	var labels []string
	for _, x := range g {
		labels = append(labels, fmt.Sprint(x.Label, " ", x.Added, x.Removed, x.Changed))
	}
	if fmt.Sprint(labels) != "[served inside 0 1 0 own hosts 0 0 1 routes 1 0 1 AI tools 1 0 1 findings 0 0 1]" {
		t.Errorf("map groups = %v", labels)
	}
	if e := g[4].Examples[0]; !strings.HasPrefix(e, "~ party_unconfirmed party:aws: detail: AWS rests on one kind") {
		t.Errorf("finding example = %q", e)
	}
}

func TestWritesTheCernlyID(t *testing.T) {
	d := Compare(&fact.Scan{Repo: "example.org/a"}, &fact.Scan{Repo: "example.org/a"}, nil, nil)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Schema != "cernly.drift/v1" || !strings.Contains(string(raw), `"schema":"cernly.drift/v1"`) {
		t.Errorf("written as %s", raw)
	}
}

func TestReadAcceptsTheOldID(t *testing.T) {
	for _, id := range []string{"cernly.drift/v1", legacySchemaID} {
		d, err := Read([]byte(`{"schema":"` + id + `","repo":"example.org/a","facts":[{"change":"added","section":"service","key":"service:x"}],"map":[]}`))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if d.Schema != SchemaID || len(d.Facts) != 1 {
			t.Errorf("%s: read as %+v", id, d)
		}
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), legacySchemaID) {
			t.Errorf("%s: written again as %s", id, raw)
		}
	}
	if _, err := Read([]byte(`{"schema":"cernly.scan/v1"}`)); err == nil {
		t.Error("a scan read as drift")
	}
}
