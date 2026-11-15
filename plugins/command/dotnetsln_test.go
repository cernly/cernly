package command

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
)

func dotnetRepo(t *testing.T, files map[string]string) *repo.Repo {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runsOf(res []fact.Fact) map[string]fact.Fact {
	out := map[string]fact.Fact{}
	for _, f := range res {
		if f.Kind == fact.KindTestRun {
			out[f.Key] = f
		}
	}
	return out
}

func TestTestRunProjectsFromSolution(t *testing.T) {
	sln := "Project(\"{X}\") = \"A\", \"..\\tests\\A\\A.csproj\", \"{1}\"\nEndProject\n"
	r := dotnetRepo(t, map[string]string{
		".github/workflows/ci.yml": "on: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n" +
			"      - run: dotnet test eng/All.sln\n" +
			"      - run: dotnet test Missing.sln\n" +
			"      - run: dotnet test tests/A/A.csproj\n",
		"eng/All.sln":         sln,
		"tests/A/A.csproj":    "<Project/>",
		"tests/A/UnitTest.cs": "class T{}",
	})
	res := Plugin{}.Run(r)
	runs := runsOf(res.Facts)
	all := runs["test_run:dotnet:eng"]
	if got, _ := all.Attrs["projects"].([]any); !reflect.DeepEqual(got, []any{"tests/A"}) {
		t.Errorf("projects = %v", all.Attrs["projects"])
	}
	if all.Attrs["dir"] != "eng" {
		t.Errorf("dir = %v", all.Attrs["dir"])
	}
	if _, ok := runs["test_run:dotnet:Missing.sln"]; ok {
		t.Error("unexpected key")
	}
	if p, ok := runs["test_run:dotnet:."].Attrs["projects"]; ok {
		t.Errorf("an untracked solution must give no projects, got %v", p)
	}
	if _, ok := runs["test_run:dotnet:tests/A"].Attrs["projects"]; ok {
		t.Error("a project target must give no projects")
	}
}

func TestTestRunUnreadableSolution(t *testing.T) {
	r := dotnetRepo(t, map[string]string{
		".github/workflows/ci.yml": "on: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - run: dotnet test Shop.slnx\n",
		"Shop.slnx":                "<Solution><Project",
	})
	res := Plugin{}.Run(r)
	if _, ok := runsOf(res.Facts)["test_run:dotnet:."].Attrs["projects"]; ok {
		t.Error("unreadable solution must give no projects")
	}
	found := false
	for _, e := range res.Errors {
		if e.Path == ".github/workflows/ci.yml" {
			found = true
		}
	}
	if !found {
		t.Errorf("want a location error, got %v", res.Errors)
	}
}
