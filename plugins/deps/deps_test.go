package deps

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/scaling"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/repotest"
)

func internalOf(facts []fact.Fact) map[string]string {
	out := map[string]string{}
	for _, f := range facts {
		if in, ok := f.Attrs["internal"].(map[string]any); ok {
			out[f.Key] = in["path"].(string) + " " + in["spec"].(string)
		} else {
			out[f.Key] = ""
		}
	}
	return out
}

// Workspace members are found by their package name, not their folder; file:
// specs resolve against the manifest; everything else stays third-party.
func TestNpmInternal(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"package.json":                  `{"name":"root","workspaces":["frontend","packages/*"]}`,
		"frontend/package.json":         `{"name":"frontend","dependencies":{"@acme/ui":"*","react":"19.0.0","tools":"file:../tools"}}`,
		"packages/ui-kit/package.json":  `{"name":"@acme/ui","dependencies":{"@acme/session":"workspace:*"}}`,
		"packages/session/package.json": `{"name":"@acme/session"}`,
		"tools/package.json":            `{"name":"tools"}`,
		"other/package.json":            `{"name":"other","dependencies":{"@acme/ui":"1.0.0"}}`,
	})
	got := internalOf(NpmPlugin{}.Run(r).Facts)
	for k, want := range map[string]string{
		"dependency:npm:frontend:@acme/ui":             "packages/ui-kit *",
		"dependency:npm:frontend:react":                "",
		"dependency:npm:frontend:tools":                "tools file:../tools",
		"dependency:npm:packages/ui-kit:@acme/session": "packages/session workspace:*",
		"dependency:npm:other:@acme/ui":                "", // not a member of any workspace: the registry package
	} {
		if got[k] != want {
			t.Errorf("%s internal = %q, want %q", k, got[k], want)
		}
	}
}

func TestGoInternal(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"api/go.mod":    "module example.com/api\n\ngo 1.24\n\nrequire (\n\texample.com/lib v0.0.0\n\texample.com/shared v0.1.0\n\tgithub.com/x/y v1.2.3\n)\n\nreplace example.com/lib => ../lib\n",
		"lib/go.mod":    "module example.com/lib\n\ngo 1.24\n",
		"shared/go.mod": "module example.com/shared\n\ngo 1.24\n",
		"go.work":       "go 1.24\n\nuse (\n\t./api\n\t./shared\n)\n",
	})
	got := internalOf(GoPlugin{}.Run(r).Facts)
	for k, want := range map[string]string{
		"dependency:go:api:example.com/lib":    "lib replace",
		"dependency:go:api:example.com/shared": "shared go.work",
		"dependency:go:api:github.com/x/y":     "",
	} {
		if got[k] != want {
			t.Errorf("%s internal = %q, want %q", k, got[k], want)
		}
	}
}

func TestGoScope(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"api/go.mod":        "module example.com/api\n\ngo 1.24\n\nrequire (\n\tgithub.com/labstack/echo/v4 v4.1.0\n\tgithub.com/stretchr/testify v1.9.0\n\tgithub.com/x/tool v1.0.0\n)\n",
		"api/main.go":       "package main\n\nimport \"github.com/labstack/echo/v4/middleware\"\n\nvar _ = middleware.Logger\n",
		"api/main_test.go":  "package main\n\nimport \"github.com/stretchr/testify/require\"\n\nvar _ = require.True\n",
		"api/testdata/x.go": "package x\n\nimport \"github.com/x/tool\"\n",
	})
	got := map[string]string{}
	for _, f := range (GoPlugin{}).Run(r).Facts {
		got[f.Attrs["name"].(string)] = f.Attrs["scope"].(string)
	}
	want := map[string]string{"github.com/labstack/echo/v4": "prod", "github.com/stretchr/testify": "test", "github.com/x/tool": "unknown"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s scope = %q, want %q", k, got[k], v)
		}
	}
}

func TestPythonRequirements(t *testing.T) {
	for in, want := range map[string]string{
		"Django>=4.2,<5": "django >=4.2,<5",
		"requests[socks] == 2.32.3 ; python_version<'3.13'": "requests ==2.32.3",
		"zope.interface": "zope-interface ",
		"pkg @ https://example.org/pkg-1.0.tar.gz": "pkg https://example.org/pkg-1.0.tar.gz",
		"Foo_Bar (>=1.0)":                          "foo-bar >=1.0",
	} {
		name, spec, ok := parseRequirement(in)
		if got := name + " " + spec; !ok || got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	src := "# comment\n-r base.txt\n--index-url https://pypi.org/simple\n-e ./local\nflask==3.1.0 \\\n    --hash=sha256:abc \\\n    --hash=sha256:def\ngunicorn  # web server\n\n-c constraints.txt\n"
	var got []string
	for _, l := range parseReqFile(src) {
		got = append(got, fmt.Sprintf("%d:%s:%s:%s", l.line, l.include, l.name, l.spec))
	}
	if fmt.Sprint(got) != "[2:base.txt:: 5::flask:==3.1.0 8::gunicorn:]" {
		t.Errorf("requirements lines = %v", got)
	}
	got = nil
	for _, l := range parseReqFile(strings.ReplaceAll(src, "\n", "\r\n")) {
		got = append(got, fmt.Sprintf("%d:%s:%s:%s", l.line, l.include, l.name, l.spec))
	}
	if fmt.Sprint(got) != "[2:base.txt:: 5::flask:==3.1.0 8::gunicorn:]" {
		t.Errorf("CRLF requirements lines = %v", got)
	}
}

func TestPyproject(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"pyproject.toml": "[tool.poetry.dependencies]\npython = \"^3.12\"\nDjango = \"^5.0\"\ncelery = { version = \"5.4.0\", extras = [\"redis\"] }\n\n[tool.poetry.group.test.dependencies]\npytest = \"^8\"\n",
		"poetry.lock":    "[[package]]\nname = \"django\"\nversion = \"5.1.4\"\n\n[[package]]\nname = \"celery\"\nversion = \"5.4.0\"\n\n[[package]]\nname = \"pytest\"\nversion = \"8.3.4\"\n\n[[package]]\nname = \"pytest\"\nversion = \"8.2.0\"\n",
	})
	res := PythonPlugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, f := range res.Facts {
		got = append(got, fmt.Sprintf("%s=%s/%s@%d", f.Attrs["name"], f.Attrs["version"], f.Attrs["scope"], f.Evidence[0].Line))
	}
	sort.Strings(got)
	if fmt.Sprint(got) != "[celery=5.4.0/prod@4 django=5.1.4/prod@3 pytest=^8/test@7]" {
		t.Errorf("poetry = %v (python left out; a package locked at two versions keeps its declared spec)", got)
	}
}

