package msbuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/repotest"
)

func projects(t *testing.T, r *repo.Repo) map[string]*Project {
	t.Helper()
	ps, errs := Projects(r)
	if len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	out := map[string]*Project{}
	for _, p := range ps {
		out[p.Path] = p
	}
	return out
}

func pkg(t *testing.T, p *Project, id string) PackageRef {
	t.Helper()
	for _, pr := range p.Packages {
		if pr.ID == id {
			return pr
		}
	}
	t.Fatalf("%s: no package %s in %+v", p.Path, id, p.Packages)
	return PackageRef{}
}

func TestSDKStyle(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Serilog" Version="3.1.1" />
  </ItemGroup>
</Project>
`,
		"src/Old/Old.vbproj": `<?xml version="1.0" encoding="utf-8"?>
<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003">
  <ItemGroup>
    <PackageReference Include="Serilog" Version="3.1.1" />
  </ItemGroup>
</Project>
`,
		"src/Fs/Fs.fsproj": `<Project>
  <Import Project="Sdk.props" Sdk="Microsoft.NET.Sdk" />
</Project>
`,
	})
	ps := projects(t, r)
	app := ps["src/App/App.csproj"]
	if app == nil || !app.SDKStyle || app.Lang != "csharp" || app.Name != "App" || app.Dir != "src/App" {
		t.Fatalf("app: %+v", app)
	}
	if len(app.Packages) != 1 {
		t.Fatalf("app packages: %+v", app.Packages)
	}
	old := ps["src/Old/Old.vbproj"]
	if old == nil || old.SDKStyle || old.Lang != "vb" || len(old.Packages) != 0 {
		t.Fatalf("old: %+v", old)
	}
	fs := ps["src/Fs/Fs.fsproj"]
	if fs == nil || !fs.SDKStyle || fs.Lang != "fsharp" {
		t.Fatalf("fs: %+v", fs)
	}
}

func TestVersionForms(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Build.props": `<Project>
  <PropertyGroup>
    <EfVersion>8.0.4</EfVersion>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Update="Polly" Version="8.2.0" />
  </ItemGroup>
</Project>
`,
		"src/Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Serilog" Version="3.1.1" />
    <PackageReference Include="Dapper">
      <Version>2.1.28</Version>
    </PackageReference>
    <PackageReference Include="Polly" />
    <PackageReference Include="Microsoft.EntityFrameworkCore" Version="$(EfVersion)" />
    <PackageReference Include="Example.Thing" Version="$(Missing)" />
  </ItemGroup>
</Project>
`,
	})
	api := projects(t, r)["src/Api/Api.csproj"]
	for id, want := range map[string]string{"Serilog": "3.1.1", "Dapper": "2.1.28", "Polly": "8.2.0", "Microsoft.EntityFrameworkCore": "8.0.4"} {
		got := pkg(t, api, id)
		if got.Version != want || got.Unresolved {
			t.Errorf("%s: %+v, want %s", id, got, want)
		}
	}
	ef := pkg(t, api, "Microsoft.EntityFrameworkCore")
	if ef.Raw != "$(EfVersion)" || len(ef.Ev) == 0 || ef.Ev[0].Path != "src/Api/Api.csproj" || ef.Ev[0].Line != 8 {
		t.Errorf("ef: %+v", ef)
	}
	polly := pkg(t, api, "Polly")
	if len(polly.Ev) != 2 || polly.Ev[0].Line != 7 || polly.Ev[1].Path != "Directory.Build.props" || polly.Ev[1].Line != 6 {
		t.Errorf("polly evidence: %+v", polly.Ev)
	}
	miss := pkg(t, api, "Example.Thing")
	if !miss.Unresolved || miss.Version != "$(Missing)" || miss.Raw != "$(Missing)" {
		t.Errorf("missing: %+v", miss)
	}
	if api.Props["efversion"] != "8.0.4" {
		t.Errorf("props: %+v", api.Props)
	}
}

