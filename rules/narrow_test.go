package rules

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
)

func dnRecipe(eps []any, others ...string) fact.Fact {
	a := map[string]any{"dockerfile": "Dockerfile", "target": "_final", "final": true, "sources": []any{"."}}
	if eps != nil {
		a["entrypoints"] = eps
	}
	if len(others) > 0 {
		a["other_commands"] = fact.Strings(others)
	}
	return fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:Dockerfile:_final", Attrs: a}
}

func dnEntry(projects ...string) map[string]any {
	return map[string]any{"kind": "dotnet", "path": "x.csproj", "resolved": true, "raw": "dotnet publish x.csproj", "projects": fact.Strings(projects)}
}

func dnImport(from, to string) fact.Fact {
	return fact.Fact{Kind: fact.KindImport, Key: "import:dotnet:" + from + ">" + to, Plugin: "dotnet",
		Attrs: map[string]any{"language": "dotnet", "from": from + "/" + from + ".csproj", "to": to + "/" + to + ".csproj", "from_dir": from, "to_dir": to}}
}

func dnUnit(dir string) fact.Fact {
	return fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:" + dir + ":P", Plugin: "deps-nuget",
		Evidence: []fact.Evidence{{Path: dir + "/App.csproj", Line: 3}}}
}

func dnGraph() []fact.Fact {
	return []fact.Fact{dnImport("src/Web", "src/Infra"), dnImport("src/Infra", "src/Domain"),
		dnUnit("src/Web"), dnUnit("src/Infra"), dnUnit("src/Domain"), dnUnit("legacy/Old"), dnUnit("tests/Web.Tests")}
}

func TestNarrowDotnet(t *testing.T) {
	rec := dnRecipe([]any{dnEntry("src/Web")})
	n, narrowed, excluded := narrow(rec, []string{"."}, dnGraph())
	if n.State != "narrowed" || fmt.Sprint(narrowed) != "[src/Web]" || fmt.Sprint(excluded) != "[legacy/Old tests/Web.Tests]" {
		t.Fatalf("%+v %v %v", n, narrowed, excluded)
	}
	for p, want := range map[string]bool{"src/Domain/Order.cs": true, "legacy/Old/A.cs": false, "db/migrations/1.sql": true, "tests/Web.Tests/T.cs": false} {
		if covers(p, []string{"."}, excluded) != want {
			t.Errorf("covers(%s) want %v", p, want)
		}
	}
}

func TestNarrowNested(t *testing.T) {
	facts := []fact.Fact{dnUnit("src/Outer"), dnUnit("src/Outer/Inner"), dnUnit("src/Other"), {Kind: fact.KindImport, Plugin: "dotnet", Key: "k", Attrs: map[string]any{"language": "dotnet", "from_dir": "src/Other", "to_dir": "src/Other"}}}
	n, _, excluded := narrow(dnRecipe([]any{dnEntry("src/Outer/Inner")}), []string{"."}, facts)
	if n.State != "narrowed" || fmt.Sprint(excluded) != "[src/Other]" {
		t.Errorf("%+v excluded %v: src/Outer holds the entrypoint and must stay", n, excluded)
	}
	// A project folder nested inside a closure folder is not excluded.
	facts = []fact.Fact{dnUnit("src/Web"), dnUnit("src/Web/Sub"), dnImport("src/Web", "src/Web")}
	if _, _, ex := narrow(dnRecipe([]any{dnEntry("src/Web")}), []string{"."}, facts); len(ex) != 0 {
		t.Errorf("nested in closure: %v", ex)
	}
}

func TestNarrowKeptReasons(t *testing.T) {
	g := dnGraph()
	cases := []struct {
		name   string
		rec    fact.Fact
		facts  []fact.Fact
		reason string
	}{
		{"no entrypoints", dnRecipe(nil), g, "no build entrypoint"},
		{"unresolved", dnRecipe([]any{map[string]any{"kind": "dotnet", "resolved": false, "raw": "dotnet publish $P"}}), g, "entrypoint not resolved: dotnet publish $P"},
		{"go", dnRecipe([]any{map[string]any{"kind": "go", "path": "cmd/a", "resolved": true, "raw": "go build"}}), g, "go entrypoints are not narrowed yet"},
		{"mixed", dnRecipe([]any{dnEntry("src/Web"), map[string]any{"kind": "go", "path": "cmd/a", "resolved": true, "raw": "go build"}}), g, "go entrypoints are not narrowed yet"},
		{"other commands", dnRecipe([]any{dnEntry("src/Web")}, "make"), g, "unknown RUN commands: make"},
		{"no graph", dnRecipe([]any{dnEntry("src/Web")}), []fact.Fact{dnUnit("src/Web")}, "no .NET project graph"},
	}
	for _, c := range cases {
		n, nr, ex := narrow(c.rec, []string{"."}, c.facts)
		if n.State != "kept" || n.Reason != c.reason || nr != nil || ex != nil {
			t.Errorf("%s: %+v %v %v", c.name, n, nr, ex)
		}
	}
}