// transitiveOf lists the transitive facts as key -> "version scope roots via
// line license".
func transitiveOf(facts []fact.Fact) map[string]string {
	out := map[string]string{}
	for _, f := range facts {
		if f.Attrs["direct"] != false {
			continue
		}
		lic, _ := f.Attrs["license"].(string)
		out[f.Key] = fmt.Sprintf("%v %v %v %v %d %s", f.Attrs["version"], f.Attrs["scope"], f.Attrs["roots"], f.Attrs["via"], f.Evidence[0].Line, lic)
	}
	return out
}

func checkMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(got) != len(want) {
		t.Errorf("got %d, want %d: %v", len(got), len(want), keys)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

// npm: nested installs resolve before hoisted ones, a workspace link is
// walked but never reported, dev comes from the lockfile, the licence is the
// lockfile's, direct facts get it too.
func TestNpmTransitive(t *testing.T) {
	lock := `{
  "lockfileVersion": 3,
  "packages": {
    "": { "workspaces": ["web", "ui"] },
    "web": { "name": "web", "dependencies": { "express": "^4", "ui": "*" }, "devDependencies": { "vitest": "^2" } },
    "ui": { "name": "ui", "dependencies": { "clsx": "^2" } },
    "node_modules/ui": { "resolved": "ui", "link": true },
    "node_modules/express": { "version": "4.21.2", "license": "MIT", "dependencies": { "qs": "6.13.0", "debug": "2.6.9" } },
    "node_modules/express/node_modules/debug": { "version": "2.6.9", "license": "MIT", "dependencies": { "ms": "2.0.0" } },
    "node_modules/debug": { "version": "4.3.7", "license": "MIT" },
    "node_modules/ms": { "version": "2.0.0", "license": "MIT" },
    "node_modules/qs": { "version": "6.13.0", "license": "BSD-3-Clause" },
    "node_modules/clsx": { "version": "2.1.1", "license": "MIT" },
    "node_modules/vitest": { "version": "2.1.0", "dev": true, "license": "MIT", "dependencies": { "tinyspy": "3" } },
    "node_modules/tinyspy": { "version": "3.0.2", "dev": true }
  }
}`
	r := repotest.Files(t, map[string]string{
		"package.json":      `{"name":"root","workspaces":["web","ui"]}`,
		"web/package.json":  `{"name":"web","dependencies":{"express":"^4","ui":"*"},"devDependencies":{"vitest":"^2"}}`,
		"ui/package.json":   `{"name":"ui","dependencies":{"clsx":"^2"}}`,
		"package-lock.json": lock,
	})
	res := NpmPlugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	checkMap(t, transitiveOf(res.Facts), map[string]string{
		"dependency:npm:.:lock:node_modules/express/node_modules/debug": "2.6.9 prod [web] [express] 9 MIT",
		"dependency:npm:.:lock:node_modules/ms":                         "2.0.0 prod [web] [debug] 11 MIT",
		"dependency:npm:.:lock:node_modules/qs":                         "6.13.0 prod [web] [express] 12 BSD-3-Clause",
		"dependency:npm:.:lock:node_modules/clsx":                       "2.1.1 prod [web] [] 13 MIT",
		"dependency:npm:.:lock:node_modules/tinyspy":                    "3.0.2 dev [web] [vitest] 15 ",
	})
	for _, f := range res.Facts {
		if f.Key == "dependency:npm:web:express" && (f.Attrs["license"] != "MIT" || f.Attrs["version"] != "4.21.2") {
			t.Errorf("direct express = %v", f.Attrs)
		}
		if f.Key == "dependency:npm:web:ui" && f.Attrs["license"] != nil {
			t.Errorf("internal ui has a licence: %v", f.Attrs)
		}
	}
}

// Go: // indirect requirements are transitive, rooted at the module folder.
func TestGoIndirect(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"svc/go.mod": "module example.com/svc\n\ngo 1.23\n\nrequire github.com/labstack/echo/v4 v4.16.1\n\nrequire golang.org/x/crypto v0.31.0 // indirect\n",
	})
	checkMap(t, transitiveOf(GoPlugin{}.Run(r).Facts), map[string]string{
		"dependency:go:svc:golang.org/x/crypto": "v0.31.0 unknown [svc] [] 7 ",
	})
}

