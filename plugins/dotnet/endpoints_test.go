package dotnet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin/plugintest"
	"github.com/cernly/cernly/repo/repotest"
)

// epRepo runs the plugin over one C# file in a web project.
func epRepo(t *testing.T, cs string) map[string]fact.Fact {
	t.Helper()
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj": webProject,
		"src/Api/Program.cs": cs,
	})
	return byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint)
}

func epGet(t *testing.T, eps map[string]fact.Fact, method, path string) fact.Fact {
	t.Helper()
	key := "endpoint:csharp:src/Api/Program.cs:" + method + ":" + path
	f, ok := eps[key]
	if !ok {
		var keys []string
		for k := range eps {
			keys = append(keys, k)
		}
		t.Fatalf("no %s; have %v", key, keys)
	}
	return f
}

func authOf(f fact.Fact) (string, []string) {
	a := f.Attrs["auth"].(map[string]any)
	var ms []string
	for _, m := range a["markers"].([]any) {
		ms = append(ms, m.(string))
	}
	return a["state"].(string), ms
}

func lines(f fact.Fact) []int {
	var out []int
	for _, e := range f.Evidence {
		out = append(out, e.Line)
	}
	return out
}

func TestMinimalAPI(t *testing.T) {
	eps := epRepo(t, `var builder = WebApplication.CreateBuilder(args);
var app = builder.Build();
app.MapGet("/health", () => "ok").AllowAnonymous();
var api = app.MapGroup("/api").RequireAuthorization();
var orders = api.MapGroup("orders");
orders.MapGet("/{id:int}", GetOrder);
orders.MapPost("", Create);
`)
	if len(eps) != 3 {
		t.Fatalf("endpoints: %v", eps)
	}
	h := epGet(t, eps, "GET", "/health")
	if st, ms := authOf(h); st != "anonymous" || !reflect.DeepEqual(ms, []string{"AllowAnonymous()"}) {
		t.Errorf("health auth %s %v", st, ms)
	}
	if h.Attrs["framework"] != "aspnet-minimal" || h.Attrs["language"] != "csharp" || h.Attrs["method"] != "GET" || h.Attrs["raw"] != "/health" {
		t.Errorf("health attrs %v", h.Attrs)
	}
	if _, ok := h.Attrs["handler"]; ok {
		t.Errorf("lambda handler: %v", h.Attrs)
	}
	if h.Plugin != "dotnet" {
		t.Errorf("plugin %q", h.Plugin)
	}
	if h.Attrs["app_unit"] != "src/Api" {
		t.Errorf("app_unit %v", h.Attrs["app_unit"])
	}
	g := epGet(t, eps, "GET", "/api/orders/{id}")
	if st, ms := authOf(g); st != "required" || !reflect.DeepEqual(ms, []string{"RequireAuthorization()"}) {
		t.Errorf("get auth %s %v", st, ms)
	}
	if g.Attrs["handler"] != "GetOrder" || g.Attrs["raw"] != "/{id:int}" || g.Attrs["path"] != "/api/orders/{id}" {
		t.Errorf("get attrs %v", g.Attrs)
	}
	// the registration first, then the prefix lines, then the marker lines
	if want := []int{6, 4, 5}; !reflect.DeepEqual(lines(g), want) {
		t.Errorf("get evidence lines %v, want %v", lines(g), want)
	}
	p := epGet(t, eps, "POST", "/api/orders")
	if st, _ := authOf(p); st != "required" {
		t.Errorf("post auth %s", st)
	}
	if _, ok := p.Attrs["ext"]; ok {
		t.Errorf("post ext %v", p.Attrs["ext"])
	}
}

func TestMapMethodsAndMap(t *testing.T) {
	eps := epRepo(t, `var app = WebApplication.Create(args);
app.MapMethods("/x", new[] { "PUT", "PATCH" }, H);
app.Map("/y", H);
app.MapHealthChecks("/healthz");
`)
	if len(eps) != 4 {
		t.Fatalf("endpoints: %v", eps)
	}
	epGet(t, eps, "PUT", "/x")
	epGet(t, eps, "PATCH", "/x")
	epGet(t, eps, "ANY", "/y")
	epGet(t, eps, "ANY", "/healthz")
}

func TestUnknownReceiver(t *testing.T) {
	eps := epRepo(t, `static void MapOrders(this IEndpointRouteBuilder e) { e.MapGet("/x", H); }
`)
	f := epGet(t, eps, "GET", "{?}/x")
	if st, ms := authOf(f); st != "unknown" || len(ms) != 0 {
		t.Errorf("auth %s %v", st, ms)
	}
	if ext, _ := f.Attrs["ext"].(map[string]any); ext["prefix_unresolved"] != true {
		t.Errorf("ext %v", f.Attrs["ext"])
	}
}

