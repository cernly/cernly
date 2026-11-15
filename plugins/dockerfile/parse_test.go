package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
)

func runDocker(t *testing.T, files map[string]string) []fact.Fact {
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
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	return res.Facts
}

// finalRecipe returns the attrs of the final stage's recipe.
func finalRecipe(t *testing.T, facts []fact.Fact) map[string]any {
	t.Helper()
	for _, f := range facts {
		if f.Attrs["final"] == true {
			return f.Attrs
		}
	}
	t.Fatal("no final recipe")
	return nil
}

func dotnetEntries(attrs map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range attrs["entrypoints"].([]any) {
		m := e.(map[string]any)
		if m["kind"] == "dotnet" {
			out = append(out, m)
		}
	}
	return out
}

const csproj = "<Project Sdk=\"Microsoft.NET.Sdk\"></Project>"

func dotnetDockerfile(run string, workdir string) string {
	return "FROM mcr.microsoft.com/dotnet/sdk:8.0\nWORKDIR /src\nCOPY . .\nWORKDIR " + workdir + "\nRUN " + run + "\n"
}

func TestDotnetPublishProject(t *testing.T) {
	facts := runDocker(t, map[string]string{
		"Dockerfile":                   dotnetDockerfile("dotnet publish src/Shop.Web/Shop.Web.csproj -c Release -o /app", "/src"),
		"src/Shop.Web/Shop.Web.csproj": csproj,
	})
	es := dotnetEntries(finalRecipe(t, facts))
	if len(es) != 1 {
		t.Fatalf("entries = %v", es)
	}
	e := es[0]
	if e["path"] != "src/Shop.Web/Shop.Web.csproj" || e["resolved"] != true || !reflect.DeepEqual(e["projects"], []any{"src/Shop.Web"}) {
		t.Errorf("entry = %v", e)
	}
}

func TestDotnetPublishFolderAndSolution(t *testing.T) {
	files := map[string]string{
		"one/One.csproj": csproj,
		"two/A.csproj":   csproj,
		"two/B.csproj":   csproj,
		"Shop.sln": "Project(\"{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}\") = \"A\", \"two\\A.csproj\", \"{1}\"\nEndProject\n" +
			"Project(\"{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}\") = \"One\", \"one\\One.csproj\", \"{2}\"\nEndProject\n",
		"src/Shop.Web/Shop.Web.csproj": csproj,
	}
	cases := []struct {
		run, workdir string
		resolved     bool
		path         string
		projects     []any
	}{
		{"dotnet publish one", "/src", true, "one", []any{"one"}},
		{"dotnet publish two", "/src", false, "two", nil},
		{"dotnet build Shop.sln", "/src", true, "Shop.sln", []any{"one", "two"}},
		{"dotnet publish", "/src/src/Shop.Web", true, "src/Shop.Web", []any{"src/Shop.Web"}},
		{"dotnet publish $PROJECT", "/src", false, "", nil},
	}
	for _, c := range cases {
		f := map[string]string{"Dockerfile": dotnetDockerfile(c.run, c.workdir)}
		for k, v := range files {
			f[k] = v
		}
		es := dotnetEntries(finalRecipe(t, runDocker(t, f)))
		if len(es) != 1 {
			t.Fatalf("%s: entries = %v", c.run, es)
		}
		e := es[0]
		if e["resolved"] != c.resolved || (c.projects != nil && !reflect.DeepEqual(e["projects"], c.projects)) || (c.projects == nil && e["projects"] != nil) {
			t.Errorf("%s: entry = %v", c.run, e)
		}
		if c.resolved && e["path"] != c.path {
			t.Errorf("%s: path = %v", c.run, e["path"])
		}
	}
}

func TestDotnetValueFlags(t *testing.T) {
	df := "FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build\nWORKDIR /src\nCOPY . .\n" +
		"RUN dotnet publish -c Release -r linux-x64 --self-contained -p:Version=1.0 -o out src/A/A.csproj\n" +
		"FROM mcr.microsoft.com/dotnet/aspnet:8.0\nCOPY --from=build /src/out /app\n"
	facts := runDocker(t, map[string]string{"Dockerfile": df, "src/A/A.csproj": csproj})
	es := dotnetEntries(finalRecipe(t, facts))
	if len(es) != 1 || es[0]["path"] != "src/A/A.csproj" || es[0]["resolved"] != true {
		t.Fatalf("entries = %v", es)
	}
	// The output /src/out is what the final stage copies; another output is not.
	df2 := "FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build\nWORKDIR /src\nCOPY . .\n" +
		"RUN dotnet publish -o out src/A/A.csproj\n" +
		"FROM mcr.microsoft.com/dotnet/aspnet:8.0\nCOPY --from=build /src/other /app\n"
	facts = runDocker(t, map[string]string{"Dockerfile": df2, "src/A/A.csproj": csproj})
	if es := dotnetEntries(finalRecipe(t, facts)); len(es) != 0 {
		t.Errorf("uncovered output kept: %v", es)
	}
}