func TestConditionalProperty(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <V>1.0.0</V>
  </PropertyGroup>
  <PropertyGroup Condition="'$(Configuration)' == 'Release'">
    <V>2.0.0</V>
  </PropertyGroup>
  <ItemGroup Condition="'$(TargetFramework)' == 'net8.0'">
    <PackageReference Include="A" Version="$(V)" PrivateAssets="all" />
    <PackageReference Include="B" Version="1.0.0">
      <PrivateAssets>All</PrivateAssets>
    </PackageReference>
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	a := pkg(t, app, "A")
	if !a.Unresolved || a.Version != "$(V)" || a.Condition != "'$(TargetFramework)' == 'net8.0'" || !a.PrivateAll {
		t.Errorf("a: %+v", a)
	}
	if b := pkg(t, app, "B"); !b.PrivateAll {
		t.Errorf("b: %+v", b)
	}
	if _, ok := app.Props["v"]; ok {
		t.Errorf("conditional property kept: %+v", app.Props)
	}
}

func TestCentralVersions(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Packages.props": `<Project>
  <PropertyGroup>
    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>
  </PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Serilog" Version="3.1.1" />
    <PackageVersion Include="Dapper" Version="2.1.28" />
    <GlobalPackageReference Include="Example.Analyzers" Version="1.2.3" />
  </ItemGroup>
</Project>
`,
		"src/A/A.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Serilog" />
    <PackageReference Include="Dapper" VersionOverride="2.0.0" />
  </ItemGroup>
</Project>
`,
		"src/B/B.csproj": `<Project Sdk="Microsoft.NET.Sdk">
</Project>
`,
	})
	ps := projects(t, r)
	a := ps["src/A/A.csproj"]
	if !a.CPM {
		t.Fatalf("a not CPM: %+v", a)
	}
	s := pkg(t, a, "Serilog")
	if s.Version != "3.1.1" || s.Unresolved || len(s.Ev) != 2 || s.Ev[0].Line != 3 || s.Ev[1].Path != "Directory.Packages.props" || s.Ev[1].Line != 6 {
		t.Errorf("serilog: %+v", s)
	}
	if d := pkg(t, a, "Dapper"); d.Version != "2.0.0" || len(d.Ev) != 1 {
		t.Errorf("dapper: %+v", d)
	}
	for _, p := range []*Project{a, ps["src/B/B.csproj"]} {
		g := pkg(t, p, "Example.Analyzers")
		if !g.Global || g.Version != "1.2.3" || len(g.Ev) != 1 || g.Ev[0].Path != "Directory.Packages.props" || g.Ev[0].Line != 8 {
			t.Errorf("%s global: %+v", p.Path, g)
		}
	}
}

func TestPropsChainImport(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Build.props": `<Project>
  <PropertyGroup>
    <Base>1.0.0</Base>
    <Over>root</Over>
  </PropertyGroup>
</Project>
`,
		"src/Directory.Build.props": `<Project>
  <Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)../'))" />
  <PropertyGroup>
    <Over>src</Over>
  </PropertyGroup>
</Project>
`,
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="A" Version="$(Base)" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["src/App/App.csproj"]
	if a := pkg(t, app, "A"); a.Version != "1.0.0" {
		t.Errorf("a: %+v", a)
	}
	if app.Props["over"] != "src" {
		t.Errorf("props: %+v", app.Props)
	}
}

func TestPropsStopAtRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "Directory.Build.props"), []byte(`<Project>
  <PropertyGroup><EfVersion>8.0.4</EfVersion></PropertyGroup>
</Project>
`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := repotest.Write(t, filepath.Join(outer, "repo"), map[string]string{
		"Directory.Build.props": `<Project>
  <Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)../'))" />