// Python: uv.lock and poetry.lock walk the graph; the project itself is
// internal; Pipfile.lock has no graph, so every other package is transitive.
func TestPythonTransitive(t *testing.T) {
	uv := `version = 1

[[package]]
name = "app"
version = "0.1.0"
source = { editable = "." }
dependencies = [{ name = "fastapi" }]

[[package]]
name = "fastapi"
version = "0.115.0"
source = { registry = "https://pypi.org/simple" }
dependencies = [{ name = "starlette" }, { name = "Pydantic" }]

[[package]]
name = "pydantic"
version = "2.9.2"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "starlette"
version = "0.38.6"
source = { registry = "https://pypi.org/simple" }
`
	r := repotest.Files(t, map[string]string{
		"api/pyproject.toml":   "[project]\nname = \"app\"\ndependencies = [\"fastapi>=0.115\"]\n",
		"api/uv.lock":          uv,
		"old/requirements.txt": "flask==3.0.3\n",
		"old/Pipfile.lock":     `{"default": {"flask": {"version": "==3.0.3"}, "werkzeug": {"version": "==3.0.4"}}, "develop": {"pytest": {"version": "==8.3.3"}}}`,
	})
	res := PythonPlugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	checkMap(t, transitiveOf(res.Facts), map[string]string{
		"dependency:pypi:api:lock:pydantic":  "2.9.2 prod [api] [fastapi] 16 ",
		"dependency:pypi:api:lock:starlette": "0.38.6 prod [api] [fastapi] 21 ",
		"dependency:pypi:old:lock:werkzeug":  "3.0.4 prod [old] [] 1 ",
		"dependency:pypi:old:lock:pytest":    "8.3.3 dev [old] [] 1 ",
	})
}

// A credential in a dependency URL never reaches a fact: the manifest's spec,
// a lockfile entry, requirements and pyproject direct references, and the
// index options pip reads.
func TestNoCredentials(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"package.json":          `{"dependencies":{"a":"git+https://user:SECRET1@github.com/acme/a.git#v1","b":"https://SECRET2@npm.acme.dev/b-1.0.tgz","c":"git+ssh://git@github.com/acme/c.git"}}`,
		"web/package.json":      `{"dependencies":{"d":"git+https://SECRET3@github.com/acme/d.git"}}`,
		"web/package-lock.json": `{"packages":{"node_modules/d":{"version":"git+https://x:SECRET4@github.com/acme/d.git#abc","resolved":"git+https://x:SECRET4@github.com/acme/d.git#abc"}}}`,
		"py/requirements.txt":   "--index-url https://user:SECRET5@pypi.acme.dev/simple\n--extra-index-url=https://SECRET6@pypi.acme.dev/simple\nlib @ git+https://oauth2:SECRET7@gitlab.com/acme/lib.git@v2\n",
		"py/pyproject.toml":     "[project]\ndependencies = [\"tool @ git+https://SECRET8@github.com/acme/tool.git\"]\n",
		// A transitive dependency from a lockfile too.
		"t/package.json":      `{"dependencies":{"f":"^1"}}`,
		"t/package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"f":"^1"}},"node_modules/f":{"version":"1.0.0","dependencies":{"e":"*"}},"node_modules/e":{"version":"git+https://x:SECRET9@github.com/acme/e.git#def"}}}`,
	})
	var facts []fact.Fact
	for _, p := range []plugin.Plugin{NpmPlugin{}, PythonPlugin{}} {
		res := p.Run(r)
		if len(res.Errors) > 0 {
			t.Fatal(res.Errors)
		}
		facts = append(facts, res.Facts...)
	}
	out, _ := json.Marshal(facts)
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("credential in facts: %s", out)
	}
	got := map[string]any{}
	for _, f := range facts {
		got[f.Attrs["name"].(string)] = f.Attrs["version"]
	}
	for name, want := range map[string]string{
		"a":    "git+https://github.com/acme/a.git#v1",
		"b":    "https://npm.acme.dev/b-1.0.tgz",
		"c":    "git+ssh://git@github.com/acme/c.git",
		"d":    "git+https://github.com/acme/d.git#abc",
		"lib":  "git+https://gitlab.com/acme/lib.git@v2",
		"tool": "git+https://github.com/acme/tool.git",
		"e":    "git+https://github.com/acme/e.git#def",
	} {
		if got[name] != want {
			t.Errorf("%s version = %v, want %q", name, got[name], want)
		}
	}
}

