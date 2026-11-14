package dotnet

import (
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/scaling"
	"github.com/cernly/cernly/repo/repotest"
)

// A file cut off inside a construct yields nothing for that construct; the
// rest of the file, and the other files of the repo, still give their facts.
func TestTruncatedSource(t *testing.T) {
	for _, tail := range []string{
		"c.GetAsync(",
		"Environment.GetEnvironmentVariable(",
		"new HttpRequestMessage(",
		"new HttpRequestMessage(HttpMethod.Get,",
		"new Uri(",
		"c.BaseAddress = new Uri(",
		"[",
		"[Migration(",
		"[DbContext(typeof(",
		"class A {",
		"class A<",
		"class A<T> : Migration { protected override void Up(MigrationBuilder mb",
		"class A : Migration { protected override void Up(MigrationBuilder mb) {",
		"class A : Migration { protected override void Up(MigrationBuilder mb) { mb.CreateTable(",
		"class A : Migration { protected override void Up(MigrationBuilder mb) { mb.AddColumn<",
		"class A : Migration { protected override void Up(MigrationBuilder mb) { mb.CreateTable(name: \"T\", columns: t => new {",
		"class A : Migration { protected override void Up(MigrationBuilder mb) { mb.CreateTable(name: \"T\", columns: t => new { Id = t.Column<int>(",
		"class A : Migration { protected override void Up(MigrationBuilder mb) { mb.CreateTable(name: \"T\", columns: t => new { Id = t.Column<int>() }, constraints: t => { t.PrimaryKey(",
	} {
		t.Run(tail, func(t *testing.T) {
			res := Plugin{}.Run(repotest.Files(t, map[string]string{
				"src/Shop/Shop.csproj":                   efProject("Microsoft.EntityFrameworkCore.SqlServer"),
				"src/Shop/Program.cs":                    "var k = Environment.GetEnvironmentVariable(\"SHOP_KEY\");\nvar r = await c.GetAsync(\"https://api.shopvendor.io/v1\");\n" + tail,
				"src/Shop/Migrations/20240101_Broken.cs": "using Microsoft.EntityFrameworkCore.Migrations;\n[Migration(\"20240101_Broken\")]\n" + tail,
				"src/Other/Other.csproj":                 webProject,
				"src/Other/Client.cs":                    "var r = await c.GetAsync(\"https://other.shopvendor.io\");\n",
			}))
			if _, ok := byKey(res.Facts, fact.KindEnvVar)["env_var:SHOP_KEY"]; !ok {
				t.Errorf("env var before the cut lost")
			}
			hosts := byKey(res.Facts, fact.KindOutboundHost)
			for _, h := range []string{"outbound_host:api.shopvendor.io", "outbound_host:other.shopvendor.io"} {
				if _, ok := hosts[h]; !ok {
					t.Errorf("%s lost: %v", h, hosts)
				}
			}
		})
	}
}

// Brackets are matched once per file, so hostile nesting stays linear.
func TestNestedCallsLinear(t *testing.T) {
	for name, src := range map[string]func(n int) string{
		"unclosed": func(n int) string { return strings.Repeat("c.GetAsync(", n) },
		"closed": func(n int) string {
			return strings.Repeat("c.GetAsync(", n) + `"https://api.shopvendor.io"` + strings.Repeat(")", n) + ";"
		},
		"env": func(n int) string {
			return strings.Repeat("Environment.GetEnvironmentVariable(", n) + `"X"` + strings.Repeat(")", n) + ";"
		},
		"request": func(n int) string {
			return strings.Repeat("new HttpRequestMessage(HttpMethod.Get, ", n) + `"https://api.shopvendor.io"` + strings.Repeat(")", n) + ";"
		},
	} {
		t.Run(name, func(t *testing.T) {
			scaling.Linear(t, 40000, func(n int) func() {
				r := repotest.Files(t, map[string]string{"src/Shop/Shop.csproj": webProject, "src/Shop/Program.cs": src(n)})
				return func() { Plugin{}.Run(r) }
			})
		})
	}
}