</Project>
`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Microsoft.EntityFrameworkCore" Version="$(EfVersion)" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	if ef := pkg(t, app, "Microsoft.EntityFrameworkCore"); !ef.Unresolved {
		t.Errorf("resolved from outside the repo: %+v", ef)
	}
}

func TestProjectRefBackslash(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <ProjectReference Include="..\Domain\Domain.csproj" />
    <ProjectReference Include="..\..\..\Elsewhere\Other.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	api := projects(t, r)["src/Api/Api.csproj"]
	if len(api.ProjectRefs) != 2 {
		t.Fatalf("refs: %+v", api.ProjectRefs)
	}
	if got := api.ProjectRefs[0]; got.Include != `..\Domain\Domain.csproj` || got.Path != "src/Domain/Domain.csproj" || got.Line != 3 {
		t.Errorf("ref 0: %+v", got)
	}
	if got := api.ProjectRefs[1]; got.Path != "" || got.Line != 4 {
		t.Errorf("ref 1: %+v", got)
	}
}

func TestBOMAndCRLF(t *testing.T) {
	content := "\xEF\xBB\xBF<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n" +
		"<Project Sdk=\"Microsoft.NET.Sdk\">\r\n" +
		"\r\n" +
		"  <PropertyGroup>\r\n" +
		"    <TargetFramework>net8.0</TargetFramework>\r\n" +
		"  </PropertyGroup>\r\n" +
		"  <ItemGroup><PackageReference Include=\"Serilog\" Version=\"3.1.1\" /></ItemGroup>\r\n" +
		"</Project>\r\n"
	r := repotest.Files(t, map[string]string{"App/App.csproj": content})
	app := projects(t, r)["App/App.csproj"]
	s := pkg(t, app, "Serilog")
	if len(s.Ev) != 1 || s.Ev[0].Line != 7 {
		t.Errorf("serilog evidence: %+v", s.Ev)
	}
	if app.Props["targetframework"] != "net8.0" {
		t.Errorf("props: %+v", app.Props)
	}
}

func TestIsTest(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"A.Tests/A.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.9.0" />
  </ItemGroup>
</Project>
`,
		"B.Tests/B.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <isTestProject>true</isTestProject>
  </PropertyGroup>
</Project>
`,
		"C/C.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	ps := projects(t, r)
	if !ps["A.Tests/A.Tests.csproj"].IsTest || !ps["B.Tests/B.Tests.csproj"].IsTest || ps["C/C.csproj"].IsTest {
		t.Errorf("IsTest wrong: %v %v %v", ps["A.Tests/A.Tests.csproj"].IsTest, ps["B.Tests/B.Tests.csproj"].IsTest, ps["C/C.csproj"].IsTest)
	}
}

func TestParseError(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Bad/Bad.csproj":   "<Project Sdk=\"Microsoft.NET.Sdk\">\n<ItemGroup>\n</Project>\n",
		"Good/Good.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	ps, errs := Projects(r)
	if len(ps) != 1 || ps[0].Path != "Good/Good.csproj" {
		t.Errorf("projects: %+v", ps)
	}
	if len(errs) != 1 || errs[0].Path != "Bad/Bad.csproj" || errs[0].Line == 0 {
		t.Errorf("errors: %+v", errs)
	}
}

func TestPackagesConfig(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Old/packages.config": `<?xml version="1.0" encoding="utf-8"?>
<packages>
  <package id="Newtonsoft.Json" version="13.0.3" targetFramework="net48" />

  <package id="Example.Tools" version="1.0.0" developmentDependency="true" />
</packages>
`,
	})
	got, err := PackagesConfig(r, "Old/packages.config")
	if err != nil {
		t.Fatal(err)
	}
	want := []PackagesConfigEntry{
		{ID: "Newtonsoft.Json", Version: "13.0.3", Line: 3},
		{ID: "Example.Tools", Version: "1.0.0", Dev: true, Line: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestConditionalVersionSources(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Packages.props": `<Project>
  <PropertyGroup>
    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>
  </PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Example.Ef" Version="8.0.0" Condition="'$(TargetFramework)'=='net8.0'" />
    <PackageVersion Include="Example.Ef" Version="6.0.0" Condition="'$(TargetFramework)'=='net6.0'" />
    <PackageVersion Include="Example.Plain" Version="1.0.0" />
  </ItemGroup>