// A compiled requirements.txt (uv pip compile, pip-compile) is a lockfile:
// only the packages with -r among their # via lines are direct.
func TestCompiledRequirements(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"svc/requirements.in": "requests==2.32.5\n",
		"svc/requirements.txt": `# This file was autogenerated by uv via the following command:
#    uv pip compile requirements.in -o requirements.txt
certifi==2024.7.4
    # via requests
idna==3.7
    # via
    #   requests
    #   -c constraints.txt
requests==2.32.5
    # via
    #   -r requirements.in
    #   google-api-core
urllib3==2.2.2
`,
	})
	res := PythonPlugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, f := range res.Facts {
		got = append(got, fmt.Sprintf("%s direct=%v via=%v line=%d", f.Attrs["name"], f.Attrs["direct"], f.Attrs["via"], f.Evidence[0].Line))
	}
	want := "[certifi direct=false via=[requests] line=3 idna direct=false via=[requests] line=5 requests direct=true via=<nil> line=9 urllib3 direct=true via=<nil> line=13]"
	if fmt.Sprint(got) != want {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func nugetFacts(t *testing.T, files map[string]string) map[string]fact.Fact {
	t.Helper()
	res := NugetPlugin{}.Run(repotest.Files(t, files))
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	out := map[string]fact.Fact{}
	for _, f := range res.Facts {
		out[f.Key] = f
	}
	return out
}

func extOf(f fact.Fact) map[string]any {
	e, _ := f.Attrs["ext"].(map[string]any)
	return e
}

func TestNugetDirect(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"src/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
		"src/Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Serilog" Version="4.0.0" />
    <PackageReference Include="Microsoft.EntityFrameworkCore.Design" Version="8.0.1" PrivateAssets="all" />
    <ProjectReference Include="..\Domain\Domain.csproj" />
    <ProjectReference Include="..\..\..\Outside\Outside.csproj" />
  </ItemGroup>
</Project>`,
	})
	s := got["dependency:nuget:src/Web/Web.csproj:serilog"]
	if s.Plugin != "deps-nuget" || s.Attrs["scope"] != "prod" || s.Attrs["version"] != "4.0.0" || s.Attrs["name"] != "Serilog" ||
		s.Attrs["ecosystem"] != "nuget" || s.Attrs["direct"] != true {
		t.Fatalf("serilog: %+v", s)
	}
	if vi := s.Attrs["version_info"].(map[string]any); vi["scheme"] != "semver" {
		t.Fatalf("version_info: %v", vi)
	}
	if len(s.Evidence) != 1 || s.Evidence[0].Path != "src/Web/Web.csproj" || s.Evidence[0].Line != 3 {
		t.Fatalf("evidence: %+v", s.Evidence)
	}
	if got["dependency:nuget:src/Web/Web.csproj:microsoft.entityframeworkcore.design"].Attrs["scope"] != "build" {
		t.Fatal("EF design not build")
	}
	d := got["dependency:nuget:src/Web/Web.csproj:project:src/Domain/Domain.csproj"]
	in, _ := d.Attrs["internal"].(map[string]any)
	if in["name"] != "Domain" || in["path"] != "src/Domain" || in["spec"] != `..\Domain\Domain.csproj` {
		t.Fatalf("internal: %+v", d.Attrs)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 facts (the outside reference gives none), got %d", len(got))
	}
}

func TestNugetTestScope(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"tests/A.Tests/A.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
<PackageReference Include="xunit" Version="2.9.0" />
<PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.0" />
</ItemGroup></Project>`,
	})
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	for k, f := range got {
		if f.Attrs["scope"] != "test" {
			t.Errorf("%s scope %v", k, f.Attrs["scope"])
		}
	}
}