func otherCommands(attrs map[string]any) any { return attrs["other_commands"] }

func TestOtherCommands(t *testing.T) {
	files := func(run string) map[string]string {
		return map[string]string{"Dockerfile": dotnetDockerfile(run, "/src"), "x.csproj": csproj}
	}
	if got := otherCommands(finalRecipe(t, runDocker(t, files("apt-get update && apt-get install -y curl && dotnet restore && dotnet publish x.csproj")))); got != nil {
		t.Errorf("setup and builds: %v", got)
	}
	if got := otherCommands(finalRecipe(t, runDocker(t, files("make build")))); !reflect.DeepEqual(got, []any{"make"}) {
		t.Errorf("make: %v", got)
	}
	if got := otherCommands(finalRecipe(t, runDocker(t, files("./build.sh")))); !reflect.DeepEqual(got, []any{"./build.sh"}) {
		t.Errorf("script: %v", got)
	}
	if got := otherCommands(finalRecipe(t, runDocker(t, files("FOO=1 dotnet test && sh -c \"make\"")))); !reflect.DeepEqual(got, []any{"dotnet test", "sh"}) {
		t.Errorf("words: %v", got)
	}
	unreached := "FROM rust AS tools\nRUN cargo build\nFROM alpine\nRUN echo hi\n"
	if got := otherCommands(finalRecipe(t, runDocker(t, map[string]string{"Dockerfile": unreached}))); got != nil {
		t.Errorf("unreached stage: %v", got)
	}
	reached := "FROM rust AS tools\nRUN cargo build\nFROM alpine\nCOPY --from=tools /out /out\n"
	if got := otherCommands(finalRecipe(t, runDocker(t, map[string]string{"Dockerfile": reached}))); !reflect.DeepEqual(got, []any{"cargo"}) {
		t.Errorf("reached stage: %v", got)
	}
}