func TestUseEndpointsLambda(t *testing.T) {
	eps := epRepo(t, `app.UseEndpoints(endpoints =>
{
    endpoints.MapGet("/a", H);
});
`)
	f := epGet(t, eps, "GET", "/a")
	if _, ok := f.Attrs["ext"]; ok {
		t.Errorf("ext %v", f.Attrs["ext"])
	}
}

func TestNonLiteralPath(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
app.MapGet(Routes.Orders, H);
string p = cfg["x"];
app.MapGet(p, H);
static class Routes { public const string Orders = "/orders"; }
`)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	epGet(t, eps, "GET", "/orders")
}

func TestConditional(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
if (app.Environment.IsDevelopment())
{
    app.MapGet("/dev", H);
}
app.MapGet("/plain", H);
#if DEBUG
app.MapGet("/dbg", H);
#endif
app.MapGet("/after", H);
#if DEBUG
if (app.Environment.IsDevelopment()) app.MapGet("/both", H);
#endif
`)
	cond := func(path string) any {
		ext, _ := epGet(t, eps, "GET", path).Attrs["ext"].(map[string]any)
		return ext["conditional"]
	}
	for path, want := range map[string]any{
		"/dev": "app.Environment.IsDevelopment()", "/plain": nil, "/dbg": "#if DEBUG", "/after": nil,
		"/both": "#if DEBUG and app.Environment.IsDevelopment()",
	} {
		if got := cond(path); got != want {
			t.Errorf("%s: conditional %v, want %v", path, got, want)
		}
	}
}

const catalog = `[ApiController]
[Route("api/[controller]")]
[Authorize]
public class CatalogController : ControllerBase
{
    [HttpGet]
    [AllowAnonymous]
    public IActionResult List() => Ok();

    [HttpGet("{id}")]
    public IActionResult Get(int id) => Ok();

    [HttpPost("~/import")]
    public IActionResult Import() => Ok();

    [NonAction]
    [HttpGet("skip")]
    public IActionResult Skipped() => Ok();

    [HttpGet("hidden")]
    IActionResult Hidden() => Ok();
}
`

func TestController(t *testing.T) {
	eps := epRepo(t, catalog)
	if len(eps) != 3 {
		t.Fatalf("endpoints: %v", eps)
	}
	l := epGet(t, eps, "GET", "/api/Catalog")
	if st, ms := authOf(l); st != "anonymous" || !reflect.DeepEqual(ms, []string{"[AllowAnonymous]"}) {
		t.Errorf("list auth %s %v", st, ms)
	}
	if l.Attrs["handler"] != "CatalogController.List" || l.Attrs["framework"] != "aspnet-mvc" || l.Attrs["raw"] != "" {
		t.Errorf("list attrs %v", l.Attrs)
	}
	if want := []int{6, 2, 7}; !reflect.DeepEqual(lines(l), want) {
		t.Errorf("list evidence %v, want %v", lines(l), want)
	}
	g := epGet(t, eps, "GET", "/api/Catalog/{id}")
	if st, ms := authOf(g); st != "required" || !reflect.DeepEqual(ms, []string{"[Authorize]"}) {
		t.Errorf("get auth %s %v", st, ms)
	}
	i := epGet(t, eps, "POST", "/import")
	if i.Attrs["raw"] != "~/import" {
		t.Errorf("import attrs %v", i.Attrs)
	}
	if want := []int{13}; !reflect.DeepEqual(lines(i)[:1], want) {
		t.Errorf("import evidence %v", lines(i))
	}
}

func TestControllerAuthorizeArgs(t *testing.T) {
	eps := epRepo(t, `[Route("x")]
public class AdminController : Controller
{
    [HttpGet, Authorize(Roles = "Admin")]
    public IActionResult Index() => View();
}
`)
	f := epGet(t, eps, "GET", "/x")
	if st, ms := authOf(f); st != "required" || !reflect.DeepEqual(ms, []string{`[Authorize(Roles = "Admin")]`}) {
		t.Errorf("auth %s %v", st, ms)
	}
}