func TestNugetVersions(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"Directory.Packages.props": `<Project><PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup><ItemGroup><GlobalPackageReference Include="Analyzer.Example" Version="1.0.0" /></ItemGroup></Project>`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
<ItemGroup>
<PackageReference Include="Range" Version="[1.0,2.0)" />
<PackageReference Include="Float" Version="1.*" />
<PackageReference Include="Prop" Version="$(MissingVer)" />
<PackageReference Include="None" />
<PackageReference Include="Cond" Version="3.0.0" Condition="'$(TargetFramework)' == 'net8.0'" />
</ItemGroup></Project>`,
	})
	k := func(id string) fact.Fact { return got["dependency:nuget:App/App.csproj:"+id] }
	for id, want := range map[string]string{"range": "[1.0,2.0)", "float": "1.*"} {
		f := k(id)
		if f.Attrs["version"] != want || f.Attrs["version_info"].(map[string]any)["scheme"] != "unknown" {
			t.Errorf("%s: %+v", id, f.Attrs)
		}
	}
	if extOf(k("prop"))["unresolved"] != true || k("prop").Attrs["version"] != "$(MissingVer)" {
		t.Errorf("prop: %+v", k("prop").Attrs)
	}
	if n := k("none"); n.Attrs["version"] != "" || extOf(n)["unresolved"] != true {
		t.Errorf("none: %+v", n.Attrs)
	}
	if c := k("cond"); extOf(c)["condition"] != "'$(TargetFramework)' == 'net8.0'" || extOf(c)["unresolved"] != nil {
		t.Errorf("cond: %+v", c.Attrs)
	}
	g := k("analyzer.example")
	if extOf(g)["global"] != true || g.Evidence[0].Path != "Directory.Packages.props" {
		t.Errorf("global: %+v", g)
	}
	// NuGet adds a GlobalPackageReference with PrivateAssets="All": build only.
	if g.Attrs["scope"] != "build" {
		t.Errorf("global scope = %v, want build", g.Attrs["scope"])
	}
	if extOf(k("range")) != nil {
		t.Errorf("plain entry has ext: %v", extOf(k("range")))
	}
}

func TestPackagesConfig(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"Legacy/Legacy.csproj": `<?xml version="1.0" encoding="utf-8"?>
<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003"><ItemGroup><Reference Include="System" /></ItemGroup></Project>`,
		"Legacy/packages.config": `<?xml version="1.0" encoding="utf-8"?>
<packages>
  <package id="Newtonsoft.Json" version="13.0.3" targetFramework="net48" />
  <package id="StyleCop.Analyzers" version="1.1.118" targetFramework="net48" developmentDependency="true" />
</packages>`,
	})
	n := got["dependency:nuget:Legacy/Legacy.csproj:newtonsoft.json"]
	if n.Attrs["direct"] != true || extOf(n)["packages_config"] != true || n.Attrs["scope"] != "prod" || n.Attrs["version"] != "13.0.3" ||
		n.Evidence[0].Path != "Legacy/packages.config" || n.Evidence[0].Line != 3 {
		t.Fatalf("newtonsoft: %+v", n)
	}
	if len(nugetFacts(t, map[string]string{
		"Sdk/Sdk.csproj":      `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"Sdk/packages.config": `<packages><package id="Ignored" version="1.0.0" /></packages>`,
	})) != 0 {
		t.Fatal("SDK-style project must not read packages.config")
	}
	s := got["dependency:nuget:Legacy/Legacy.csproj:stylecop.analyzers"]
	if s.Attrs["scope"] != "build" || extOf(s)["packages_config"] != true {
		t.Fatalf("stylecop: %+v", s)
	}
}

func TestLockScopes(t *testing.T) {
	g := &lockGraph{path: "x.lock", dir: "p", eco: "ex", plugin: "deps-ex", nodes: map[string]*lockNode{}}
	for _, id := range []string{"a", "t", "b", "m", "d", "x", "tb", "dt"} {
		g.nodes[id] = &lockNode{id: id, name: id, version: "1", line: 1}
	}
	// shared children: m reached from prod and test, x from dev and test.
	g.nodes["rp"] = &lockNode{id: "rp", name: "rp", deps: []string{"m", "a"}}
	g.nodes["rt"] = &lockNode{id: "rt", name: "rt", deps: []string{"m", "x", "t", "tb"}}
	g.nodes["rb"] = &lockNode{id: "rb", name: "rb", deps: []string{"b", "tb"}}
	g.nodes["rd"] = &lockNode{id: "rd", name: "rd", deps: []string{"x", "d", "dt"}}
	g.nodes["rt2"] = &lockNode{id: "rt2", name: "rt2", deps: []string{"dt"}}
	got := map[string]string{}
	for _, f := range g.transitive([]lockRoot{{dir: "p", direct: map[string]string{
		"rp": "prod", "rt": "test", "rb": "build", "rd": "dev", "rt2": "test",
	}}}) {
		got[f.Attrs["name"].(string)] = f.Attrs["scope"].(string)
	}
	want := map[string]string{"a": "prod", "m": "prod", "t": "test", "b": "build", "x": "test", "d": "dev", "tb": "build", "dt": "test"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: scope %q, want %q", k, got[k], v)
		}
	}
}

func TestNugetLock(t *testing.T) {
	lock := `{
  "version": 1,
  "dependencies": {
    "net8.0": {
      "Serilog": {
        "type": "Direct",
        "requested": "[4.0.0, )",
        "resolved": "4.0.0",
        "contentHash": "abc",
        "dependencies": { "Serilog.Sinks.Console": "5.0.0" }
      },
      "Serilog.Sinks.Console": {
        "type": "Transitive",
        "resolved": "5.0.0",
        "contentHash": "def"
      },
      "Domain": {
        "type": "Project",
        "dependencies": { "Serilog": "4.0.0" }
      }
    },
    "net48": {
      "Serilog": {
        "type": "Direct",
        "resolved": "4.0.0",
        "dependencies": { "Serilog.Sinks.Console": "5.0.0" }
      },
      "Serilog.Sinks.Console": {
        "type": "Transitive",
        "resolved": "5.0.0"
      }
    }
  }
}`
	got := nugetFacts(t, map[string]string{
		"src/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"><ItemGroup>
<PackageReference Include="Serilog" Version="4.0.0" />
<ProjectReference Include="..\Domain\Domain.csproj" />
</ItemGroup></Project>`,
		"src/Web/packages.lock.json": lock,
	})
	f, ok := got["dependency:nuget:src/Web/Web.csproj:lock:serilog.sinks.console"]
	if !ok {
		t.Fatalf("no transitive fact: %v", got)
	}
	if f.Attrs["direct"] != false || f.Attrs["version"] != "5.0.0" || f.Attrs["scope"] != "prod" || f.Attrs["ecosystem"] != "nuget" || f.Attrs["name"] != "Serilog.Sinks.Console" {
		t.Fatalf("attrs: %+v", f.Attrs)
	}
	if via := f.Attrs["via"].([]any); len(via) != 1 || via[0] != "Serilog" {
		t.Fatalf("via: %v", via)
	}
	if roots := f.Attrs["roots"].([]any); len(roots) != 1 || roots[0] != "src/Web" {
		t.Fatalf("roots: %v", roots)
	}
	if fw := extOf(f)["frameworks"]; !reflect.DeepEqual(fw, []any{"net48", "net8.0"}) {
		t.Fatalf("frameworks: %#v", fw)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Path != "src/Web/packages.lock.json" || f.Evidence[0].Line != 12 {
		t.Fatalf("evidence: %+v", f.Evidence)
	}
	for k := range got {
		if strings.Contains(k, ":lock:domain") || strings.Contains(k, ":lock:serilog:") || strings.HasSuffix(k, ":lock:serilog") {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestLockScopesDeep(t *testing.T) {
	g := &lockGraph{path: "x.lock", dir: "p", eco: "ex", plugin: "deps-ex", nodes: map[string]*lockNode{}}
	add := func(id string, deps ...string) {
		g.nodes[id] = &lockNode{id: id, name: id, version: "1", line: 1, deps: deps}
	}
	// prod root P -> X -> M -> N; test root T -> M -> N; dev root D -> M2 -> N2, prod root P -> M2.
	add("P", "X", "M2")
	add("X", "M")
	add("T", "M")
	add("M", "N")
	add("N")
	add("D", "M2")
	add("M2", "N2")
	add("N2")
	got := map[string]string{}
	for _, f := range g.transitive([]lockRoot{{dir: "p", direct: map[string]string{"P": "prod", "T": "test", "D": "dev"}}}) {
		got[f.Attrs["name"].(string)] = f.Attrs["scope"].(string)
	}
	for k, v := range map[string]string{"M": "prod", "N": "prod", "M2": "prod", "N2": "prod"} {
		if got[k] != v {
			t.Errorf("%s: scope %q, want %q", k, got[k], v)
		}
	}
	// A package reached only from the dev and test roots is test.
	g2 := &lockGraph{path: "x.lock", dir: "p", eco: "ex", plugin: "deps-ex", nodes: map[string]*lockNode{}}
	g = g2
	add("T", "M")
	add("D", "M")
	add("M", "N")
	add("N")
	got = map[string]string{}
	for _, f := range g.transitive([]lockRoot{{dir: "p", direct: map[string]string{"T": "test", "D": "dev"}}}) {
		got[f.Attrs["name"].(string)] = f.Attrs["scope"].(string)
	}
	if got["M"] != "test" || got["N"] != "test" {
		t.Errorf("dev+test: %v", got)
	}
}

// The evidence line of a lock entry comes from a line index built once, not a
// count from the start of the file per entry.
func TestNugetLockLinesLinear(t *testing.T) {
	lock := func(n int) *repo.Repo {
		var b strings.Builder
		b.WriteString("{\n\"version\": 1,\n\"dependencies\": {\n\"net8.0\": {\n")
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",\n")
			}
			fmt.Fprintf(&b, `"Pkg.%06d": { "type": "Transitive", "resolved": "1.0.0", "contentHash": "abcdefghijklmnop" }`, i)
		}
		b.WriteString("\n}\n}\n}\n")
		return repotest.Files(t, map[string]string{"App/packages.lock.json": b.String()})
	}
	scaling.Linear(t, 10000, func(n int) func() {
		r := lock(n)
		return func() { _, _ = readNugetLock(r, "App/packages.lock.json") }
	})
	const n = 40000
	g, err := readNugetLock(lock(n), "App/packages.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := g.nodes["pkg.000000"].line; got != 5 {
		t.Errorf("first entry line %d, want 5", got)
	}
	if got := g.nodes[fmt.Sprintf("pkg.%06d", n-1)].line; got != 5+n-1 {
		t.Errorf("last entry line %d, want %d", got, 5+n-1)
	}
}

// Test packages a root props file adds under '$(IsTestProject)'=='true' land
// only on test projects; a condition that cannot be decided stays in ext.
func TestNugetConditionalProps(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"Directory.Build.props": `<Project>
  <ItemGroup Condition="'$(IsTestProject)'=='true'">
    <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.9.0" />
    <PackageReference Include="Moq" Version="4.20.0" />
  </ItemGroup>
  <ItemGroup Condition="$(MSBuildProjectName.EndsWith('.Tests'))">
    <PackageReference Include="coverlet.collector" Version="6.0.0" />
  </ItemGroup>
</Project>`,
		"src/App/App.csproj":               `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><IsTestProject>true</IsTestProject></PropertyGroup></Project>`,
	})
	for _, id := range []string{"microsoft.net.test.sdk", "moq"} {
		if _, ok := got["dependency:nuget:src/App/App.csproj:"+id]; ok {
			t.Errorf("prod project has %s", id)
		}
		f, ok := got["dependency:nuget:tests/App.Tests/App.Tests.csproj:"+id]
		if !ok || f.Attrs["scope"] != "test" || extOf(f)["condition"] != nil {
			t.Errorf("test project %s: %+v", id, f.Attrs)
		}
	}
	c := got["dependency:nuget:src/App/App.csproj:coverlet.collector"]
	if c.Attrs["scope"] != "prod" || extOf(c)["condition"] != "$(MSBuildProjectName.EndsWith('.Tests'))" {
		t.Errorf("undecided: %+v", c.Attrs)
	}
}