// CRLF line endings: BuildKit's parser joins continuations and keeps lines.
func TestCRLF(t *testing.T) {
	res, err := parse([]byte("FROM node:20 AS build\r\nRUN npm ci \\\r\n  && npm run build\r\nFROM nginx\r\nCOPY --from=build /a \\\r\n  /b\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range res.AST.Children {
		got = append(got, fmt.Sprintf("%d-%d %s %q", n.StartLine, n.EndLine, n.Value, args(n)))
	}
	want := `[1-1 FROM ["node:20" "AS" "build"] 2-3 RUN ["npm ci   && npm run build"] 4-4 FROM ["nginx"] 5-6 COPY ["/a" "/b"]]`
	if fmt.Sprint(got) != want {
		t.Errorf("got %s", got)
	}
}

// An ADD URL keeps no credentials, also when an ARG puts them there.
func TestRemoteCredentials(t *testing.T) {
	res, err := parse([]byte("ARG TOKEN=SECRET2\nFROM alpine:3.20\nARG TOKEN\nADD https://u:SECRET1@files.acme.dev/ca.pem /etc/ca.pem\nADD https://${TOKEN}@files.acme.dev/key /key\n"))
	if err != nil {
		t.Fatal(err)
	}
	st := readStages(res)
	var got []string
	for _, r := range st[len(st)-1].remote {
		got = append(got, r.url)
	}
	if len(got) != 2 || got[0] != "https://files.acme.dev/ca.pem" || got[1] != "https://files.acme.dev/key" {
		t.Errorf("remote = %q", got)
	}
}

func TestOtherCommandsMarkers(t *testing.T) {
	cases := []struct {
		name, df string
		want     any
	}{
		{"unparsable", "FROM alpine\nRUN if then fi (\n", []any{"if"}},
		{"heredoc other command", "FROM alpine\nRUN python3 <<EOF\nprint(1)\nEOF\n", []any{"python3"}},
		{"heredoc bare", "FROM alpine\nRUN <<EOF\nmake all\nEOF\n", []any{"make"}},
		{"heredoc bash", "FROM alpine\nRUN bash -e <<EOF\napt-get update\nEOF\n", nil},
		{"powershell shell", "FROM alpine\nSHELL [\"powershell\", \"-Command\"]\nRUN Install-Thing\n", []any{"powershell"}},
		{"powershell exe path", "FROM alpine\nSHELL [\"C:\\\\Windows\\\\System32\\\\powershell.exe\", \"-Command\"]\nRUN Install-Thing\n", []any{"powershell"}},
		{"bash shell", "FROM alpine\nSHELL [\"/bin/bash\", \"-c\"]\nRUN apt-get update\n", nil},
	}
	for _, c := range cases {
		got := otherCommands(finalRecipe(t, runDocker(t, map[string]string{"Dockerfile": c.df})))
		if !reflect.DeepEqual(got, c.want) && !(got == nil && c.want == nil) {
			t.Errorf("%s: other_commands = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDotnetSameCommandTwoStages(t *testing.T) {
	df := "FROM mcr.microsoft.com/dotnet/sdk:8.0 AS api\nWORKDIR /src\nCOPY . .\nWORKDIR /src/Api\nRUN dotnet publish -c Release -o /app\n" +
		"FROM mcr.microsoft.com/dotnet/sdk:8.0 AS worker\nWORKDIR /src\nCOPY . .\nWORKDIR /src/Worker\nRUN dotnet publish -c Release -o /app\n" +
		"FROM mcr.microsoft.com/dotnet/aspnet:8.0\nCOPY --from=api /app /app/api\nCOPY --from=worker /app /app/worker\n"
	facts := runDocker(t, map[string]string{"Dockerfile": df, "Api/Api.csproj": csproj, "Worker/Worker.csproj": csproj})
	es := dotnetEntries(finalRecipe(t, facts))
	if len(es) != 2 {
		t.Fatalf("entries = %v", es)
	}
}

func TestDotnetFlagForms(t *testing.T) {
	for _, run := range []string{
		"dotnet publish -p Version=1 --self-contained true src/A/A.csproj",
		"dotnet publish --self-contained false -p:X=Y src/A/A.csproj",
	} {
		facts := runDocker(t, map[string]string{"Dockerfile": dotnetDockerfile(run, "/src"), "src/A/A.csproj": csproj})
		es := dotnetEntries(finalRecipe(t, facts))
		if len(es) != 1 || es[0]["path"] != "src/A/A.csproj" || es[0]["resolved"] != true {
			t.Errorf("%s: entries = %v", run, es)
		}
	}
}

// Run reads solutions and which project files exist to resolve dotnet
// entrypoints, so a change to them can change the output.
func TestWatches(t *testing.T) {
	for p, want := range map[string]bool{
		"Dockerfile": true, "src/Web/Dockerfile": true, "Shop.sln": true, "Shop.slnx": true, "SHOP.SLN": true,
		"src/A/A.csproj": true, "src/B/B.fsproj": true, "src/C/C.vbproj": true,
		"src/A/Program.cs": false, "package.json": false,
	} {
		if got := (Plugin{}).Watches(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

func TestDotnetCdBeforeBuildIsUnresolved(t *testing.T) {
	files := map[string]string{
		"src/Shared/Shared.csproj": csproj,
		"src/Api/Api.csproj":       csproj,
	}
	for _, run := range []string{
		"cd ../Api && dotnet publish -c Release",
		"cd ../Api; dotnet build",
		"(cd ../Api && dotnet publish)",
	} {
		f := map[string]string{"Dockerfile": dotnetDockerfile(run, "/src/src/Shared")}
		for k, v := range files {
			f[k] = v
		}
		es := dotnetEntries(finalRecipe(t, runDocker(t, f)))
		if len(es) != 1 || es[0]["resolved"] != false || es[0]["projects"] != nil {
			t.Errorf("%s: entries = %v", run, es)
		}
	}
	// A cd after the build, or none, changes nothing.
	f := map[string]string{"Dockerfile": dotnetDockerfile("dotnet publish && cd /app", "/src/src/Shared"), "src/Shared/Shared.csproj": csproj}
	es := dotnetEntries(finalRecipe(t, runDocker(t, f)))
	if len(es) != 1 || es[0]["resolved"] != true || es[0]["path"] != "src/Shared" {
		t.Errorf("cd after build: %v", es)
	}
}

func TestDotnetValuedFlagsAndExtraArgs(t *testing.T) {
	files := map[string]string{"src/Api/Api.csproj": csproj, "src/Other/Other.csproj": csproj}
	for run, want := range map[string]bool{
		"dotnet publish --artifacts-path /out src/Api/Api.csproj":  true,
		"dotnet build -tl off src/Api/Api.csproj":                  true,
		"dotnet publish -bl src/Api/Api.csproj":                    true,
		"dotnet publish src/Api/Api.csproj src/Other/Other.csproj": false,
	} {
		f := map[string]string{"Dockerfile": dotnetDockerfile(run, "/src")}
		for k, v := range files {
			f[k] = v
		}
		es := dotnetEntries(finalRecipe(t, runDocker(t, f)))
		if len(es) != 1 || es[0]["resolved"] != want || (want && !reflect.DeepEqual(es[0]["projects"], []any{"src/Api"})) {
			t.Errorf("%s: %v", run, es)
		}
	}
}
