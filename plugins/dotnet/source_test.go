package dotnet

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo/repotest"
)

const webProject = `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`

func TestEnvVar(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Shop/Shop.csproj": webProject,
		"src/Shop/Keys.cs": `static class Keys { public const string K = "X_Y"; }
`,
		"src/Shop/Program.cs": `using System;
var key = Environment.GetEnvironmentVariable("SHOP_API_KEY");
var other = System.Environment.GetEnvironmentVariable(Keys.K);
var dyn = Environment.GetEnvironmentVariable(name);
var lower = Environment.GetEnvironmentVariable("shop.setting");
builder.Configuration.AddEnvironmentVariables("SHOP_");
var mollie = Environment.GetEnvironmentVariable("Mollie__ApiKey");
var p = Environment.GetEnvironmentVariable("Path");
var cs = Environment.GetEnvironmentVariable("ConnectionStrings__Shop");
var guessed = Environment.GetEnvironmentVariable(Settings.ApiKey);
`,
		"src/Shop/Other.cs": `class Other { const string ApiKey = "PAY_KEY"; }
`,
		"tests/Shop.Tests/Shop.Tests.csproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Shop.Tests/EnvTests.cs": `class EnvTests { void T() { Environment.GetEnvironmentVariable("TEST_ONLY"); } }
`,
	})
	res := Plugin{}.Run(r)
	env := byKey(res.Facts, fact.KindEnvVar)
	if len(env) != 5 {
		t.Fatalf("env facts %v", env)
	}
	f, ok := env["env_var:SHOP_API_KEY"]
	if !ok {
		t.Fatalf("no env_var:SHOP_API_KEY: %v", env)
	}
	if want := map[string]any{"name": "SHOP_API_KEY", "origins": []any{"code"}}; !reflect.DeepEqual(f.Attrs, want) {
		t.Errorf("attrs %v, want %v", f.Attrs, want)
	}
	if want := []fact.Evidence{{Path: "src/Shop/Program.cs", Line: 2, Unit: "src/Shop"}}; !reflect.DeepEqual(f.Evidence, want) {
		t.Errorf("evidence %v, want %v", f.Evidence, want)
	}
	if f, ok := env["env_var:X_Y"]; !ok || f.Evidence[0].Line != 3 {
		t.Errorf("const argument: %v", env)
	}
	for k, line := range map[string]int{"env_var:Mollie__ApiKey": 7, "env_var:Path": 8, "env_var:ConnectionStrings__Shop": 9} {
		if f, ok := env[k]; !ok || f.Evidence[0].Line != line {
			t.Errorf("%s: %v", k, env)
		}
	}
	for _, k := range []string{"env_var:shop.setting", "env_var:PAY_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s should give no fact", k)
		}
	}
}

func outbound(t *testing.T, files map[string]string) map[string]fact.Fact {
	t.Helper()
	return byKey(Plugin{}.Run(repotest.Files(t, files)).Facts, fact.KindOutboundHost)
}

func usages(hosts map[string]fact.Fact) map[string]string {
	out := map[string]string{}
	for k, f := range hosts {
		out[k] = f.Attrs["usage"].(string)
	}
	return out
}

func TestOutboundCode(t *testing.T) {
	hosts := outbound(t, map[string]string{
		"src/Shop/Shop.csproj": webProject,
		"src/Shop/Clients.cs": `class Clients {
    const string StatusUrl = "https://status.statuspage-demo.io";
    const string PayUrl = "https://pay.paygate-demo.io/v2";
    const string MirrorHost = "https://mirror.paygate-demo.io";
    void Setup(HttpClient client) {
        client.BaseAddress = new Uri("https://api.shopvendor.io/");
        // see https://commented.vendor-docs.io for the API
        /* and https://blockcomment.vendor-docs.io */
        var u = $"https://{host}/x";
        var d = $"https://interp.vendor-docs.io/{id}";
        var local = "http://localhost:5000";
        var reserved = "https://api.example.com/";
        var link = "https://help.vendor-docs.io/start";
    }
    async Task Call(HttpClient client) {
        await client.GetAsync("https://pay.paygate.io/v1");
        await client.PostAsync(PayUrl, null);
        await client.SendAsync(new HttpRequestMessage(HttpMethod.Get, "https://send.paygate.io/x"));
    }
}
`,
		"src/Shop/Startup.cs": `class Startup { void C(IServiceCollection s) {
    s.AddHttpClient("rates", c => c.BaseAddress = new Uri("https://rates.ratesvendor.io"));
} }
`,
		"tests/Shop.Tests/Shop.Tests.csproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Shop.Tests/ClientTests.cs":    `class T { void X(HttpClient c) { c.GetAsync("https://only-in-tests.io/x"); } }` + "\n",
	})
	want := map[string]string{
		"outbound_host:api.shopvendor.io":         "request",
		"outbound_host:pay.paygate.io":            "request",
		"outbound_host:pay.paygate-demo.io":       "request",
		"outbound_host:send.paygate.io":           "request",
		"outbound_host:rates.ratesvendor.io":      "request",
		"outbound_host:status.statuspage-demo.io": "default",
		"outbound_host:mirror.paygate-demo.io":    "default", // host: a shipped config-name suffix
		"outbound_host:help.vendor-docs.io":       "mention",
	}
	if got := usages(hosts); !reflect.DeepEqual(got, want) {
		t.Fatalf("usages\n got %v\nwant %v", got, want)
	}
	f := hosts["outbound_host:api.shopvendor.io"]
	if want := map[string]any{"host": "api.shopvendor.io", "via": "literal_url", "usage": "request"}; !reflect.DeepEqual(f.Attrs, want) {
		t.Errorf("attrs %v", f.Attrs)
	}
	if want := []fact.Evidence{{Path: "src/Shop/Clients.cs", Line: 6, Unit: "src/Shop"}}; !reflect.DeepEqual(f.Evidence, want) {
		t.Errorf("evidence %v", f.Evidence)
	}
	// The const is declared on line 3 and used for a request on line 17.
	if ev := hosts["outbound_host:pay.paygate-demo.io"].Evidence; len(ev) != 2 || ev[0].Line != 3 || ev[1].Line != 17 {
		t.Errorf("const evidence %v", ev)
	}
}