// A lock entry of type Direct that the project file does not show (an SDK
// implicit reference, say) is reported from the lock, and starts a walk.
func TestNugetLockDirectNotInProject(t *testing.T) {
	lock := `{
  "version": 1,
  "dependencies": {
    "net8.0": {
      "Example.Implicit": {
        "type": "Direct",
        "resolved": "1.0.0",
        "dependencies": { "Example.Below": "2.0.0" }
      },
      "Example.Below": {
        "type": "Transitive",
        "resolved": "2.0.0"
      }
    }
  }
}`
	for _, tc := range []struct{ proj, scope string }{
		{`<Project Sdk="Microsoft.NET.Sdk"></Project>`, "prod"},
		{`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><IsTestProject>true</IsTestProject></PropertyGroup></Project>`, "test"},
	} {
		got := nugetFacts(t, map[string]string{"App/App.csproj": tc.proj, "App/packages.lock.json": lock})
		f, ok := got["dependency:nuget:App/App.csproj:lock:example.below"]
		if !ok || f.Attrs["scope"] != tc.scope || f.Attrs["direct"] != false {
			t.Errorf("%s: below %+v in %v", tc.scope, f.Attrs, got)
		}
		d, ok := got["dependency:nuget:App/App.csproj:lock:example.implicit"]
		if !ok || d.Attrs["direct"] != true || d.Attrs["scope"] != tc.scope || d.Attrs["version"] != "1.0.0" ||
			d.Attrs["name"] != "Example.Implicit" || d.Attrs["lockfile"] != "App/packages.lock.json" ||
			!reflect.DeepEqual(d.Attrs["roots"], []any{"App"}) ||
			!reflect.DeepEqual(extOf(d), map[string]any{"lock_direct": true, "frameworks": []any{"net8.0"}}) {
			t.Errorf("%s: implicit %+v", tc.scope, d.Attrs)
		}
		if len(d.Evidence) != 1 || d.Evidence[0].Path != "App/packages.lock.json" || d.Evidence[0].Line != 5 || d.Evidence[0].Unit != "App" {
			t.Errorf("%s: evidence %+v", tc.scope, d.Evidence)
		}
		if len(got) != 2 {
			t.Errorf("%s: want 2 facts, got %v", tc.scope, got)
		}
	}
}