func TestControllerVerbsAndArea(t *testing.T) {
	eps := epRepo(t, `[Route("[area]/[controller]")]
[Area("Admin")]
public class MembersController : Controller
{
    [AcceptVerbs("GET", "HEAD")]
    public IActionResult Find() => View();

    [Route("any")]
    public IActionResult Any() => View();
}

[Route("[area]/[controller]")]
public class OrphansController : Controller
{
    [HttpGet("[action]")]
    public IActionResult Show() => View();
}
`)
	epGet(t, eps, "GET", "/Admin/Members")
	epGet(t, eps, "HEAD", "/Admin/Members")
	epGet(t, eps, "ANY", "/Admin/Members/any")
	o := epGet(t, eps, "GET", "{?}/Orphans/Show")
	if ext, _ := o.Attrs["ext"].(map[string]any); ext["prefix_unresolved"] != true {
		t.Errorf("ext %v", o.Attrs["ext"])
	}
}

func TestConventionalRoute(t *testing.T) {
	eps := epRepo(t, `app.MapControllerRoute("default", "{controller=Home}/{action=Index}/{id?}");
`)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	f := epGet(t, eps, "ANY", "/{controller}/{action}/{id}")
	if f.Attrs["framework"] != "aspnet-mvc" {
		t.Errorf("attrs %v", f.Attrs)
	}
	if _, ok := f.Attrs["handler"]; ok {
		t.Errorf("handler %v", f.Attrs)
	}
}

func TestAppUnit(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Web/Web.csproj":   webProject,
		"src/Web/Program.cs":   "var app = builder.Build();\napp.MapGet(\"/w\", H);\n",
		"src/Wasm/Wasm.csproj": `<Project Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"></Project>`,
		"src/Wasm/Program.cs":  "var app = builder.Build();\napp.MapGet(\"/b\", H);\n",
		"src/Lib/Lib.csproj":   `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Lib/Routes.cs":    "var app = builder.Build();\napp.MapGet(\"/l\", H);\n",
		"src/Old/Old.csproj":   `<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003"></Project>`,
		"src/Old/Legacy.cs":    "var app = builder.Build();\napp.MapGet(\"/o\", H);\n",
	})
	eps := byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint)
	want := map[string]any{"/w": "src/Web", "/b": "src/Wasm", "/l": nil}
	if len(eps) != 3 {
		t.Fatalf("endpoints: %v", eps)
	}
	for key, f := range eps {
		path := f.Attrs["path"].(string)
		got := f.Attrs["app_unit"]
		if w, ok := want[path]; !ok || got != w {
			t.Errorf("%s: app_unit %v, want %v", key, got, w)
		}
		if strings.Contains(key, "/Old/") {
			t.Errorf("legacy project gave %s", key)
		}
	}
}

func TestTestFilesSkipped(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"tests/Api.Tests/Api.Tests.csproj": testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Api.Tests/Fake.cs":          "var app = builder.Build();\napp.MapGet(\"/t\", H);\n",
	})
	if eps := byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint); len(eps) != 0 {
		t.Errorf("endpoints in a test project: %v", eps)
	}
}

func TestRepeatedKeyMerges(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
if (a) app.MapGet("/r", H);
else app.MapGet("/r", H).AllowAnonymous();
`)
	f := epGet(t, eps, "GET", "/r")
	if len(f.Evidence) < 2 {
		t.Errorf("evidence %v", f.Evidence)
	}
	if st, _ := authOf(f); st != "unknown" {
		t.Errorf("clashing auth must be unknown, got %s", st)
	}
}

func TestGroupFollowUps(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
var admin = app.MapGroup("/admin");
admin.MapGet("/a", H);
admin.RequireAuthorization("Admins");
var dyn = app.MapGroup(cfg.Prefix);
dyn.MapGet("/b", H);
static void Extra(this WebApplication app) { app.MapGet("/c", H); }
app.MapGet("/d", [AllowAnonymous] () => "x");
`)
	a := epGet(t, eps, "GET", "/admin/a")
	if st, ms := authOf(a); st != "required" || !reflect.DeepEqual(ms, []string{`RequireAuthorization("Admins")`}) {
		t.Errorf("admin auth %s %v", st, ms)
	}
	b := epGet(t, eps, "GET", "{?}/b")
	if ext, _ := b.Attrs["ext"].(map[string]any); ext["prefix_unresolved"] != true {
		t.Errorf("dynamic prefix ext %v", b.Attrs["ext"])
	}
	epGet(t, eps, "GET", "{?}/c") // a parameter named app does not make it the root
	d := epGet(t, eps, "GET", "/d")
	if st, ms := authOf(d); st != "anonymous" || !reflect.DeepEqual(ms, []string{"[AllowAnonymous]"}) {
		t.Errorf("handler attribute auth %s %v", st, ms)
	}
}