</Project>
`,
		"Directory.Build.props": `<Project>
  <ItemGroup Condition="'$(Configuration)'=='Release'">
    <PackageReference Update="Example.Plain" Version="2.0.0" />
  </ItemGroup>
</Project>
`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Example.Ef" />
    <PackageReference Include="Example.Plain" />
    <PackageReference Include="Example.Pinned" Version="3.0.0" />
    <PackageReference Update="Example.Pinned" Version="4.0.0" Condition="'$(Configuration)'=='Debug'" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	want := map[string][]Candidate{
		"Example.Ef": {
			{Raw: "8.0.0", Condition: "'$(TargetFramework)'=='net8.0'", Ev: fact.Evidence{Path: "Directory.Packages.props", Line: 6}},
			{Raw: "6.0.0", Condition: "'$(TargetFramework)'=='net6.0'", Ev: fact.Evidence{Path: "Directory.Packages.props", Line: 7}},
		},
		"Example.Plain": {
			{Raw: "1.0.0", Ev: fact.Evidence{Path: "Directory.Packages.props", Line: 8}},
			{Raw: "2.0.0", Condition: "'$(Configuration)'=='Release'", Ev: fact.Evidence{Path: "Directory.Build.props", Line: 3}},
		},
		"Example.Pinned": {
			{Raw: "3.0.0", Ev: fact.Evidence{Path: "App/App.csproj", Line: 5}},
			{Raw: "4.0.0", Condition: "'$(Configuration)'=='Debug'", Ev: fact.Evidence{Path: "App/App.csproj", Line: 6}},
		},
	}
	for id, cands := range want {
		got := pkg(t, app, id)
		if !got.Unresolved || got.Version != "" || got.Raw != "" || got.Condition != "" || !reflect.DeepEqual(got.Candidates, cands) {
			t.Errorf("%s: %+v, want unresolved with candidates %+v", id, got, cands)
		}
		if len(got.Ev) != 1+len(cands) && !(id == "Example.Pinned" && len(got.Ev) == 2) {
			t.Errorf("%s: evidence %+v", id, got.Ev)
		}
	}
}

func TestPropsCaseInsensitive(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <EXAMPLEVER>1.2.3</EXAMPLEVER>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="A" Version="$(ExampleVer)" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	if app.Props["targetframework"] != "net8.0" {
		t.Errorf("props: %+v", app.Props)
	}
	if _, ok := app.Props["TargetFramework"]; ok {
		t.Errorf("key not lower-cased: %+v", app.Props)
	}
	if a := pkg(t, app, "A"); a.Version != "1.2.3" || a.Unresolved {
		t.Errorf("a: %+v", a)
	}
}

// Windows resolves paths without regard to case: a ProjectReference spelled in
// another case resolves to the one tracked project that matches, and stays as
// written when two match.
func TestProjectRefCase(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <ProjectReference Include="..\domain\domain.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	api := projects(t, r)["src/Api/Api.csproj"]
	if got := api.ProjectRefs[0].Path; got != "src/Domain/Domain.csproj" {
		t.Errorf("ref path %q, want src/Domain/Domain.csproj", got)
	}
	// Two tracked projects that differ only in case (git allows it; this
	// file system may not, so the list is given directly).
	fold := foldMatch([]string{"src/Twin/Twin.csproj", "src/TWIN/Twin.csproj", "src/One/One.csproj"})
	if got := fold("src/twin/twin.csproj"); got != "src/twin/twin.csproj" {
		t.Errorf("ambiguous: %q", got)
	}
	if got := fold("src/one/one.csproj"); got != "src/One/One.csproj" {
		t.Errorf("single: %q", got)
	}
}

// A root props file adds test packages under a condition on IsTestProject: the
// condition is decided per project once every property is read.
func TestConditionDecided(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Build.props": `<Project>
  <ItemGroup Condition="'$(IsTestProject)'=='true'">
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.9.0" />
    <PackageReference Include="Moq" Version="4.20.0" />
  </ItemGroup>
  <ItemGroup Condition="'$(MSBuildProjectName.EndsWith(&apos;.Tests&apos;))' == 'true'">
    <PackageReference Include="Example.Suffix" Version="1.0.0" />
  </ItemGroup>
  <ItemGroup Condition="'$(UseExample)' != 'TRUE' and 'yes' == '$(Flag)'">
    <PackageReference Include="Example.Both" Version="1.0.0" />
  </ItemGroup>