// Project references are keyed by the target's path: two projects of one name
// are two facts, and a project never collides with a package of its name.
func TestNugetProjectRefKeys(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"src/Orders/Domain/Domain.csproj":  `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Billing/Domain/Domain.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Serilog/Serilog.csproj":       `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
<PackageReference Include="Serilog" Version="4.0.0" />
<ProjectReference Include="..\Orders\Domain\Domain.csproj" />
<ProjectReference Include="..\Billing\Domain\Domain.csproj" />
<ProjectReference Include="..\Serilog\Serilog.csproj" />
</ItemGroup></Project>`,
	})
	const pre = "dependency:nuget:src/App/App.csproj:"
	for _, k := range []string{"serilog", "project:src/Orders/Domain/Domain.csproj", "project:src/Billing/Domain/Domain.csproj", "project:src/Serilog/Serilog.csproj"} {
		if _, ok := got[pre+k]; !ok {
			t.Errorf("no %s in %v", k, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("want 4 facts, got %d", len(got))
	}
	d := got[pre+"project:src/Billing/Domain/Domain.csproj"]
	in, _ := d.Attrs["internal"].(map[string]any)
	if d.Attrs["name"] != "Domain" || in["name"] != "Domain" || in["path"] != "src/Billing/Domain" {
		t.Errorf("billing domain: %+v", d.Attrs)
	}
	if s := got[pre+"serilog"]; s.Attrs["internal"] != nil || s.Attrs["version"] != "4.0.0" {
		t.Errorf("package serilog: %+v", s.Attrs)
	}
}

func TestNugetProjectRefCondition(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"src/Net8Only/Net8Only.csproj":     `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Net472Only/Net472Only.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
<ProjectReference Include="..\Net8Only\Net8Only.csproj" Condition="'$(TargetFramework)'=='net8.0'" />
<ProjectReference Include="..\Net472Only\Net472Only.csproj" Condition="'$(TargetFramework)'=='net472'" />
</ItemGroup></Project>`,
	})
	const pre = "dependency:nuget:src/App/App.csproj:project:"
	for path, cond := range map[string]string{"src/Net8Only/Net8Only.csproj": "'$(TargetFramework)'=='net8.0'", "src/Net472Only/Net472Only.csproj": "'$(TargetFramework)'=='net472'"} {
		if c := extOf(got[pre+path])["condition"]; c != cond {
			t.Errorf("%s: condition %v, want %q", path, c, cond)
		}
	}
}

// The walk starts at the lock's Project node of a referenced project, and only
// at a node of type Project.
func TestNugetLockProjectNode(t *testing.T) {
	lock := `{
  "version": 1,
  "dependencies": {
    "net8.0": {
      "Example.Helper": {
        "type": "Direct",
        "resolved": "1.0.0",
        "dependencies": { "Example.Deep": "1.0.0" }
      },
      "Example.Deep": { "type": "Transitive", "resolved": "1.0.0" },
      "Example.Leaf": { "type": "Transitive", "resolved": "2.0.0" },
      "domain": {
        "type": "Project",
        "dependencies": { "Example.Leaf": "[2.0.0, )" }
      }
    }
  }
}`
	got := nugetFacts(t, map[string]string{
		"src/Domain/Domain.csproj":                   `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"tools/Example.Helper/Example.Helper.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Web/Web.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
<ProjectReference Include="..\Domain\Domain.csproj" />
<ProjectReference Include="..\..\tools\Example.Helper\Example.Helper.csproj" />
</ItemGroup></Project>`,
		"src/Web/packages.lock.json": lock,
	})
	const pre = "dependency:nuget:src/Web/Web.csproj:"
	if f, ok := got[pre+"lock:example.leaf"]; !ok || f.Attrs["direct"] != false {
		t.Errorf("project's package not reached: %v", got)
	}
	// Example.Helper in the lock is a package, not the referenced project: it
	// is a lock Direct entry of its own.
	if f, ok := got[pre+"lock:example.helper"]; !ok || extOf(f)["lock_direct"] != true {
		t.Errorf("package named like a referenced project: %v", got)
	}
	if _, ok := got[pre+"lock:example.deep"]; !ok {
		t.Errorf("walk from the package missing: %v", got)
	}
}

// Frameworks that resolve a package to different versions leave the version
// unknown and list each.
func TestNugetLockFrameworkVersions(t *testing.T) {
	lock := `{
  "version": 1,
  "dependencies": {
    "net8.0": {
      "Example.Root": { "type": "Direct", "resolved": "1.0.0", "dependencies": { "Some.Package": "4.0.0" } },
      "Some.Package": { "type": "Transitive", "resolved": "4.0.0" }
    },
    "net472": {
      "Example.Root": { "type": "Direct", "resolved": "1.0.0", "dependencies": { "Some.Package": "3.5.0" } },
      "Some.Package": { "type": "Transitive", "resolved": "3.5.0" }
    }
  }
}`
	got := nugetFacts(t, map[string]string{
		"App/App.csproj":         `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="Example.Root" Version="1.0.0" /></ItemGroup></Project>`,
		"App/packages.lock.json": lock,
	})
	f := got["dependency:nuget:App/App.csproj:lock:some.package"]
	if f.Attrs["version"] != "" || extOf(f)["unresolved"] != true || f.Attrs["version_info"] != nil {
		t.Fatalf("attrs: %+v", f.Attrs)
	}
	want := map[string]any{
		"net8.0": map[string]any{"version": "4.0.0", "line": 6},
		"net472": map[string]any{"version": "3.5.0", "line": 10},
	}
	if fv := extOf(f)["framework_versions"]; !reflect.DeepEqual(fv, want) {
		t.Errorf("framework_versions: %#v", fv)
	}
	if len(f.Evidence) != 2 || f.Evidence[0].Line != 6 || f.Evidence[1].Line != 10 {
		t.Errorf("evidence: %+v", f.Evidence)
	}
	if r := got["dependency:nuget:App/App.csproj:example.root"]; r.Attrs["version"] != "1.0.0" || extOf(r) != nil {
		t.Errorf("root: %+v", r.Attrs)
	}
}

// Several conditional version sources: no version, every candidate in ext.
func TestNugetVersionCandidates(t *testing.T) {
	got := nugetFacts(t, map[string]string{
		"Directory.Packages.props": `<Project>
<PropertyGroup><ManagePackageVersionsCentrally>true</ManagePackageVersionsCentrally></PropertyGroup>
<ItemGroup>
<PackageVersion Include="Example.Ef" Version="8.0.0" Condition="'$(TargetFramework)'=='net8.0'" />
<PackageVersion Include="Example.Ef" Version="6.0.0" Condition="'$(TargetFramework)'=='net6.0'" />
</ItemGroup></Project>`,
		"App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="Example.Ef" /></ItemGroup></Project>`,
	})
	f := got["dependency:nuget:App/App.csproj:example.ef"]
	if f.Attrs["version"] != "" || extOf(f)["unresolved"] != true || f.Attrs["version_info"].(map[string]any)["scheme"] != "unknown" {
		t.Fatalf("attrs: %+v", f.Attrs)
	}
	want := []any{
		map[string]any{"version": "8.0.0", "condition": "'$(TargetFramework)'=='net8.0'", "path": "Directory.Packages.props", "line": 4},
		map[string]any{"version": "6.0.0", "condition": "'$(TargetFramework)'=='net6.0'", "path": "Directory.Packages.props", "line": 5},
	}
	if c := extOf(f)["candidates"]; !reflect.DeepEqual(c, want) {
		t.Errorf("candidates: %#v", c)
	}
	if len(f.Evidence) != 3 {
		t.Errorf("evidence: %+v", f.Evidence)
	}
}