// A file cut anywhere gives no panic and no hang.
func TestEndpointsTruncated(t *testing.T) {
	src := "var app = builder.Build();\nvar g = app.MapGroup(\"/a\").RequireAuthorization();\nif (x) { g.MapGet(\"/b\", [Authorize(Roles = \"A\")] () => 1); }\n#if X\napp.UseEndpoints(e => { e.MapMethods(\"/c\", new[] { \"GET\" }, H); });\n#endif\n" + catalog
	for n := 0; n <= len(src); n += 5 {
		epRepo(t, src[:n])
	}
}

func unresolvedExt(t *testing.T, f fact.Fact) {
	t.Helper()
	if ext, _ := f.Attrs["ext"].(map[string]any); ext["prefix_unresolved"] != true {
		t.Errorf("%s: ext %v", f.Key, f.Attrs["ext"])
	}
}

func TestRepeatedNoMarkers(t *testing.T) {
	eps := epRepo(t, "var app = builder.Build();\nif (a) app.MapGet(\"/r\", A); else app.MapGet(\"/r\", B);\n")
	f := epGet(t, eps, "GET", "/r")
	ms, ok := f.Attrs["auth"].(map[string]any)["markers"].([]any)
	if !ok || ms == nil || len(ms) != 0 {
		t.Errorf("markers %#v, want an empty list", f.Attrs["auth"])
	}
}

func TestParameterShapes(t *testing.T) {
	eps := epRepo(t, `public static RouteHandlerBuilder MapX(this RouteGroupBuilder app) => app.MapGet("/x", H);
public static void MapY<T>(this RouteGroupBuilder endpoints) { endpoints.MapGet("/y", H); }
public static void MapZ<T>(this RouteGroupBuilder endpoints) where T : class, new() { endpoints.MapGet("/z", H); }
var app = builder.Build();
app.MapGet("/after", H);
`)
	for _, p := range []string{"/x", "/y", "/z"} {
		unresolvedExt(t, epGet(t, eps, "GET", "{?}"+p))
	}
	if f := epGet(t, eps, "GET", "/after"); f.Attrs["ext"] != nil {
		t.Errorf("a parameter's scope leaked: %v", f.Attrs["ext"])
	}
}

func TestMiddlewareBranch(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
app.Map("/branch", b => { b.UseEndpoints(e => { e.MapGet("/x", H); }); });
app.Map("/run", b => b.Run(ctx => ctx.Response.WriteAsync("x")));
app.UseEndpoints(e => { e.MapGet("/root", H); });
`)
	for k := range eps {
		if strings.Contains(k, ":ANY:") {
			t.Errorf("branch gave an endpoint: %s", k)
		}
	}
	unresolvedExt(t, epGet(t, eps, "GET", "{?}/x"))
	if f := epGet(t, eps, "GET", "/root"); f.Attrs["ext"] != nil {
		t.Errorf("UseEndpoints on the root: %v", f.Attrs["ext"])
	}
}

func TestBuildReceivers(t *testing.T) {
	eps := epRepo(t, `var other = cfg.Build();
other.MapGet("/o", H);
var a = WebApplication.CreateBuilder(args).Build();
a.MapGet("/a", H);
var b = WebApplication.CreateBuilder(args);
var c = b.Build();
c.MapGet("/c", H);
`)
	unresolvedExt(t, epGet(t, eps, "GET", "{?}/o"))
	epGet(t, eps, "GET", "/a")
	epGet(t, eps, "GET", "/c")
}

func TestFileAndLine(t *testing.T) {
	eps := epRepo(t, "var app = builder.Build();\n\napp.MapGet(\"/f\", H);\n")
	f := epGet(t, eps, "GET", "/f")
	if f.Attrs["file"] != "src/Api/Program.cs" || f.Attrs["line"] != 3 {
		t.Errorf("file/line %v %v", f.Attrs["file"], f.Attrs["line"])
	}
	c := epGet(t, epRepo(t, catalog), "GET", "/api/Catalog")
	if c.Attrs["file"] != "src/Api/Program.cs" || c.Attrs["line"] != 6 {
		t.Errorf("controller file/line %v %v", c.Attrs["file"], c.Attrs["line"])
	}
}

func TestTestFilesInProductionProject(t *testing.T) {
	body := "var app = builder.Build();\napp.MapGet(\"/t\", H);\n"
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj":      webProject,
		"src/Api/OrderTests.cs":   body,
		"src/Api/Tests/Helper.cs": body,
		"src/Api/Real.cs":         body,
	})
	eps := byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	for _, f := range eps {
		if f.Attrs["file"] != "src/Api/Real.cs" {
			t.Errorf("file %v", f.Attrs["file"])
		}
	}
}

func TestBranchForms(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
app.Map("/admin", b => b.UseMiddleware<Foo>());
app.Map("/expr", b => b.UseX());
app.Map("/gen", b => { b.Run<Foo>(); });
app.Map("/m", ConfigureBranch);
app.Map("/h", Handle);
void ConfigureBranch(IApplicationBuilder b) { b.UseX(); }
IResult Handle(HttpContext c) => Results.Ok();
`)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	epGet(t, eps, "ANY", "/h")
}