</Project>
`,
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <Flag>Yes</Flag>
  </PropertyGroup>
</Project>
`,
		"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <IsTestProject>true</IsTestProject>
    <UseExample>true</UseExample>
  </PropertyGroup>
</Project>
`,
	})
	ps := projects(t, r)
	app, tests := ps["src/App/App.csproj"], ps["tests/App.Tests/App.Tests.csproj"]
	if app.IsTest {
		t.Error("prod project is a test project")
	}
	for _, pr := range app.Packages {
		if pr.ID == "Microsoft.NET.Test.Sdk" || pr.ID == "Moq" {
			t.Errorf("prod project has %s", pr.ID)
		}
	}
	if !tests.IsTest {
		t.Error("test project not a test project")
	}
	for _, id := range []string{"Microsoft.NET.Test.Sdk", "Moq"} {
		if pr := pkg(t, tests, id); pr.Condition != "" || pr.Unresolved {
			t.Errorf("%s: %+v, want unconditional", id, pr)
		}
	}
	if pr := pkg(t, app, "Example.Both"); pr.Condition != "" {
		t.Errorf("both: %+v, want decided true", pr)
	}
	for _, pr := range tests.Packages {
		if pr.ID == "Example.Both" {
			t.Errorf("test project has Example.Both")
		}
	}
	for _, p := range []*Project{app, tests} {
		if pr := pkg(t, p, "Example.Suffix"); pr.Condition == "" {
			t.Errorf("%s: suffix condition decided: %+v", p.Path, pr)
		}
	}
}

// A Test.Sdk reference under a condition that cannot be decided does not make
// a test project; neither does a property set under a condition, nor one the
// SDK or MSBuild sets, nor one no file of the repo sets (the environment may).
func TestConditionUndecided(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"A/A.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup Condition="$(MSBuildProjectName.EndsWith('.Tests'))">
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.9.0" />
  </ItemGroup>
</Project>
`,
		"B/B.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup Condition="'$(Configuration)' == 'Debug'">
    <Mode>x</Mode>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Mode" Version="1.0.0" Condition="'$(Mode)' == ''" />
    <PackageReference Include="Tfm" Version="1.0.0" Condition="'$(TargetFramework)' == ''" />
    <PackageReference Include="Or" Version="1.0.0" Condition="'$(Unset)' == 'x' or '$(Unset)' == ''" />
    <PackageReference Include="Num" Version="1.0.0" Condition="'$(N)' == '1'" />
    <PackageReference Include="Env" Version="1.0.0" Condition="'$(CI)'=='true'" />
  </ItemGroup>
  <PropertyGroup>
    <N>1.0</N>
  </PropertyGroup>