func TestNarrowCIImage(t *testing.T) {
	rec := ciRecipe("Dockerfile", "_final", true, ".")
	rec.Attrs["entrypoints"] = []any{dnEntry("src/Web")}
	s := &fact.Scan{Facts: append(dnGraph(), rec,
		ciBuild("app", "Dockerfile", true, []string{"ghcr.io/acme/app:1"}, buildWf("ship")))}
	m := DeriveWith(s, strings.Repeat("0", 64), Settings{DefaultBranch: "main"}, Catalogue{})
	if len(m.CIImages) != 1 {
		t.Fatalf("ci images %v", m.CIImages)
	}
	c := m.CIImages[0]
	if c.Narrowing.State != "narrowed" || c.Precision != "entrypoint" || fmt.Sprint(c.Excluded) != "[legacy/Old tests/Web.Tests]" || fmt.Sprint(c.Narrowed) != "[src/Web]" {
		t.Errorf("%+v", c)
	}
}

func TestAttachNarrowed(t *testing.T) {
	dep := func(dir string) fact.Fact {
		return fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:" + dir + ":P", Attrs: map[string]any{"direct": true},
			Evidence: []fact.Evidence{{Path: dir + "/App.csproj", Line: 3, Unit: dir}}}
	}
	// A central version also gives evidence at the root props file, with the project as unit.
	central := fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:tests/Web.Tests:xunit", Attrs: map[string]any{"direct": true},
		Evidence: []fact.Evidence{{Path: "Directory.Packages.props", Line: 5, Unit: "tests/Web.Tests"}, {Path: "tests/Web.Tests/App.csproj", Line: 3, Unit: "tests/Web.Tests"}}}
	facts := []fact.Fact{dep("legacy/Old"), dep("src/Infra"), central}
	m := &Map{
		Links:    []Link{{Service: "service:compose:prod:web", Recipe: "r", Sources: []string{"."}, Excluded: []string{"legacy/Old", "tests/Web.Tests"}}},
		CIImages: []CIImage{{Image: "ci_image:Dockerfile:_final", Recipe: "r", Sources: []string{"."}, Excluded: []string{"legacy/Old", "tests/Web.Tests"}}},
	}
	attach(m, facts)
	if len(m.Attached) != 2 {
		t.Fatalf("attached %+v", m.Attached)
	}
	for _, a := range m.Attached {
		if a.Fact != "dependency:nuget:src/Infra:P" {
			t.Errorf("%s attached %s", a.Service, a.Fact)
		}
	}
}

func TestNarrowPackagesConfigUnit(t *testing.T) {
	old := fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:legacy/Old/Old.csproj:p", Plugin: "deps-nuget",
		Evidence: []fact.Evidence{{Path: "legacy/Old/packages.config", Line: 4}}}
	_, _, excluded := narrow(dnRecipe([]any{dnEntry("src/Web")}), []string{"."}, append(dnGraph(), old))
	if fmt.Sprint(excluded) != "[legacy/Old tests/Web.Tests]" {
		t.Errorf("excluded %v", excluded)
	}
}

func dnItem(from, toDir, item string) fact.Fact {
	a := map[string]any{"language": "dotnet", "from": from + "/" + from + ".csproj", "to": toDir + "/X.cs", "from_dir": from, "item": item}
	if toDir != "" {
		a["to_dir"] = toDir
	} else {
		a["to"] = `$(SharedDir)\Y.cs`
		a["ext"] = map[string]any{"unresolved": true}
	}
	return fact.Fact{Kind: fact.KindImport, Key: "import:dotnet:" + from + ":" + item + ":" + toDir, Plugin: "dotnet", Attrs: a}
}