func TestExpressionLambdaParameter(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
Configure(app => app.MapGet("/x", H));
app.MapGet("/after", H);
`)
	unresolvedExt(t, epGet(t, eps, "GET", "{?}/x"))
	if f := epGet(t, eps, "GET", "/after"); f.Attrs["ext"] != nil {
		t.Errorf("lambda scope leaked: %v", f.Attrs["ext"])
	}
}

func pageRepo(t *testing.T, files map[string]string) map[string]fact.Fact {
	t.Helper()
	files["src/Web/Web.csproj"] = webProject
	return byKey(Plugin{}.Run(repotest.Files(t, files)).Facts, fact.KindEndpoint)
}

func pageGet(t *testing.T, eps map[string]fact.Fact, file, method, path string) fact.Fact {
	t.Helper()
	f, ok := eps["endpoint:csharp:"+file+":"+method+":"+path]
	if !ok {
		var keys []string
		for k := range eps {
			keys = append(keys, k)
		}
		t.Fatalf("no %s %s in %s; have %v", method, path, file, keys)
	}
	return f
}

func TestRazorPages(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Pages/Privacy.cshtml":       "@page\n@model PrivacyModel\n",
		"src/Web/Pages/Orders/Index.cshtml":  "@using X\n@page\n",
		"src/Web/Pages/Index.cshtml":         "@page\n",
		"src/Web/Pages/Orders/Detail.cshtml": "@page \"{id:int}\"\n@model DetailModel\n",
		"src/Web/Pages/Orders/Detail.cshtml.cs": `using Microsoft.AspNetCore.Authorization;
[Authorize]
public class DetailModel : PageModel { public void OnGet() {} }
`,
		"src/Web/Pages/Other.cshtml":          "@page \"/custom\"\n",
		"src/Web/Pages/Shared/_Layout.cshtml": "<html></html>\n",
		"src/Web/Views/Home/Index.cshtml":     "<p>mvc view</p>\n",
		"src/Web/Pages/Pagetitle.cshtml":      "@pageTitle\n",
	})
	if len(eps) != 5 {
		t.Fatalf("endpoints: %v", eps)
	}
	for file, path := range map[string]string{
		"src/Web/Pages/Privacy.cshtml":      "/Privacy",
		"src/Web/Pages/Orders/Index.cshtml": "/Orders",
		"src/Web/Pages/Index.cshtml":        "/",
		"src/Web/Pages/Other.cshtml":        "/custom",
	} {
		f := pageGet(t, eps, file, "GET", path)
		if f.Attrs["framework"] != "aspnet-razor" || f.Attrs["app_unit"] != "src/Web" || f.Attrs["file"] != file {
			t.Errorf("%s attrs %v", file, f.Attrs)
		}
		if st, ms := authOf(f); st != "unknown" || len(ms) != 0 {
			t.Errorf("%s auth %s %v", file, st, ms)
		}
	}
	idx := pageGet(t, eps, "src/Web/Pages/Index.cshtml", "GET", "/")
	if idx.Attrs["line"] != 1 {
		t.Errorf("line %v", idx.Attrs["line"])
	}
	if o := pageGet(t, eps, "src/Web/Pages/Orders/Index.cshtml", "GET", "/Orders"); o.Attrs["line"] != 2 {
		t.Errorf("line %v", o.Attrs["line"])
	}
	d := pageGet(t, eps, "src/Web/Pages/Orders/Detail.cshtml", "GET", "/Orders/Detail/{id}")
	if d.Attrs["raw"] != "{id:int}" {
		t.Errorf("raw %v", d.Attrs["raw"])
	}
	st, ms := authOf(d)
	if st != "required" || !reflect.DeepEqual(ms, []string{"[Authorize]"}) {
		t.Errorf("code-behind auth %s %v", st, ms)
	}
	found := false
	for _, e := range d.Evidence {
		if e.Path == "src/Web/Pages/Orders/Detail.cshtml.cs" && e.Line == 2 {
			found = true
		}
	}
	if !found || d.Evidence[0].Path != "src/Web/Pages/Orders/Detail.cshtml" || d.Evidence[0].Line != 1 {
		t.Errorf("evidence %v", d.Evidence)
	}
}

func TestRazorPagesAreaAndAnonymous(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Areas/Admin/Pages/Members.cshtml": "@page\n",
		"src/Web/Pages/Login.cshtml":               "@page\n",
		"src/Web/Pages/Login.cshtml.cs":            "[AllowAnonymous]\npublic class LoginModel : PageModel {}\n",
		"src/Web/Pages/Secret.cshtml":              "@page\n@attribute [Authorize(Roles = \"Admin\")]\n",
		"src/Web/Pages/Rooted.cshtml":              "@page \"~/rooted\"\n",
		"src/Web/Stray/Thing.cshtml":               "@page\n",
		"src/Web/Pages/Dynamic.cshtml":             "@page @Foo.Route\n",
	})
	pageGet(t, eps, "src/Web/Areas/Admin/Pages/Members.cshtml", "GET", "/Admin/Members")
	if st, _ := authOf(pageGet(t, eps, "src/Web/Pages/Login.cshtml", "GET", "/Login")); st != "anonymous" {
		t.Errorf("login %s", st)
	}
	if st, ms := authOf(pageGet(t, eps, "src/Web/Pages/Secret.cshtml", "GET", "/Secret")); st != "required" || !reflect.DeepEqual(ms, []string{`[Authorize(Roles = "Admin")]`}) {
		t.Errorf("secret %s %v", st, ms)
	}
	pageGet(t, eps, "src/Web/Pages/Rooted.cshtml", "GET", "/rooted")
	th := pageGet(t, eps, "src/Web/Stray/Thing.cshtml", "GET", "{?}/Stray/Thing")
	if th.Attrs["ext"].(map[string]any)["prefix_unresolved"] != true {
		t.Errorf("stray ext %v", th.Attrs)
	}
	if len(eps) != 5 {
		t.Errorf("endpoints: %v", eps)
	}
}

func TestBlazor(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Components/Pages/Counter.razor": "@page \"/counter\"\n@page \"/counter/{n:int}\"\n<h1>x</h1>\n",
		"src/Web/Components/Pages/Admin.razor":   "@page \"/admin\"\n@attribute [Authorize]\n",
		"src/Web/Components/Pages/Open.razor":    "@page \"/open\"\n@attribute [Microsoft.AspNetCore.Authorization.AllowAnonymous]\n",
		"src/Web/Components/Layout/Nav.razor":    "<nav></nav>\n@code { }\n",
		"src/Web/Components/_Imports.razor":      "@using System\n",
	})
	if len(eps) != 4 {
		t.Fatalf("endpoints: %v", eps)
	}
	c := pageGet(t, eps, "src/Web/Components/Pages/Counter.razor", "GET", "/counter")
	n := pageGet(t, eps, "src/Web/Components/Pages/Counter.razor", "GET", "/counter/{n}")
	if c.Attrs["framework"] != "blazor" || c.Attrs["line"] != 1 || n.Attrs["line"] != 2 || n.Attrs["app_unit"] != "src/Web" {
		t.Errorf("attrs %v %v", c.Attrs, n.Attrs)
	}
	a := pageGet(t, eps, "src/Web/Components/Pages/Admin.razor", "GET", "/admin")
	if st, ms := authOf(a); st != "required" || !reflect.DeepEqual(ms, []string{"[Authorize]"}) {
		t.Errorf("admin %s %v", st, ms)
	}
	if len(a.Evidence) != 2 || a.Evidence[0].Line != 1 {
		t.Errorf("evidence %v", a.Evidence)
	}
	if st, _ := authOf(pageGet(t, eps, "src/Web/Components/Pages/Open.razor", "GET", "/open")); st != "anonymous" {
		t.Errorf("open %s", st)
	}
	if st, _ := authOf(c); st != "unknown" {
		t.Errorf("counter %s", st)
	}
}

func TestBlazorBothRequired(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Pages/Two.razor": "@page \"/a\"\n@page \"/b\"\n@attribute [Authorize]\n",
	})
	for _, p := range []string{"/a", "/b"} {
		if st, _ := authOf(pageGet(t, eps, "src/Web/Pages/Two.razor", "GET", p)); st != "required" {
			t.Errorf("%s %s", p, st)
		}
	}
}

func TestRazorSkips(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"tests/Web.Tests/Web.Tests.csproj":  testProject(`    <PackageReference Include="xunit" Version="2.9.2" />`),
		"tests/Web.Tests/Pages/Fake.cshtml": "@page\n",
		"src/Old/Old.csproj":                `<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003"></Project>`,
		"src/Old/Pages/Legacy.cshtml":       "@page\n",
		"src/Web/Web.csproj":                webProject,
		"src/Web/tests/Spec.razor":          "@page \"/spec\"\n",
		"src/Web/Nested/Nested.csproj":      `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Web/Nested/Pages/Lib.cshtml":   "@page\n",
	})
	eps := byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	f := eps["endpoint:csharp:src/Web/Nested/Pages/Lib.cshtml:GET:/Lib"]
	if _, ok := f.Attrs["app_unit"]; ok || f.Attrs["path"] != "/Lib" {
		t.Errorf("nested: %v", f.Attrs)
	}
}

func policies(facts []fact.Fact) map[string]fact.Fact { return byKey(facts, fact.KindAuthPolicy) }

func TestAuthPolicies(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj": webProject,
		"src/Api/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddAuthorization(o => o.FallbackPolicy = new AuthorizationPolicyBuilder().RequireAuthenticatedUser().Build());
var app = builder.Build();
app.MapControllers().RequireAuthorization();
app.MapRazorPages()
   .RequireAuthorization("Admins");
`,
		"src/Api/Other.cs": `class S { void C(IServiceCollection s) {
  var builder = WebApplication.CreateBuilder();
  builder.Services.AddAuthorizationBuilder().AddPolicy("x", p => p.RequireRole("a")).SetFallbackPolicy(p);
} }
`,
		"src/Api/Nothing.cs": `class N { void C() {
  var builder = WebApplication.CreateBuilder();
  builder.Services.AddAuthorization(o => o.FallbackPolicy = null);
  builder.Services.AddAuthorization(o => { o.DefaultPolicy = p; });
  var app = builder.Build();
  app.MapControllers();
  app.MapControllers().AllowAnonymous();
  services.AddAuthorization(o => o.FallbackPolicy = p);
  other.MapControllers().RequireAuthorization();
  builder.Services.AddAuthorizationBuilder().SetFallbackPolicy(null);
} }
`,
	})
	got := policies(Plugin{}.Run(r).Facts)
	want := map[string]int{
		"auth_policy:csharp:src/Api/Program.cs:2:fallback":    2,
		"auth_policy:csharp:src/Api/Program.cs:4:controllers": 4,
		"auth_policy:csharp:src/Api/Program.cs:6:razor":       6,
		"auth_policy:csharp:src/Api/Other.cs:3:fallback":      3,
	}
	if len(got) != len(want) {
		t.Fatalf("policies: %v", got)
	}
	for k, line := range want {
		f, ok := got[k]
		if !ok {
			t.Fatalf("no %s", k)
		}
		a := f.Attrs
		if a["language"] != "csharp" || a["framework"] != "aspnet" || a["state"] != "required" || a["app_unit"] != "src/Api" || a["line"] != line {
			t.Errorf("%s attrs %v", k, a)
		}
		if len(f.Evidence) != 1 || f.Evidence[0].Line != line || f.Evidence[0].Path != a["file"] {
			t.Errorf("%s evidence %v", k, f.Evidence)
		}
		if f.Plugin != "dotnet" || !strings.HasSuffix(k, ":"+a["scope"].(string)) {
			t.Errorf("%s: %v", k, f)
		}
	}
}