</Project>
`,
	})
	ps := projects(t, r)
	a := ps["A/A.csproj"]
	if a.IsTest {
		t.Error("undecided Test.Sdk made a test project")
	}
	if pr := pkg(t, a, "Microsoft.NET.Test.Sdk"); pr.Condition == "" {
		t.Errorf("condition lost: %+v", pr)
	}
	b := ps["B/B.csproj"]
	for _, id := range []string{"Mode", "Tfm", "Or", "Num", "Env"} {
		if pr := pkg(t, b, id); pr.Condition == "" {
			t.Errorf("%s: condition decided: %+v", id, pr)
		}
	}
}

func ids(p *Project) map[string]PackageRef {
	out := map[string]PackageRef{}
	for _, pr := range p.Packages {
		out[pr.ID] = pr
	}
	return out
}

const chooseTestProps = `<Project>
  <Choose>
    <When Condition="'$(IsTestProject)'=='true'">
      <ItemGroup>
        <PackageReference Include="Moq" Version="4.20.0" />
      </ItemGroup>
    </When>
    <Otherwise>
      <ItemGroup>
        <PackageReference Include="ProdOnly" Version="1.0.0" />
      </ItemGroup>
    </Otherwise>
  </Choose>
</Project>
`

// The first true When wins and Otherwise applies only when every When is
// false. A Choose is decided where it stands, with the properties set before
// it, as MSBuild decides it in the property pass.
func TestChooseFirstTrueWins(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Build.props": chooseTestProps,
		"tests/Directory.Build.props": `<Project>
  <PropertyGroup>
    <IsTestProject>true</IsTestProject>
  </PropertyGroup>
  <Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)../'))" />
</Project>
`,
		"tests/T/T.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/P/P.csproj":   `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Late/Late.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <IsTestProject>true</IsTestProject>
  </PropertyGroup>
</Project>
`,
	})
	ps := projects(t, r)
	tp, pp, late := ids(ps["tests/T/T.csproj"]), ids(ps["src/P/P.csproj"]), ids(ps["src/Late/Late.csproj"])
	if m, ok := tp["Moq"]; !ok || m.Condition != "" {
		t.Errorf("test project Moq: %+v", tp)
	}
	if _, ok := tp["ProdOnly"]; ok {
		t.Errorf("test project has ProdOnly: %+v", tp)
	}
	if m, ok := pp["ProdOnly"]; !ok || m.Condition != "" {
		t.Errorf("prod project ProdOnly: %+v", pp)
	}
	if _, ok := pp["Moq"]; ok {
		t.Errorf("prod project has Moq: %+v", pp)
	}
	// The project body comes after Directory.Build.props: its IsTestProject is
	// not set yet when the props Choose is decided.
	if _, ok := late["ProdOnly"]; !ok || len(late) != 1 {
		t.Errorf("late: %+v, want ProdOnly only", late)
	}
}

func TestChooseTwoTrueAndAllFalse(t *testing.T) {
	choose := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <A>%s</A>
    <B>%s</B>
  </PropertyGroup>
  <Choose>
    <When Condition="'$(A)'=='alpha'">
      <PropertyGroup><Picked>a</Picked></PropertyGroup>
      <ItemGroup><PackageReference Include="First" Version="1.0.0" /></ItemGroup>
    </When>
    <When Condition="'$(B)'=='beta'">
      <ItemGroup><PackageReference Include="Second" Version="1.0.0" /></ItemGroup>
    </When>
    <Otherwise>
      <ItemGroup><PackageReference Include="Other" Version="1.0.0" /></ItemGroup>
    </Otherwise>
  </Choose>
</Project>
`
	r := repotest.Files(t, map[string]string{
		"Both/Both.csproj": fmt.Sprintf(choose, "alpha", "beta"),
		"None/None.csproj": fmt.Sprintf(choose, "zeta", "zeta"),
	})
	ps := projects(t, r)
	both, none := ps["Both/Both.csproj"], ps["None/None.csproj"]
	if got := ids(both); len(got) != 1 || got["First"].ID == "" || got["First"].Condition != "" {
		t.Errorf("both: %+v, want First only, unconditional", got)
	}
	if both.Props["picked"] != "a" {
		t.Errorf("property in the chosen When not set: %+v", both.Props)
	}
	if got := ids(none); len(got) != 1 || got["Other"].ID == "" || got["Other"].Condition != "" {
		t.Errorf("none: %+v, want Other only, unconditional", got)
	}
}

