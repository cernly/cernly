package rules

import (
	"testing"

	"github.com/cernly/cernly/fact"
)

func TestDotnetRunCoversSuitesBelow(t *testing.T) {
	run := fact.Fact{Kind: fact.KindTestRun, Key: "test_run:dotnet:.", Attrs: map[string]any{"tool": "dotnet", "dir": "."}}
	for _, lang := range []string{"csharp", "fsharp", "vb"} {
		suite := fact.Fact{Kind: fact.KindTestSuite, Key: "test_suite:" + lang + ":tests/Shop.Tests", Attrs: map[string]any{"language": lang, "path": "tests/Shop.Tests"}}
		if !runCovers(run, suite) {
			t.Errorf("dotnet run at . should cover a %s suite below it", lang)
		}
	}
	other := fact.Fact{Kind: fact.KindTestSuite, Attrs: map[string]any{"language": "java", "path": "tests/Shop.Tests"}}
	if runCovers(run, other) {
		t.Error("dotnet run must not cover a java suite")
	}
	narrow := fact.Fact{Kind: fact.KindTestRun, Attrs: map[string]any{"tool": "dotnet", "dir": "src"}}
	suite := fact.Fact{Kind: fact.KindTestSuite, Attrs: map[string]any{"language": "csharp", "path": "tests/Shop.Tests"}}
	if runCovers(narrow, suite) {
		t.Error("dotnet run at src must not cover tests/Shop.Tests")
	}
}

func TestDotnetSolutionRunCoversListedProjectsOnly(t *testing.T) {
	suite := func(p string) fact.Fact {
		return fact.Fact{Kind: fact.KindTestSuite, Attrs: map[string]any{"language": "csharp", "path": p}}
	}
	root := fact.Fact{Kind: fact.KindTestRun, Attrs: map[string]any{"tool": "dotnet", "dir": ".", "projects": []any{"src/Shop", "tests/Shop.Tests"}}}
	if !runCovers(root, suite("tests/Shop.Tests")) {
		t.Error("a listed project is covered")
	}
	if runCovers(root, suite("tests/Legacy.Tests")) {
		t.Error("a root solution that omits Legacy.Tests must not cover it")
	}
	build := fact.Fact{Kind: fact.KindTestRun, Attrs: map[string]any{"tool": "dotnet", "dir": "build", "projects": []any{"tests/X"}}}
	if !runCovers(build, suite("tests/X")) {
		t.Error("build/All.sln listing ../tests/X covers tests/X")
	}
	if runCovers(build, suite("build/Y")) {
		t.Error("projects replace the folder rule")
	}
}