func TestAuthPolicyMapsStillResolve(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
app.MapControllers().RequireAuthorization();
app.MapGet("/x", H);
`)
	if len(eps) != 1 {
		t.Fatalf("endpoints: %v", eps)
	}
	if st, _ := authOf(epGet(t, eps, "GET", "/x")); st != "unknown" {
		t.Errorf("auth %s", st)
	}
}

func TestPagesAndPoliciesSchema(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Web/Web.csproj":         webProject,
		"src/Web/Program.cs":         "var builder = WebApplication.CreateBuilder(args);\nbuilder.Services.AddAuthorization(o => o.FallbackPolicy = p);\nvar app = builder.Build();\napp.MapRazorPages().RequireAuthorization();\n",
		"src/Web/Pages/Index.cshtml": "@page\n@attribute [Authorize]\n",
		"src/Web/Pages/C.razor":      "@page \"/c\"\n",
	})
	data := plugintest.Scan(t, r.Root, Plugin{})
	if err := plugintest.ValidScan(data); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !strings.Contains(string(data), `"auth_policy"`) {
		t.Errorf("no policy in %s", data)
	}
}

func TestRazorPageRootsOnly(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Features/Pages/X.cshtml": "@page\n",
		"src/Web/Pages/Ok.cshtml":         "@page\n",
	})
	x := pageGet(t, eps, "src/Web/Features/Pages/X.cshtml", "GET", "{?}/Features/Pages/X")
	if x.Attrs["ext"].(map[string]any)["prefix_unresolved"] != true {
		t.Errorf("ext %v", x.Attrs)
	}
	pageGet(t, eps, "src/Web/Pages/Ok.cshtml", "GET", "/Ok")
}

func TestUntrackedCodeBehindIgnored(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Web/Web.csproj":        webProject,
		"src/Web/Pages/Open.cshtml": "@page\n",
	})
	if err := os.WriteFile(filepath.Join(r.Root, "src/Web/Pages/Open.cshtml.cs"), []byte("[AllowAnonymous]\npublic class OpenModel : PageModel {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eps := byKey(Plugin{}.Run(r).Facts, fact.KindEndpoint)
	if st, _ := authOf(pageGet(t, eps, "src/Web/Pages/Open.cshtml", "GET", "/Open")); st != "unknown" {
		t.Errorf("state %s", st)
	}
}

func TestPolicyChainAllowAnonymous(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"src/Api/Api.csproj": webProject,
		"src/Api/Program.cs": "var app = builder.Build();\napp.MapControllers().RequireAuthorization().AllowAnonymous();\napp.MapRazorPages().RequireAuthorization().AllowAnonymous();\n",
	})
	if p := policies(Plugin{}.Run(r).Facts); len(p) != 0 {
		t.Errorf("policies %v", p)
	}
}

func TestRazorDirectiveEdges(t *testing.T) {
	eps := pageRepo(t, map[string]string{
		"src/Web/Pages/Bare.razor": "@page\n",
		"src/Web/Pages/Dyn.cshtml": "@page @Foo.Route\n@page \"/second\"\n",
		"src/Web/Pages/Fine.razor": "@page \"/fine\"\n",
	})
	if len(eps) != 1 {
		t.Errorf("endpoints: %v", eps)
	}
	pageGet(t, eps, "src/Web/Pages/Fine.razor", "GET", "/fine")
}

func TestConditionalGroupMarker(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
var g = app.MapGroup("/a");
if (env.IsDevelopment()) { g.RequireAuthorization(); }
g.MapGet("/secret", H);
var h = app.MapGroup("/b");
if (env.IsDevelopment()) { h.AllowAnonymous(); }
h.MapGet("/open", H);
var k = app.MapGroup("/c");
if (env.IsDevelopment()) { k.RequireAuthorization(); k.MapGet("/same", H); }
`)
	st, ms := authOf(epGet(t, eps, "GET", "/a/secret"))
	if st != "unknown" || len(ms) != 1 || !strings.Contains(ms[0], "RequireAuthorization()") || !strings.Contains(ms[0], "env.IsDevelopment()") {
		t.Errorf("conditional required: %s %v", st, ms)
	}
	st, ms = authOf(epGet(t, eps, "GET", "/b/open"))
	if st != "unknown" || len(ms) != 1 || !strings.Contains(ms[0], "AllowAnonymous()") {
		t.Errorf("conditional anonymous: %s %v", st, ms)
	}
	st, ms = authOf(epGet(t, eps, "GET", "/c/same"))
	if st != "required" || !reflect.DeepEqual(ms, []string{"RequireAuthorization()"}) {
		t.Errorf("same condition: %s %v", st, ms)
	}
}

