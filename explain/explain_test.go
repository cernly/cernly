package explain

import (
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// The open core holds the words of one scan's map and of the gate, and no
// finding that needs history or CI.
func TestCoreRulesOnly(t *testing.T) {
	want := []string{rules.FindingImagePinning, rules.FindingProductionUnconfirmed, rules.FindingEndpointAnonymousPublic,
		rules.FindingPartyUnconfirmed, "driver_without_store", rules.FindingNoCodeLink, rules.FindingLinkConflict,
		"new_outside_party", "new_sensitive_column", "new_unpinned_image", "new_production_stack",
		"new_dependency", "new_transitive_dependency", "licence_not_allowed", config.GateNewAnonymousEndpoint, "gate_loosened"}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("%d rules, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.ID != want[i] {
			t.Errorf("rule %d = %s, want %s", i, r.ID, want[i])
		}
		if r.Title == r.ID || r.Meaning == "" || r.Action == "" {
			t.Errorf("%s has no words", r.ID)
		}
	}
}

// The endpoint finding is often intended and nothing confirms it: info.
func TestEndpointRuleLevel(t *testing.T) {
	if l := Of(rules.FindingEndpointAnonymousPublic).Level; l != Info {
		t.Errorf("level %s, want info", l)
	}
}

// The snippet a reader is shown is a valid .cernly.yml .
func TestStackRoleSnippet(t *testing.T) {
	got := config.ParseRepo([]byte(StackRole("default-2", "edge.vps")))
	if len(got.Errors) != 0 || len(got.Stacks) != 2 {
		t.Fatalf("parsed %+v", got)
	}
	for i, want := range []string{"default-2", "edge.vps"} {
		if s := got.Stacks[i]; s.Stack != want || s.Role != "production" {
			t.Errorf("stack %d: %+v", i, s)
		}
	}
}

func TestStackLabels(t *testing.T) {
	stack := func(name string, files ...string) fact.Fact {
		fs := []any{}
		for _, f := range files {
			fs = append(fs, f)
		}
		return fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + name, Attrs: map[string]any{"name": name, "files": fs}}
	}
	s := &fact.Scan{Facts: []fact.Fact{
		stack("default", "docker/development/compose.yml"),
		stack("default-2", "docker/production/compose.yml"),
		stack("default-3", "compose.yml"),
		stack("default-4", "docker-compose.yml"),
		stack("prod", "docker-compose.yml", "docker-compose.prod.yml"),
		stack("default-5", "ops/a/compose.yml"),
		stack("default-6", "ops/a/compose.test.yml"),
		stack("edge", "edge/compose.yml"),
	}}
	m := &rules.Map{Stacks: []rules.StackRole{
		{Stack: "stack:compose:default-3", Reasons: []rules.Reason{{Workflow: ".github/workflows/deploy.yml", Job: "deploy"}}},
		{Stack: "stack:compose:edge", Name: "UAT"},
	}}
	got := StackLabels(s, m)
	want := map[string]string{
		"default":   "docker/development",
		"default-2": "docker/production",
		"default-3": "started by deploy.yml",
		"default-4": "docker-compose.yml",
		"prod":      "prod",
		"default-5": "ops/a/compose.yml",
		"default-6": "ops/a/compose.test.yml",
		"edge":      "UAT",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}
