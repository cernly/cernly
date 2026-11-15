package rules

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/plugins/dotnet"
	"github.com/cernly/cernly/repo"
)

// dnRepo writes files to a folder outside git and opens it.
func dnRepo(t *testing.T, files map[string]string) *repo.Repo {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// dnCommit commits files straight into a git index, so paths that differ
// only in case can sit side by side on a file system that folds case.
func dnCommit(t *testing.T, files map[string]string) *repo.Repo {
	t.Helper()
	dir := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "-q")
	for p, content := range files {
		sha := git(content, "hash-object", "-w", "--stdin")
		git("", "update-index", "--add", "--cacheinfo", "100644,"+sha+","+p)
	}
	git("", "commit", "-q", "-m", "x")
	r, _, err := repo.OpenCommit(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func dnProj(refs ...string) string {
	var b strings.Builder
	b.WriteString("<Project Sdk=\"Microsoft.NET.Sdk\">\n  <ItemGroup>\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "    <ProjectReference Include=\"%s\" />\n", r)
	}
	b.WriteString("  </ItemGroup>\n</Project>\n")
	return b.String()
}

// narrowRepo runs the dotnet plugin over r and narrows a recipe that
// publishes src/Web, with every project folder a code unit.
func narrowRepo(t *testing.T, r *repo.Repo) (Narrowing, []string) {
	t.Helper()
	return narrowEntry(t, r, "src/Web")
}

// narrowEntry is narrowRepo for a recipe that publishes the project in entry.
func narrowEntry(t *testing.T, r *repo.Repo, entry string) (Narrowing, []string) {
	t.Helper()
	// A self edge of src/Extra makes sure the graph exists even when the
	// plugin gives no other import, so a dropped reference shows as an exclusion.
	facts := append(dotnet.Plugin{}.Run(r).Facts, dnImport("src/Extra", "src/Extra"))
	for _, f := range r.Files {
		if strings.HasSuffix(f, ".csproj") {
			facts = append(facts, dnUnit(filepath.ToSlash(filepath.Dir(f))))
		}
	}
	n, _, ex := narrow(dnRecipe([]any{dnEntry(entry)}), []string{"."}, facts)
	return n, ex
}

// A ProjectReference the reader cannot resolve to a tracked project keeps the
// link; one it resolves narrows it with the referenced project inside.
func TestNarrowProjectReferences(t *testing.T) {
	lib := dnProj()
	cases := []struct {
		name  string
		files map[string]string
		state string
		want  string // the reason when kept, the excluded folders when narrowed
	}{
		{"unknown property", map[string]string{"src/Web/Web.csproj": dnProj(`$(SolutionDir)src\Lib\Lib.csproj`)},
			"kept", `linked item not resolved: $(SolutionDir)src\Lib\Lib.csproj`},
		{"this file directory", map[string]string{"src/Web/Web.csproj": dnProj(`$(MSBuildThisFileDirectory)..\Lib\Lib.csproj`)},
			"narrowed", "[src/Extra]"},
		{"case differs, one match", map[string]string{"src/Web/Web.csproj": dnProj(`..\lib\lib.csproj`)},
			"narrowed", "[src/Extra]"},
		{"list", map[string]string{"src/Web/Web.csproj": dnProj(`..\Lib\Lib.csproj;..\Extra\Extra.csproj`)},
			"narrowed", "[]"},
		{"missing project", map[string]string{"src/Web/Web.csproj": dnProj(`..\Lib\Lib.csproj`, `..\Missing\Missing.csproj`)},
			"kept", "linked item not resolved: src/Missing/Missing.csproj"},
		{"leaves the repo", map[string]string{"src/Web/Web.csproj": dnProj(`..\Lib\Lib.csproj`, `..\..\..\Outside\Outside.csproj`)},
			"narrowed", "[src/Extra]"},
		{"in Directory.Build.props", map[string]string{"src/Web/Web.csproj": dnProj(),
			"Directory.Build.props": "<Project>\n  <ItemGroup>\n    <ProjectReference Include=\"$(MSBuildThisFileDirectory)src\\Lib\\Lib.csproj\" />\n  </ItemGroup>\n</Project>\n"},
			"narrowed", "[src/Extra]"},
		{"in an imported file, relative to the project", map[string]string{
			"src/Web/Web.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <Import Project=\"..\\..\\eng\\refs.targets\" />\n</Project>\n",
			"eng/refs.targets":   "<Project>\n  <ItemGroup>\n    <ProjectReference Include=\"..\\Lib\\Lib.csproj\" />\n  </ItemGroup>\n</Project>\n"},
			"narrowed", "[src/Extra]"},
		{"in an imported file, missing for the project", map[string]string{
			"src/Web/Web.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <Import Project=\"..\\..\\eng\\refs.targets\" />\n</Project>\n",
			"eng/refs.targets":   "<Project>\n  <ItemGroup>\n    <ProjectReference Include=\"..\\src\\Lib\\Lib.csproj\" />\n  </ItemGroup>\n</Project>\n"},
			"kept", `linked item not resolved: ..\src\Lib\Lib.csproj`}, // as written in the file
		{"in Directory.Build.props, leaves the repo", map[string]string{"src/Web/Web.csproj": dnProj(),
			"Directory.Build.props": "<Project>\n  <ItemGroup>\n    <ProjectReference Include=\"..\\..\\..\\Lib\\Lib.csproj\" />\n  </ItemGroup>\n</Project>\n"},
			"kept", `linked item not resolved: ..\..\..\Lib\Lib.csproj`},
	}
	for _, c := range cases {
		files := map[string]string{"src/Lib/Lib.csproj": lib, "src/Extra/Extra.csproj": dnProj()}
		for k, v := range c.files {
			files[k] = v
		}
		n, ex := narrowRepo(t, dnRepo(t, files))
		got := n.Reason
		if n.State == NarrowingNarrowed {
			got = fmt.Sprint(ex)
		}
		if n.State != c.state || got != c.want {
			t.Errorf("%s: %s %q, want %s %q", c.name, n.State, got, c.state, c.want)
		}
	}
}

// Two tracked projects that differ only in case: the reference matches both
// case-insensitively, so it is not resolved and the link is kept.
func TestNarrowProjectReferenceCaseAmbiguous(t *testing.T) {
	r := dnCommit(t, map[string]string{
		"src/Web/Web.csproj":     dnProj(`..\lib\lib.csproj`),
		"src/Lib/Lib.csproj":     dnProj(),
		"src/LIB/Lib.csproj":     dnProj(),
		"src/Extra/Extra.csproj": dnProj(),
	})
	n, ex := narrowRepo(t, r)
	if n.State != NarrowingKept || n.Reason != "linked item not resolved: src/lib/lib.csproj" {
		t.Errorf("%+v %v", n, ex)
	}
}

// Items in a props file resolve against each importing project's folder, as
// MSBuild does: the folder they name is in the closure, never excluded.
func TestNarrowPropsItems(t *testing.T) {
	proj := "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <ItemGroup>\n    <PackageReference Include=\"Example.Lib\" Version=\"1.0.0\" />\n  </ItemGroup>\n</Project>\n"
	props := func(include string) string {
		return "<Project>\n  <ItemGroup>\n    <Compile Include=\"" + include + "\" />\n  </ItemGroup>\n</Project>\n"
	}
	cases := []struct {
		name  string
		files map[string]string
		entry string
		state string
		want  string // the reason when kept, the excluded folders when narrowed
	}{
		{"relative to the project", map[string]string{"src/Directory.Build.props": props(`..\Shared\Contracts.cs`),
			"src/Api/Api.csproj": proj, "src/Shared/Shared.csproj": proj},
			"src/Api", "narrowed", "[]"},
		{"this file directory", map[string]string{"src/Directory.Build.props": props(`$(MSBuildThisFileDirectory)Shared\Contracts.cs`),
			"src/Api/Api.csproj": proj, "src/Shared/Shared.csproj": proj},
			"src/Api", "narrowed", "[]"},
		{"root props, outside the project folders", map[string]string{"Directory.Build.props": props(`..\..\Common\X.cs`),
			"src/Api/Api.csproj": proj, "src/Shared/Shared.csproj": proj},
			"src/Api", "narrowed", "[src/Shared]"},
		{"root props, leaves the repo", map[string]string{"Directory.Build.props": props(`..\..\Common\X.cs`),
			"Api/Api.csproj": proj, "Shared/Shared.csproj": proj},
			"Api", "kept", `linked item not resolved: ..\..\Common\X.cs`},
	}
	for _, c := range cases {
		n, ex := narrowEntry(t, dnRepo(t, c.files), c.entry)
		got := n.Reason
		if n.State == NarrowingNarrowed {
			got = fmt.Sprint(ex)
		}
		if n.State != c.state || got != c.want {
			t.Errorf("%s: %s %q, want %s %q", c.name, n.State, got, c.state, c.want)
		}
	}
}
