package dotnet

import (
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin/plugintest"
	"github.com/cernly/cernly/repo/repotest"
)

func byKey(facts []fact.Fact, kind string) map[string]fact.Fact {
	out := map[string]fact.Fact{}
	for _, f := range facts {
		if f.Kind == kind {
			out[f.Key] = f
		}
	}
	return out
}

func testProject(pkg string) string {
	return `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
` + pkg + `
  </ItemGroup>
</Project>
`
}

func TestTestSuites(t *testing.T) {
	for _, tc := range []struct{ pkg, want string }{
		{`    <PackageReference Include="xunit" Version="2.9.2" />`, "xunit"},
		{`    <PackageReference Include="xunit.v3" Version="1.0.0" />`, "xunit"},
		{`    <PackageReference Include="NUnit" Version="4.2.2" />`, "nunit"},
		{`    <PackageReference Include="MSTest" Version="3.6.1" />`, "mstest"},
		{`    <PackageReference Include="MSTest.TestFramework" Version="3.6.1" />`, "mstest"},
		{`    <PackageReference Include="TUnit" Version="0.2.0" />`, "tunit"},
		{`    <PackageReference Include="FluentAssertions" Version="6.12.1" />`, "unknown"},
	} {
		r := repotest.Files(t, map[string]string{
			"src/Shop/Shop.csproj":                      `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			"src/Shop/Order.cs":                         "class Order {}\n",
			"tests/Shop.Tests/Shop.Tests.csproj":        testProject(tc.pkg),
			"tests/Shop.Tests/OrderTests.cs":            "class OrderTests {}\n",
			"tests/Shop.Tests/Orders/PriceTests.cs":     "class PriceTests {}\n",
			"tests/Shop.Tests/Fixtures/Fixtures.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
			"tests/Shop.Tests/Fixtures/Data.cs":         "class Data {}\n",
			"tests/Shop.Tests/readme.md":                "x\n",
		})
		res := Plugin{}.Run(r)
		suites := byKey(res.Facts, fact.KindTestSuite)
		if len(suites) != 1 {
			t.Fatalf("%s: suites %v", tc.want, suites)
		}
		f, ok := suites["test_suite:csharp:tests/Shop.Tests"]
		if !ok {
			t.Fatalf("%s: no suite for tests/Shop.Tests: %v", tc.want, suites)
		}
		want := map[string]any{"path": "tests/Shop.Tests", "language": "csharp", "framework": tc.want, "files": 2,
			"dirs": []any{"tests/Shop.Tests", "tests/Shop.Tests/Orders"}}
		if !reflect.DeepEqual(f.Attrs, want) {
			t.Errorf("%s: attrs %v, want %v", tc.want, f.Attrs, want)
		}
		if f.Plugin != "dotnet" {
			t.Errorf("plugin %q", f.Plugin)
		}
		if tc.want == "unknown" {
			continue
		}
		found := false
		for _, e := range f.Evidence {
			if e.Path == "tests/Shop.Tests/Shop.Tests.csproj" && e.Line == 4 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no evidence on the PackageReference line: %+v", tc.want, f.Evidence)
		}
	}
}

func TestTestSuitesFSharp(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"tests/Shop.Tests/Shop.Tests.fsproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Shop.Tests/Tests.fs":          "module Tests\n",
	})
	f, ok := byKey(Plugin{}.Run(r).Facts, fact.KindTestSuite)["test_suite:fsharp:tests/Shop.Tests"]
	if !ok || f.Attrs["files"] != 1 {
		t.Fatalf("fsharp suite: %+v", f)
	}
}

func TestImports(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <ProjectReference Include="..\Application\Application.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Application/Application.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <ProjectReference Include="../Domain/Domain.csproj" />
    <ProjectReference Include="../../../Outside/Outside.csproj" />
    <ProjectReference Include="../Missing/Missing.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	got := byKey(Plugin{}.Run(r).Facts, fact.KindImport)
	want := map[string]fact.Fact{
		"import:dotnet:src/Web/Web.csproj:src/Application/Application.csproj": {
			Kind: fact.KindImport, Key: "import:dotnet:src/Web/Web.csproj:src/Application/Application.csproj", Plugin: "dotnet",
			Attrs: map[string]any{"language": "dotnet", "from": "src/Web/Web.csproj", "to": "src/Application/Application.csproj",
				"from_dir": "src/Web", "to_dir": "src/Application"},
			Evidence: []fact.Evidence{{Path: "src/Web/Web.csproj", Line: 3, Unit: "src/Web"}},
		},
		"import:dotnet:src/Application/Application.csproj:src/Domain/Domain.csproj": {
			Kind: fact.KindImport, Key: "import:dotnet:src/Application/Application.csproj:src/Domain/Domain.csproj", Plugin: "dotnet",
			Attrs: map[string]any{"language": "dotnet", "from": "src/Application/Application.csproj", "to": "src/Domain/Domain.csproj",
				"from_dir": "src/Application", "to_dir": "src/Domain"},
			Evidence: []fact.Evidence{{Path: "src/Application/Application.csproj", Line: 3, Unit: "src/Application"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imports:\n got %+v\nwant %+v", got, want)
	}
}

func TestWatches(t *testing.T) {
	for p, want := range map[string]bool{
		"src/Web/Web.csproj":                   true,
		"src/Lib/Lib.fsproj":                   true,
		"src/Old/Old.vbproj":                   true,
		"Directory.Build.props":                true,
		"Directory.Packages.props":             true,
		"src/Web/Program.cs":                   true,
		"src/Web/appsettings.json":             true,
		"src/Web/appsettings.Development.json": true,
		"src/Web/package.json":                 false,
		"src/Web/main.go":                      false,
	} {
		if got := (Plugin{}).Watches(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

func TestSchema(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <ProjectReference Include="..\Domain\Domain.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Domain/Domain.csproj":           `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"tests/Shop.Tests/Shop.Tests.csproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Shop.Tests/OrderTests.cs":     "class OrderTests {}\n",
	})
	data := plugintest.Scan(t, r.Root, Plugin{})
	if err := plugintest.ValidScan(data); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
}

// A non-SDK-style project gets its dependency list and SBOM, nothing more: no
// env, outbound or test facts from its folder, and its files do not fall to
// a project folder above it.
func TestLegacyProjectNoSourceFacts(t *testing.T) {
	const legacy = `<?xml version="1.0" encoding="utf-8"?>
<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003">
  <ItemGroup><Reference Include="System" /><Reference Include="Microsoft.NET.Test.Sdk" /></ItemGroup>
  <PropertyGroup><IsTestProject>true</IsTestProject></PropertyGroup>
</Project>
`
	res := Plugin{}.Run(repotest.Files(t, map[string]string{
		"Root.csproj":                     `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"legacy/Old/Old.csproj":           legacy,
		"legacy/Old/Client.cs":            "class C { void X() { Environment.GetEnvironmentVariable(\"OLD_KEY\"); c.GetAsync(\"https://old.shopvendor.io\"); } }\n",
		"legacy/Old/appsettings.json":     `{"Api": {"BaseUrl": "https://oldcfg.shopvendor.io"}}`,
		"legacy/OldTests/OldTests.csproj": legacy,
		"legacy/OldTests/Tests.cs":        "class T { }\n",
	}))
	for _, f := range res.Facts {
		t.Errorf("legacy folder gave %s", f.Key)
	}
}

// An undecided ProjectReference keeps its condition; one referenced under
// several applies when any holds, and unconditionally when one is.
func TestImportsCondition(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <ProjectReference Include="..\Net8Only\Net8Only.csproj" Condition="'$(TargetFramework)'=='net8.0'" />
    <ProjectReference Include="..\Net472Only\Net472Only.csproj" Condition="'$(TargetFramework)'=='net472'" />
    <ProjectReference Include="..\Both\Both.csproj" Condition="'$(TargetFramework)'=='net8.0'" />
    <ProjectReference Include="..\Both\Both.csproj" Condition="'$(TargetFramework)'=='net472'" />
    <ProjectReference Include="..\Always\Always.csproj" Condition="'$(TargetFramework)'=='net8.0'" />
    <ProjectReference Include="..\Always\Always.csproj" />
  </ItemGroup>
</Project>
`,
		"src/Net8Only/Net8Only.csproj":     `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Net472Only/Net472Only.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Both/Both.csproj":             `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Always/Always.csproj":         `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	})
	got := byKey(Plugin{}.Run(r).Facts, fact.KindImport)
	cond := func(to string) any {
		f, ok := got["import:dotnet:src/App/App.csproj:src/"+to]
		if !ok {
			t.Fatalf("no import to %s: %v", to, got)
		}
		e, _ := f.Attrs["ext"].(map[string]any)
		return e["condition"]
	}
	for to, want := range map[string]any{
		"Net8Only/Net8Only.csproj":     "'$(TargetFramework)'=='net8.0'",
		"Net472Only/Net472Only.csproj": "'$(TargetFramework)'=='net472'",
		"Both/Both.csproj":             "('$(TargetFramework)'=='net8.0') or ('$(TargetFramework)'=='net472')",
		"Always/Always.csproj":         nil,
	} {
		if c := cond(to); c != want {
			t.Errorf("%s: condition %v, want %v", to, c, want)
		}
	}
}