func TestNarrowLinkedItems(t *testing.T) {
	rec := dnRecipe([]any{dnEntry("src/Web")})
	// A Compile item that reaches into legacy/Old keeps it covered.
	n, _, ex := narrow(rec, []string{"."}, append(dnGraph(), dnItem("src/Web", "legacy/Old", "Compile")))
	if n.State != "narrowed" || fmt.Sprint(ex) != "[tests/Web.Tests]" {
		t.Errorf("linked compile: %+v %v", n, ex)
	}
	// A linked item into a folder under an excluded unit's ancestor keeps the ancestor too.
	n, _, ex = narrow(rec, []string{"."}, append(dnGraph(), dnItem("src/Web", "legacy", "Content")))
	if n.State != "narrowed" || fmt.Sprint(ex) != "[tests/Web.Tests]" {
		t.Errorf("linked ancestor: %+v %v", n, ex)
	}
	// An item whose path is not resolved keeps the link.
	n, nr, ex := narrow(rec, []string{"."}, append(dnGraph(), dnItem("src/Web", "", "Compile")))
	if n.State != "kept" || n.Reason != `linked item not resolved: $(SharedDir)\Y.cs` || nr != nil || ex != nil {
		t.Errorf("unresolved: %+v", n)
	}
	// Unresolved item of a project outside the closure does not matter.
	n, _, _ = narrow(rec, []string{"."}, append(dnGraph(), dnItem("legacy/Old", "", "Compile")))
	if n.State != "narrowed" {
		t.Errorf("outside closure: %+v", n)
	}
}

func TestNarrowNoSources(t *testing.T) {
	n, nr, ex := narrow(dnRecipe([]any{dnEntry("src/Web")}), nil, dnGraph())
	if n.State != "kept" || n.Reason != "no sources" || nr != nil || ex != nil {
		t.Errorf("%+v %v %v", n, nr, ex)
	}
}

func TestNarrowLegacyProject(t *testing.T) {
	legacy := fact.Fact{Kind: fact.KindImport, Key: "import:dotnet:legacy/Old/Old.csproj:?", Plugin: "dotnet",
		Attrs: map[string]any{"language": "dotnet", "from": "legacy/Old/Old.csproj", "to": "legacy/Old/Old.csproj", "from_dir": "legacy/Old", "item": "Project",
			"ext": map[string]any{"unresolved": true}}}
	rec := dnRecipe([]any{dnEntry("src/Web")})
	// The closure reaches the legacy project: what it pulls in is unknown.
	n, _, _ := narrow(rec, []string{"."}, append(dnGraph(), legacy, dnImport("src/Web", "legacy/Old")))
	if n.State != "kept" || n.Reason != "linked item not resolved: legacy/Old/Old.csproj" {
		t.Errorf("reached: %+v", n)
	}
	// Outside the closure it is excluded as before.
	n, _, ex := narrow(rec, []string{"."}, append(dnGraph(), legacy))
	if n.State != "narrowed" || fmt.Sprint(ex) != "[legacy/Old tests/Web.Tests]" {
		t.Errorf("outside: %+v %v", n, ex)
	}
}

func TestAttachUnitOnlyForCodeUnits(t *testing.T) {
	// Compose evidence names a service as its unit; a service called like an excluded folder keeps its env var.
	env := fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:K", Evidence: []fact.Evidence{{Path: "docker-compose.yml", Line: 4, Unit: "tests"}}}
	unit := fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:tests/T.csproj:x", Attrs: map[string]any{"direct": true},
		Evidence: []fact.Evidence{{Path: "tests/T.csproj", Line: 2, Unit: "tests"}}}
	m := &Map{Links: []Link{{Service: "service:compose:prod:web", Recipe: "r", Sources: []string{"."}, Excluded: []string{"tests"}}}}
	attach(m, []fact.Fact{env, unit})
	if len(m.Attached) != 1 || m.Attached[0].Fact != "env_var:K" {
		t.Errorf("attached %+v", m.Attached)
	}
}

func TestNarrowKeepsOnCappedChoose(t *testing.T) {
	capped := fact.Fact{Kind: fact.KindImport, Key: "import:dotnet:src/Web/Web.csproj:ProjectReference:(Choose: rest skipped)", Plugin: "dotnet",
		Attrs: map[string]any{"language": "dotnet", "from": "src/Web/Web.csproj", "to": "(Choose: rest skipped)", "from_dir": "src/Web", "item": "ProjectReference",
			"ext": map[string]any{"unresolved": true}}}
	n, _, _ := narrow(dnRecipe([]any{dnEntry("src/Web")}), []string{"."}, append(dnGraph(), capped))
	if n.State != "kept" || !strings.HasPrefix(n.Reason, "linked item not resolved") {
		t.Errorf("%+v", n)
	}
}