// A lock Direct entry whose frameworks disagree has no version_info, as a
// transitive fact.
func TestNugetLockDirectUnknownVersion(t *testing.T) {
	lock := `{
  "version": 1,
  "dependencies": {
    "net8.0": { "Example.Implicit": { "type": "Direct", "resolved": "2.0.0" } },
    "net472": { "Example.Implicit": { "type": "Direct", "resolved": "1.0.0" } }
  }
}`
	got := nugetFacts(t, map[string]string{
		"App/App.csproj":         `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"App/packages.lock.json": lock,
	})
	f, ok := got["dependency:nuget:App/App.csproj:lock:example.implicit"]
	if !ok || f.Attrs["version"] != "" || extOf(f)["unresolved"] != true {
		t.Fatalf("lock direct: %+v", f)
	}
	if vi, ok := f.Attrs["version_info"]; ok {
		t.Errorf("version_info %v for an unknown version", vi)
	}
}

// A diamond of -r includes (every level includes the next one many times)
// is expanded once per file, not once per path.
func TestRequirementsIncludeDiamond(t *testing.T) {
	files := map[string]string{}
	for lvl := 0; lvl < 8; lvl++ {
		var b strings.Builder
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&b, "-r r%d.txt\n", lvl+1)
		}
		fmt.Fprintf(&b, "pkg%d==1.0\n", lvl)
		name := fmt.Sprintf("r%d.txt", lvl)
		if lvl == 0 {
			name = "requirements.txt"
		}
		files[name] = b.String()
	}
	files["r8.txt"] = "-r requirements.txt\nleaf==1.0\n"
	r := repotest.Files(t, files)
	res := PythonPlugin{}.Run(r)
	if n := len(res.Facts); n == 0 || n > 100 {
		t.Fatalf("facts = %d", n)
	}
}
