package rules

import (
	"testing"

	"github.com/cernly/cernly/fact"
)

func TestDotnetRunCoversSuitesBelow(t *testing.T) {
	run := fact.Fact{Kind: fact.KindTestRun, Key: "test_run:dotnet:.", Attrs: map[string]any{"tool": "dotnet", "dir": "."}}
	for _, lang := range []string{"csharp", "fsharp", "vb"} {
		suite := fact.Fact{Kind: fact.KindTestSuite, Key: "test_suite:" + lang + ":tests/Shop.Tests", Attrs: map[string]any{"language": lang, "path": "tests/Shop.Tests"}}
		if !covers(run, suite) {
			t.Errorf("dotnet run at . should cover a %s suite below it", lang)
		}
	}
	other := fact.Fact{Kind: fact.KindTestSuite, Attrs: map[string]any{"language": "java", "path": "tests/Shop.Tests"}}
	if covers(run, other) {
		t.Error("dotnet run must not cover a java suite")
	}
	narrow := fact.Fact{Kind: fact.KindTestRun, Attrs: map[string]any{"tool": "dotnet", "dir": "src"}}
	suite := fact.Fact{Kind: fact.KindTestSuite, Attrs: map[string]any{"language": "csharp", "path": "tests/Shop.Tests"}}
	if covers(narrow, suite) {
		t.Error("dotnet run at src must not cover tests/Shop.Tests")
	}
}