// After an undecided When, every later branch depends on it being false: its
// condition carries that, and is never decided.
func TestChooseUndecided(t *testing.T) {
	choose := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <A>%s</A>
    <B>zeta</B>
  </PropertyGroup>
  <Choose>
    <When Condition="'$(CI)'=='true'">
      <ItemGroup><PackageReference Include="Ci" Version="1.0.0" /></ItemGroup>
    </When>
    <When Condition="'$(B)'=='beta'">
      <ItemGroup><PackageReference Include="Never" Version="1.0.0" /></ItemGroup>
    </When>
    <When Condition="'$(A)'=='alpha'">
      <ItemGroup><PackageReference Include="Later" Version="1.0.0" /></ItemGroup>
    </When>
    <Otherwise>
      <ItemGroup><PackageReference Include="Other" Version="1.0.0" /></ItemGroup>
    </Otherwise>
  </Choose>
</Project>
`
	r := repotest.Files(t, map[string]string{
		"One/One.csproj": fmt.Sprintf(choose, "alpha"),
		"Two/Two.csproj": fmt.Sprintf(choose, "zeta"),
	})
	ps := projects(t, r)
	one, two := ids(ps["One/One.csproj"]), ids(ps["Two/Two.csproj"])
	for _, got := range []map[string]PackageRef{one, two} {
		if c := got["Ci"].Condition; c != "'$(CI)'=='true'" {
			t.Errorf("Ci condition %q", c)
		}
		if _, ok := got["Never"]; ok {
			t.Errorf("decided false When walked: %+v", got)
		}
	}
	if c := one["Later"].Condition; c == "" || !strings.Contains(c, "'$(CI)'=='true'") {
		t.Errorf("Later: %+v, want the undecided When's negation", one["Later"])
	}
	if _, ok := one["Other"]; ok {
		t.Errorf("Otherwise walked after a true When: %+v", one)
	}
	if _, ok := two["Later"]; ok {
		t.Errorf("false When walked: %+v", two)
	}
	if c := two["Other"].Condition; c == "" || !strings.Contains(c, "'$(CI)'=='true'") {
		t.Errorf("Other: %+v, want the undecided When's negation", two["Other"])
	}
}

// A negated group is one operand: an and inside it never decides the whole.
func TestConditionNegatedGroup(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><A>alpha</A><B>zeta</B></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Neg" Version="1.0.0" Condition="!('$(A)'=='alpha' and '$(B)'=='beta' and '$(A)'=='alpha')" />
  </ItemGroup>
</Project>
`,
	})
	if pr := pkg(t, projects(t, r)["App/App.csproj"], "Neg"); pr.Condition == "" {
		t.Errorf("negated group decided: %+v", pr)
	}
}

func TestProjectRefCondition(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><Flavor>full</Flavor></PropertyGroup>
  <ItemGroup>
    <ProjectReference Include="..\Net8Only\Net8Only.csproj" Condition="'$(TargetFramework)'=='net8.0'" />
    <ProjectReference Include="..\Net472Only\Net472Only.csproj" Condition="'$(TargetFramework)'=='net472'" />
    <ProjectReference Include="..\Lite\Lite.csproj" Condition="'$(Flavor)'=='lite'" />
    <ProjectReference Include="..\Full\Full.csproj" Condition="'$(Flavor)'=='full'" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	got := map[string]string{}
	for _, ref := range app.ProjectRefs {
		got[ref.Include] = ref.Condition
	}
	want := map[string]string{
		`..\Net8Only\Net8Only.csproj`:     "'$(TargetFramework)'=='net8.0'",
		`..\Net472Only\Net472Only.csproj`: "'$(TargetFramework)'=='net472'",
		`..\Full\Full.csproj`:             "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs %v, want %v", got, want)
	}
}

