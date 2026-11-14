package sbom

import (
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

func TestPURL(t *testing.T) {
	for _, c := range []struct{ eco, name, version, want string }{
		{"npm", "@mollie/api-client", "4.1.0", "pkg:npm/%40mollie/api-client@4.1.0"},
		{"npm", "react", "^19", "pkg:npm/react"},
		{"go", "github.com/jackc/pgx/v5", "v5.7.1", "pkg:golang/github.com/jackc/pgx/v5@v5.7.1"},
		{"pypi", "Typing_Extensions", "4.12.2", "pkg:pypi/typing-extensions@4.12.2"},
		{"pypi", "fastapi", ">=0.115", "pkg:pypi/fastapi"},
		{"npm", "x", "1.0.0+build.1", "pkg:npm/x@1.0.0%2Bbuild.1"},
		{"cargo", "serde", "1.0.0", "pkg:generic/serde@1.0.0"},
	} {
		if got := PURL(c.eco, c.name, c.version, Exact(c.version)); got != c.want {
			t.Errorf("PURL(%s %s %s) = %s, want %s", c.eco, c.name, c.version, got, c.want)
		}
	}
	for _, v := range []string{"", "*", "1.x", "workspace:*", "file:../x", ">=1, <2", "latest || 2"} {
		if Exact(v) {
			t.Errorf("Exact(%q) = true", v)
		}
	}
}

func TestPURLNuget(t *testing.T) {
	if got, want := PURL("nuget", "Newtonsoft.Json", "13.0.3", true), "pkg:nuget/Newtonsoft.Json@13.0.3"; got != want {
		t.Errorf("exact = %s, want %s", got, want)
	}
	if got, want := PURL("nuget", "Newtonsoft.Json", "[13.0,)", false), "pkg:nuget/Newtonsoft.Json"; got != want {
		t.Errorf("range = %s, want %s", got, want)
	}
	// An unknown version (several conditional candidates, frameworks that
	// disagree) is no version: no @ in the purl.
	if got, want := PURL("nuget", "Example.Ef", "", Exact("")), "pkg:nuget/Example.Ef"; got != want {
		t.Errorf("unknown = %s, want %s", got, want)
	}
}

// A transitive dependency whose roots attribute is one string (not a list)
// still reaches the service whose manifest folder it names.
func TestBuildScopeRootsAsString(t *testing.T) {
	dep := func(key, name string, direct bool, roots any) fact.Fact {
		attrs := map[string]any{"ecosystem": "npm", "name": name, "version": "1.0.0", "direct": direct}
		if roots != nil {
			attrs["roots"] = roots
		}
		return fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: attrs,
			Evidence: []fact.Evidence{{Path: "app/package.json", Line: 1, Unit: "app"}}}
	}
	s := &fact.Scan{Repo: "example/repo", Facts: []fact.Fact{
		dep("dependency:npm:app:direct", "direct", true, nil),
		dep("dependency:npm:app:left", "left", false, "app"),
	}}
	m := &rules.Map{Attached: []rules.Attached{{Service: "service:a", Fact: "dependency:npm:app:direct", Kind: fact.KindDependency}}}
	b, err := BuildScope(s, m, Scope{Service: "service:a"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Components {
		if c.Name == "left" {
			return
		}
	}
	t.Errorf("transitive dependency with a string roots attribute is missing: %+v", b.Components)
}