func TestConditionalAnonymousOverRequired(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
var g = app.MapGroup("/a").RequireAuthorization();
if (env.IsDevelopment()) { g.AllowAnonymous(); }
g.MapGet("/x", H);
`)
	if st, _ := authOf(epGet(t, eps, "GET", "/a/x")); st != "unknown" {
		t.Errorf("state %s", st)
	}
}

func TestQualifiedLambdaAttributes(t *testing.T) {
	eps := epRepo(t, `var app = builder.Build();
app.MapGet("/a", [Microsoft.AspNetCore.Authorization.AllowAnonymous] () => "x");
app.MapGet("/b", [Microsoft.AspNetCore.Authorization.Authorize(Roles = "A")] () => "x");
app.MapGet("/c", [AllowAnonymousAttribute] () => "x");
`)
	if st, ms := authOf(epGet(t, eps, "GET", "/a")); st != "anonymous" || len(ms) != 1 {
		t.Errorf("/a %s %v", st, ms)
	}
	if st, ms := authOf(epGet(t, eps, "GET", "/b")); st != "required" || len(ms) != 1 {
		t.Errorf("/b %s %v", st, ms)
	}
	if st, _ := authOf(epGet(t, eps, "GET", "/c")); st != "anonymous" {
		t.Errorf("/c %s", st)
	}
}