// Which of several conditional version sources applies is never guessed: the
// version stays empty and every candidate is listed. A decided condition
// settles its candidate; the last unconditional source overrides the ones
// before it.
func TestConditionalVersionCandidates(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Packages.props": `<Project>
  <PropertyGroup>
    <ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally>
    <Flavor>full</Flavor>
  </PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Example.Decided" Version="1.0.0" Condition="'$(Flavor)'=='lite'" />
    <PackageVersion Include="Example.Decided" Version="2.0.0" Condition="'$(Flavor)'=='full'" />
    <PackageVersion Include="Example.Base" Version="1.0.0" />
    <PackageVersion Include="Example.Base" Version="9.0.0" Condition="'$(Flavor)'=='lite'" />
  </ItemGroup>
</Project>
`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Example.Decided" />
    <PackageReference Include="Example.Base" />
  </ItemGroup>
</Project>
`,
	})
	app := projects(t, r)["App/App.csproj"]
	if d := pkg(t, app, "Example.Decided"); d.Unresolved || d.Version != "2.0.0" || len(d.Candidates) != 0 || len(d.Ev) != 2 || d.Ev[1].Line != 8 {
		t.Errorf("decided: %+v", d)
	}
	if b := pkg(t, app, "Example.Base"); b.Unresolved || b.Version != "1.0.0" || len(b.Candidates) != 0 {
		t.Errorf("base: %+v", b)
	}
}

// NuGet matches PackageVersion to a reference after evaluation: a version-less
// reference in Directory.Build.props, read before Directory.Packages.props,
// still gets its central version.
func TestCentralVersionAfterEvaluation(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"Directory.Build.props": `<Project>
  <ItemGroup>
    <PackageReference Include="Example.Analyzer" PrivateAssets="all" />
  </ItemGroup>
</Project>
`,
		"Directory.Packages.props": `<Project>
  <PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
  <ItemGroup>
    <PackageVersion Include="Example.Analyzer" Version="3.3.4" />
  </ItemGroup>
</Project>
`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	a := pkg(t, projects(t, r)["App/App.csproj"], "Example.Analyzer")
	want := []fact.Evidence{{Path: "Directory.Build.props", Line: 3}, {Path: "Directory.Packages.props", Line: 4}}
	if a.Unresolved || a.Version != "3.3.4" || len(a.Candidates) != 0 || !reflect.DeepEqual(a.Ev, want) {
		t.Errorf("analyzer: %+v", a)
	}
}

// A Choose with thousands of undecided branches must not grow every later
// condition without bound.
func TestChooseBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<Project Sdk="Microsoft.NET.Sdk"><Choose>`)
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, `<When Condition="'$(Cfg%d)' == 'x'"><ItemGroup><PackageReference Include="P%d" Version="1.0" /></ItemGroup></When>`, i, i)
	}
	b.WriteString(`</Choose>`)
	// False branches do not lengthen any condition: a true one after many
	// of them is still the branch taken.
	b.WriteString(`<Choose>`)
	for i := 0; i < 200; i++ {
		b.WriteString(`<When Condition="'a' == 'b'"></When>`)
	}
	b.WriteString(`<When Condition="'a' == 'a'"><ItemGroup><PackageReference Include="Taken" Version="1.0" /></ItemGroup></When></Choose>`)
	b.WriteString(`</Project>`)
	r := repotest.Files(t, map[string]string{"App/App.csproj": b.String()})
	ps, errs := Projects(r)
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "rest skipped") {
		t.Errorf("errors = %+v, want one cap error", errs)
	}
	out, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 1<<20 {
		t.Fatalf("output %d bytes", len(out))
	}
	if n := len(ps[0].Packages); n != maxChooseBranches+1 {
		t.Errorf("packages = %d, want %d: the first undecided branches and Taken", n, maxChooseBranches+1)
	}
	if last := ps[0].Packages[len(ps[0].Packages)-1]; last.ID != "Taken" {
		t.Errorf("last package = %s, want Taken", last.ID)
	}
}