func TestAppSettings(t *testing.T) {
	hosts := outbound(t, map[string]string{
		"src/Shop/Shop.csproj": webProject,
		"src/Shop/appsettings.Production.json": "\uFEFF{\n" +
			"  // payment provider\n" +
			"  \"Payments\": {\"BaseUrl\": \"https://api.paygate.io\"}, /* block\n" +
			"  comment */\n" +
			"  \"Docs\": \"https://docs.vendor-docs.io\",\n" +
			"  \"Notes\": \"see // not a comment https://notes.vendor-docs.io\",\n" +
			"  \"ConnectionStrings\": {\"Shop\": \"postgres://db.paygate-data.io/shop\", \"Cache\": \"Host=cache.paygate-data.io\"},\n" +
			"  \"Reserved\": {\"Endpoint\": \"https://api.example.com\"},\n" +
			"  \"Mirrors\": [\"https://mirror.vendor-docs.io\",],\n" +
			"}\n",
		"config/appsettings.json":            `{"Webhooks": {"Host": "https://hooks.paygate.io"}}`,
		"tests/Shop.Tests/Shop.Tests.csproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Shop.Tests/appsettings.json":  `{"ApiUrl": "https://only-in-tests.io"}`,
	})
	want := map[string]string{
		"outbound_host:api.paygate.io":        "default",
		"outbound_host:docs.vendor-docs.io":   "mention",
		"outbound_host:mirror.vendor-docs.io": "mention",
		"outbound_host:hooks.paygate.io":      "default",
	}
	if got := usages(hosts); !reflect.DeepEqual(got, want) {
		t.Fatalf("usages\n got %v\nwant %v", got, want)
	}
	if ev := hosts["outbound_host:api.paygate.io"].Evidence; len(ev) != 1 || ev[0] != (fact.Evidence{Path: "src/Shop/appsettings.Production.json", Line: 3, Unit: "src/Shop"}) {
		t.Errorf("evidence %v", ev)
	}
	if ev := hosts["outbound_host:mirror.vendor-docs.io"].Evidence; len(ev) != 1 || ev[0].Line != 9 {
		t.Errorf("array evidence %v", ev)
	}
	res := Plugin{}.Run(repotest.Files(t, map[string]string{
		"src/Shop/Shop.csproj":      webProject,
		"src/Shop/appsettings.json": `{"ConnectionStrings": {"Shop": "Host=db.paygate-data.io;Database=shop"}}`,
	}))
	if len(res.Facts) != 0 {
		t.Errorf("connection strings gave facts: %v", res.Facts)
	}
}

func TestAppSettingsBroken(t *testing.T) {
	res := Plugin{}.Run(repotest.Files(t, map[string]string{
		"src/Shop/Shop.csproj":      webProject,
		"src/Shop/appsettings.json": "{\n  \"A\": \"https://a.paygate.io\",\n  \"B\": \n",
	}))
	if len(res.Errors) != 1 || res.Errors[0].Path != "src/Shop/appsettings.json" {
		t.Fatalf("errors %v", res.Errors)
	}
}

func TestTolerantJSON(t *testing.T) {
	in := "\uFEFF{\r\n // c \"x\"\r\n \"a\": \"//not\", /* m\n*/ \"b\": [1,2,],\n}"
	got := tolerantJSON([]byte(in))
	if strings.Count(string(got), "\n") != strings.Count(in, "\n") {
		t.Errorf("lines changed: %q", got)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("not valid JSON: %v: %q", err, got)
	}
	if v["a"] != "//not" {
		t.Errorf("string changed: %v", v)
	}
}

// projectDir reads the set of project folders the state holds, built once,
// not one rebuilt from the projects on every call.
func TestProjectDirSetOnce(t *testing.T) {
	s := &state{dirs: map[string]bool{"src/App": true, ".": true}}
	for f, want := range map[string]string{"src/App/Config/appsettings.json": "src/App", "other/appsettings.json": "."} {
		if got, ok := s.projectDir(f); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", f, got, ok, want)
		}
	}
	if _, ok := (&state{dirs: map[string]bool{}}).projectDir("a/appsettings.json"); ok {
		t.Error("no project folder: want none")
	}
}
