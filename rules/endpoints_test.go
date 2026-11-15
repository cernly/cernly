package rules

import (
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
)

func epFact(key, file, fw, method, path, auth, unit string) fact.Fact {
	a := map[string]any{"file": file, "line": 3, "framework": fw, "method": method, "path": path,
		"auth": map[string]any{"state": auth, "markers": []any{}}}
	if unit != "" {
		a["app_unit"] = unit
	}
	return fact.Fact{Kind: fact.KindEndpoint, Key: key, Attrs: a,
		Evidence: []fact.Evidence{{Path: "src/Lib/Routes.cs", Line: 1}, {Path: file, Line: 3}}}
}

func polFact(key, file, scope string) fact.Fact {
	return fact.Fact{Kind: fact.KindAuthPolicy, Key: key, Attrs: map[string]any{"file": file, "line": 1, "scope": scope, "state": "required", "framework": "aspnet"}}
}

func epMap(links ...Link) *Map {
	return &Map{Links: links, CIImages: []CIImage{}, Routes: []RouteEdge{}}
}

func keptLink(svc string, sources ...string) Link {
	return Link{Service: svc, Recipe: "image_recipe:Dockerfile:_final", Sources: sources, Narrowing: Narrowing{State: NarrowingKept}}
}

func TestEndpointAttachByRegistration(t *testing.T) {
	m := epMap(keptLink("service:web", "src/Web"), keptLink("service:lib", "src/Lib"))
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/Web/Program.cs", "aspnet-minimal", "GET", "/a", "unknown", "")})
	if len(m.Endpoints) != 1 || m.Endpoints[0].Service != "service:web" {
		t.Fatalf("%+v", m.Endpoints)
	}
	r := m.Endpoints[0]
	if r.Routes == nil || r.Public || !reflect.DeepEqual(r.From, []string{"endpoint:a", "image_recipe:Dockerfile:_final", "service:web"}) {
		t.Fatalf("%+v", r)
	}
}

func TestEndpointRepoLevel(t *testing.T) {
	m := epMap(keptLink("service:web", "src/Web"))
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/Other/P.cs", "aspnet-minimal", "GET", "/a", "unknown", "")})
	if len(m.Endpoints) != 1 || m.Endpoints[0].Service != "" {
		t.Fatalf("%+v", m.Endpoints)
	}
}

func TestPolicyApplies(t *testing.T) {
	m := epMap(keptLink("service:web", "src/Web"), keptLink("service:other", "src/Other"))
	facts := []fact.Fact{
		epFact("endpoint:mvc", "src/Web/C.cs", "aspnet-mvc", "GET", "/mvc", "unknown", ""),
		epFact("endpoint:anon", "src/Web/C.cs", "aspnet-mvc", "GET", "/anon", "anonymous", ""),
		epFact("endpoint:min", "src/Web/P.cs", "aspnet-minimal", "GET", "/min", "unknown", ""),
		epFact("endpoint:razor", "src/Web/Pages/I.cshtml.cs", "aspnet-razor", "GET", "/r", "unknown", ""),
		epFact("endpoint:o", "src/Other/C.cs", "aspnet-mvc", "GET", "/o", "unknown", ""),
		polFact("auth_policy:c", "src/Web/Program.cs", "controllers"),
	}
	endpoints(m, facts)
	got := map[string]EndpointAuth{}
	for _, r := range m.Endpoints {
		got[r.Endpoint] = r.Auth
	}
	if a := got["endpoint:mvc"]; a.State != "required" || a.Policy != "auth_policy:c" {
		t.Errorf("mvc %+v", a)
	}
	if got["endpoint:anon"].State != "anonymous" || got["endpoint:min"].State != "unknown" || got["endpoint:razor"].State != "unknown" || got["endpoint:o"].State != "unknown" {
		t.Errorf("%+v", got)
	}
	facts = append(facts[:5:5], polFact("auth_policy:f", "src/Web/Program.cs", "fallback"), polFact("auth_policy:r", "src/Web/Program.cs", "razor"))
	m = epMap(keptLink("service:web", "src/Web"), keptLink("service:other", "src/Other"))
	endpoints(m, facts)
	got = map[string]EndpointAuth{}
	for _, r := range m.Endpoints {
		got[r.Endpoint] = r.Auth
	}
	if got["endpoint:min"].State != "required" || got["endpoint:min"].Policy != "auth_policy:f" ||
		got["endpoint:razor"].Policy != "auth_policy:r" || got["endpoint:anon"].State != "anonymous" || got["endpoint:o"].State != "unknown" {
		t.Errorf("%+v", got)
	}
}

func TestBroadLink(t *testing.T) {
	two := []fact.Fact{
		epFact("endpoint:a", "src/A/P.cs", "aspnet-minimal", "GET", "/a", "unknown", "src/A"),
		epFact("endpoint:b", "src/B/P.cs", "aspnet-minimal", "GET", "/b", "unknown", "src/B"),
	}
	m := epMap(keptLink("service:web", "."))
	endpoints(m, two)
	for _, r := range m.Endpoints {
		if r.Link != "broad" {
			t.Errorf("%+v", r)
		}
	}
	m = epMap(keptLink("service:web", "."))
	endpoints(m, two[:1])
	if m.Endpoints[0].Link != "" {
		t.Errorf("one app: %+v", m.Endpoints[0])
	}
	l := keptLink("service:web", ".")
	l.Narrowing.State = NarrowingNarrowed
	m = epMap(l)
	endpoints(m, two)
	for _, r := range m.Endpoints {
		if r.Link != "" {
			t.Errorf("narrowed: %+v", r)
		}
	}
}

func TestPolicyNotAcrossApps(t *testing.T) {
	pol := polFact("auth_policy:a", "src/A/Program.cs", "controllers")
	pol.Attrs["app_unit"] = "src/A"
	m := epMap(keptLink("service:web", "src"))
	endpoints(m, []fact.Fact{
		epFact("endpoint:a", "src/A/C.cs", "aspnet-mvc", "GET", "/a", "unknown", "src/A"),
		epFact("endpoint:b", "src/B/C.cs", "aspnet-mvc", "GET", "/b", "unknown", "src/B"), pol})
	// Two apps on a kept link: broad, so no policy applies at all.
	for _, r := range m.Endpoints {
		if r.Auth.State != "unknown" || r.Link != "broad" {
			t.Errorf("broad: %+v", r)
		}
	}
	// One app unit among the endpoints, but a policy of another app: not applied.
	pol.Attrs["app_unit"] = "src/Z"
	m = epMap(keptLink("service:web", "src"))
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/A/C.cs", "aspnet-mvc", "GET", "/a", "unknown", "src/A"), pol})
	if r := m.Endpoints[0]; r.Auth.State != "unknown" || r.Auth.Policy != "" {
		t.Errorf("other app's policy applied: %+v", r)
	}
	pol.Attrs["app_unit"] = "src/A"
	m = epMap(keptLink("service:web", "src"))
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/A/C.cs", "aspnet-mvc", "GET", "/a", "unknown", "src/A"), pol})
	if r := m.Endpoints[0]; r.Auth.State != "required" {
		t.Errorf("same app's policy not applied: %+v", r)
	}
}

func TestPolicyStateAndFramework(t *testing.T) {
	anon := polFact("auth_policy:anon", "src/Web/Program.cs", "fallback")
	anon.Attrs["state"] = "anonymous"
	spring := polFact("auth_policy:spring", "src/Web/Program.cs", "fallback")
	spring.Attrs["framework"] = "spring"
	odd := polFact("auth_policy:odd", "src/Web/Program.cs", "controllers")
	odd.Attrs["state"] = "maybe"
	m := epMap(keptLink("service:web", "src/Web"))
	endpoints(m, []fact.Fact{
		epFact("endpoint:u", "src/Web/P.cs", "aspnet-minimal", "GET", "/u", "unknown", ""),
		epFact("endpoint:b", "src/Web/B.razor", "blazor", "GET", "/b", "unknown", ""),
		epFact("endpoint:r", "src/Web/P.cs", "aspnet-minimal", "GET", "/r", "required", ""),
		epFact("endpoint:c", "src/Web/C.cs", "aspnet-mvc", "GET", "/c", "unknown", ""), anon, odd})
	got := map[string]EndpointAuth{}
	for _, r := range m.Endpoints {
		got[r.Endpoint] = r.Auth
	}
	if got["endpoint:u"].State != "anonymous" || got["endpoint:u"].Policy != "auth_policy:anon" || got["endpoint:b"].State != "anonymous" ||
		got["endpoint:r"].State != "required" || got["endpoint:r"].Policy != "" {
		t.Errorf("%+v", got)
	}
	m = epMap(keptLink("service:web", "src/Web"))
	endpoints(m, []fact.Fact{epFact("endpoint:u", "src/Web/P.cs", "aspnet-minimal", "GET", "/u", "unknown", ""), spring, odd,
		epFact("endpoint:c", "src/Web/C.cs", "aspnet-mvc", "GET", "/c", "unknown", "")})
	for _, r := range m.Endpoints {
		if r.Auth.State != "unknown" || r.Auth.Policy != "" {
			t.Errorf("spring or odd-state policy applied: %+v", r)
		}
	}
}

func TestEndpointCIImageSubject(t *testing.T) {
	m := &Map{CIImages: []CIImage{{Image: "ci_image:Dockerfile:_final", Recipe: "image_recipe:Dockerfile:_final", Sources: []string{"src/Web"}, Narrowing: Narrowing{State: NarrowingKept}}}, Routes: []RouteEdge{}}
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/Web/Program.cs", "aspnet-minimal", "GET", "/a", "unknown", "")})
	if len(m.Endpoints) != 1 || m.Endpoints[0].Service != "ci_image:Dockerfile:_final" ||
		!reflect.DeepEqual(m.Endpoints[0].From, []string{"endpoint:a", "image_recipe:Dockerfile:_final"}) {
		t.Fatalf("%+v", m.Endpoints)
	}
}

// routeFact builds a route fact with the match attrs the plugins give.
func routeFact(key, matchType string, paths []any, rw map[string]any, hosts ...string) fact.Fact {
	addrs := []any{}
	for _, h := range hosts {
		addrs = append(addrs, map[string]any{"address": h, "host": h})
	}
	a := map[string]any{"addresses": addrs, "match_type": matchType, "rewrite": rw, "action": "proxy"}
	if paths != nil {
		a["paths"] = paths
	}
	return fact.Fact{Kind: fact.KindRoute, Key: key, Attrs: a}
}

func joinedPaths(t *testing.T, rf fact.Fact, rw *Rewrite, paths ...string) map[string]bool {
	t.Helper()
	m := epMap(keptLink("service:web", "src/Web"))
	m.Routes = []RouteEdge{{Route: rf.Key, Stack: "stack:compose:p", To: "service:web", Rewrite: rw, From: []string{rf.Key}}}
	facts := []fact.Fact{rf}
	for _, p := range paths {
		facts = append(facts, epFact("endpoint:"+p, "src/Web/P.cs", "aspnet-minimal", "GET", p, "unknown", ""))
	}
	endpoints(m, facts)
	got := map[string]bool{}
	for _, r := range m.Endpoints {
		got[r.Path] = len(r.Routes) > 0
	}
	return got
}

func none() map[string]any { return map[string]any{"kind": "none"} }

func TestJoinPrefix(t *testing.T) {
	rf := routeFact("route:caddy:C:e.acme-shop.net:/api/*", "prefix", []any{"/api/*"}, none(), "e.acme-shop.net")
	got := joinedPaths(t, rf, &Rewrite{Kind: "none"}, "/api/orders", "/api", "/apix", "/health", "/api/{id}")
	if !got["/api/orders"] || !got["/api"] || got["/apix"] || got["/health"] {
		t.Errorf("%v", got)
	}
	// Prefix literal against a {param} segment does not match.
	rf = routeFact("route:x", "prefix", []any{"/api/v1/*"}, none(), "e.acme-shop.net")
	if got := joinedPaths(t, rf, &Rewrite{Kind: "none"}, "/api/{v}/x", "/api/v1/x"); got["/api/{v}/x"] || !got["/api/v1/x"] {
		t.Errorf("%v", got)
	}
}

func TestJoinStrip(t *testing.T) {
	strip := func(prefix string) *Rewrite { return &Rewrite{Kind: "strip", Prefix: prefix} }
	// M equals S (handle_path /api/*): every endpoint.
	rf := routeFact("route:a", "prefix", []any{"/api/*"}, nil, "e.acme-shop.net")
	if got := joinedPaths(t, rf, strip("/api"), "/orders", "/health", "/x/y"); !got["/orders"] || !got["/health"] || !got["/x/y"] {
		t.Errorf("M=S: %v", got)
	}
	// M extends S: PathPrefix(/api/v1) with stripprefix /api reaches only /v1/...
	rf = routeFact("route:b", "prefix", []any{"/api/v1*"}, nil, "e.acme-shop.net")
	got := joinedPaths(t, rf, strip("/api"), "/v1/orders", "/v1", "/health", "/api/v1/x")
	if !got["/v1/orders"] || !got["/v1"] || got["/health"] || got["/api/v1/x"] {
		t.Errorf("M extends S: %v", got)
	}
	// S extends M: the strip is deeper than the match: every endpoint.
	rf = routeFact("route:c", "prefix", []any{"/api/*"}, nil, "e.acme-shop.net")
	if got := joinedPaths(t, rf, strip("/api/v1"), "/orders", "/health"); !got["/orders"] || !got["/health"] {
		t.Errorf("S extends M: %v", got)
	}
	// Unrelated: the strip never fires, the app sees the matched path.
	got = joinedPaths(t, rf, strip("/v1"), "/api/orders", "/api", "/health")
	if !got["/api/orders"] || !got["/api"] || got["/health"] {
		t.Errorf("unrelated: %v", got)
	}
	// Exact, S strictly extends M: the strip cannot fire, the app sees M.
	rf = routeFact("route:e", "exact", []any{"/api"}, nil, "e.acme-shop.net")
	got = joinedPaths(t, rf, strip("/api/v1"), "/api", "/x", "/v1")
	if !got["/api"] || got["/x"] || got["/v1"] {
		t.Errorf("exact, S extends M: %v", got)
	}
	// Exact: the exact path minus S.
	rf = routeFact("route:d", "exact", []any{"/api/x"}, nil, "e.acme-shop.net")
	got = joinedPaths(t, rf, strip("/api"), "/x", "/api/x", "/y")
	if !got["/x"] || got["/api/x"] || got["/y"] {
		t.Errorf("exact: %v", got)
	}
}

func TestJoinReplace(t *testing.T) {
	rf := routeFact("route:nginx:n", "prefix", []any{"/api/"}, nil, "e.acme-shop.net")
	got := joinedPaths(t, rf, &Rewrite{Kind: "replace", Prefix: "/api/", To: "/"}, "/orders", "/health")
	if !got["/orders"] || !got["/health"] {
		t.Errorf("to /: %v", got)
	}
	got = joinedPaths(t, rf, &Rewrite{Kind: "replace", Prefix: "/api/", To: "/v2/"}, "/v2/orders", "/orders", "/v2")
	if !got["/v2/orders"] || got["/orders"] || !got["/v2"] {
		t.Errorf("to /v2/: %v", got)
	}
}

func TestJoinReplaceNoTrailingSlash(t *testing.T) {
	// location /api/ with proxy_pass http://web/v2: /api/x becomes /v2x.
	rf := routeFact("route:nginx:n", "prefix", []any{"/api/"}, nil, "e.acme-shop.net")
	got := joinedPaths(t, rf, &Rewrite{Kind: "replace", Prefix: "/api/", To: "/v2"}, "/v2", "/v2/orders", "/orders")
	if !got["/v2"] || got["/v2/orders"] || got["/orders"] {
		t.Errorf("%v", got)
	}
}

func TestJoinExactRegexUnknown(t *testing.T) {
	rf := routeFact("route:e", "exact", []any{"= /health"}, none(), "e.acme-shop.net")
	got := joinedPaths(t, rf, &Rewrite{Kind: "none"}, "/health", "/health/x", "/other")
	if !got["/health"] || got["/health/x"] || got["/other"] {
		t.Errorf("exact: %v", got)
	}
	for _, c := range []struct {
		typ string
		rw  *Rewrite
	}{{"regex", &Rewrite{Kind: "none"}}, {"other", &Rewrite{Kind: "none"}}, {"prefix", &Rewrite{Kind: "unknown"}}, {"host", &Rewrite{Kind: "unknown"}}} {
		rf := routeFact("route:r", c.typ, []any{"/health"}, nil, "e.acme-shop.net")
		if got := joinedPaths(t, rf, c.rw, "/health", "/x"); got["/health"] || got["/x"] {
			t.Errorf("%s %v: %v", c.typ, c.rw, got)
		}
	}
	// A route without the new attrs (an old scan) joins nothing by path.
	rf = routeFact("route:old", "", []any{"/health"}, nil, "e.acme-shop.net")
	delete(rf.Attrs, "match_type")
	if got := joinedPaths(t, rf, nil, "/health"); got["/health"] {
		t.Errorf("old route: %v", got)
	}
}

func TestJoinHostAndUnresolved(t *testing.T) {
	rf := routeFact("route:h", "host", nil, none(), "e.acme-shop.net")
	got := joinedPaths(t, rf, &Rewrite{Kind: "none"}, "{?}/x", "/a")
	if !got["{?}/x"] || !got["/a"] {
		t.Errorf("host: %v", got)
	}
	rf = routeFact("route:p", "prefix", []any{"/api/*"}, none(), "e.acme-shop.net")
	if got := joinedPaths(t, rf, &Rewrite{Kind: "none"}, "{?}/x"); got["{?}/x"] {
		t.Errorf("prefix reached an unresolved path: %v", got)
	}
}

func TestJoinBroadSkipped(t *testing.T) {
	two := []fact.Fact{
		epFact("endpoint:a", "src/A/P.cs", "aspnet-minimal", "GET", "/a", "anonymous", "src/A"),
		epFact("endpoint:b", "src/B/P.cs", "aspnet-minimal", "GET", "/b", "unknown", "src/B"),
		routeFact("route:h", "host", nil, none(), "e.acme-shop.net"),
	}
	m := epMap(keptLink("service:web", "."))
	m.Routes = []RouteEdge{{Route: "route:h", Stack: "stack:compose:p", To: "service:web", Rewrite: &Rewrite{Kind: "none"}}}
	m.Stacks = []StackRole{{Stack: "stack:compose:p", Role: RoleProduction}}
	endpoints(m, two)
	for _, r := range m.Endpoints {
		if r.Link != "broad" || len(r.Routes) != 0 || r.Public {
			t.Errorf("%+v", r)
		}
	}
	endpointFindings(m, two)
	if len(m.Findings) != 0 {
		t.Errorf("%+v", m.Findings)
	}
}

func findingMap(host, role string, state string) (*Map, []fact.Fact) {
	e := epFact("endpoint:a", "src/Web/P.cs", "aspnet-minimal", "GET", "/health", state, "")
	e.Attrs["auth"] = map[string]any{"state": state, "markers": []any{"AllowAnonymous"}}
	facts := []fact.Fact{e,
		routeFact("route:z", "host", nil, none(), host),
		routeFact("route:b", "prefix", []any{"/health*"}, none(), host)}
	m := epMap(keptLink("service:web", "src/Web"))
	m.Routes = []RouteEdge{
		{Route: "route:z", Stack: "stack:compose:p", To: "service:web", Rewrite: &Rewrite{Kind: "none"}},
		{Route: "route:b", Stack: "stack:compose:p", To: "service:web", Rewrite: &Rewrite{Kind: "none"}},
	}
	m.Stacks = []StackRole{{Stack: "stack:compose:p", Role: role}}
	return m, facts
}

func TestFindingAnonymousPublic(t *testing.T) {
	for _, role := range []string{RoleProduction, RoleUnconfirmed} {
		m, facts := findingMap("app.acme-shop.net", role, "anonymous")
		endpoints(m, facts)
		endpointFindings(m, facts)
		if len(m.Findings) != 1 {
			t.Fatalf("%s: %+v", role, m.Findings)
		}
		f := m.Findings[0]
		if f.Rule != FindingEndpointAnonymousPublic || f.Subject != "endpoint:a" || f.Detail != "route:b (AllowAnonymous)" ||
			!reflect.DeepEqual(f.From, []string{"endpoint:a", "route:b", "service:web"}) {
			t.Errorf("%+v", f)
		}
		if !m.Endpoints[0].Public || !reflect.DeepEqual(m.Endpoints[0].Routes, []string{"route:b", "route:z"}) {
			t.Errorf("%+v", m.Endpoints[0])
		}
	}
	for name, c := range map[string][3]string{
		"localhost": {"localhost", RoleProduction, "anonymous"},
		"role":      {"app.acme-shop.net", RoleNone, "anonymous"},
		"required":  {"app.acme-shop.net", RoleProduction, "required"},
		"unknown":   {"app.acme-shop.net", RoleProduction, "unknown"},
	} {
		m, facts := findingMap(c[0], c[1], c[2])
		endpoints(m, facts)
		endpointFindings(m, facts)
		if len(m.Findings) != 0 {
			t.Errorf("%s: %+v", name, m.Findings)
		}
	}
}

func TestEndpointCounts(t *testing.T) {
	facts := []fact.Fact{
		epFact("endpoint:r1", "src/Web/P.cs", "aspnet-minimal", "GET", "/r1", "required", ""),
		epFact("endpoint:r2", "src/Web/P.cs", "aspnet-minimal", "GET", "/r2", "required", ""),
		epFact("endpoint:an", "src/Web/P.cs", "aspnet-minimal", "GET", "/an", "anonymous", ""),
		epFact("endpoint:un", "src/Web/P.cs", "aspnet-minimal", "GET", "/un", "unknown", ""),
	}
	m := epMap(keptLink("service:web", "src/Web"))
	endpoints(m, facts)
	m.Stacks = []StackRole{}
	facts = append(facts, fact.Fact{Kind: fact.KindService, Key: "service:web", Attrs: map[string]any{"stack": "p", "name": "web"}})
	mt := metrics(m, facts)
	var got *ServiceMetrics
	for i := range mt.Services {
		if mt.Services[i].Service == "service:web" {
			got = &mt.Services[i]
		}
	}
	if got == nil || got.Endpoints == nil || *got.Endpoints != (EndpointCounts{Total: 4, Required: 2, Anonymous: 1, Unknown: 1}) ||
		!reflect.DeepEqual(got.Items.Endpoints, []string{"endpoint:an", "endpoint:r1", "endpoint:r2", "endpoint:un"}) {
		t.Fatalf("%+v", got)
	}
}

func TestRowsJoinOnce(t *testing.T) {
	m, facts := findingMap("app.acme-shop.net", RoleProduction, "anonymous")
	endpoints(m, facts)
	if len(m.Endpoints[0].joined) != 2 {
		t.Fatalf("joined %v", m.Endpoints[0].joined)
	}
}

func TestEndpointSeveralLinksOneRowPerEndpoint(t *testing.T) {
	a, b := keptLink("service:web", "src/Web"), keptLink("service:web", "src")
	b.Recipe = "image_recipe:Other:_final"
	m := epMap(a, b)
	endpoints(m, []fact.Fact{epFact("endpoint:a", "src/Web/Program.cs", "aspnet-minimal", "GET", "/a", "unknown", "")})
	if len(m.Endpoints) != 1 || m.Endpoints[0].Service != "service:web" {
		t.Fatalf("%+v", m.Endpoints)
	}
}

func TestEndpointSeveralLinksKeptIfAnyKept(t *testing.T) {
	a, b := keptLink("service:web", "src"), keptLink("service:web", "src")
	a.Narrowing.State = NarrowingNarrowed
	m := epMap(a, b)
	endpoints(m, []fact.Fact{
		epFact("endpoint:a", "src/Web/Program.cs", "aspnet-minimal", "GET", "/a", "unknown", "src/Web"),
		epFact("endpoint:b", "src/Api/Program.cs", "aspnet-minimal", "GET", "/b", "unknown", "src/Api"),
	})
	if len(m.Endpoints) != 2 || m.Endpoints[0].Link != "broad" {
		t.Fatalf("%+v", m.Endpoints)
	}
}

// A row whose registrations disagree (unknown, with an anonymous marker kept)
// does not get a fallback policy: it must not read as protected.
func TestPolicyNotAppliedWithAnonymousMarker(t *testing.T) {
	e := epFact("endpoint:a", "src/Web/P.cs", "aspnet-minimal", "GET", "/r", "unknown", "")
	e.Attrs["auth"] = map[string]any{"state": "unknown", "markers": []any{"AllowAnonymous()"}}
	m := epMap(keptLink("service:web", "src/Web"))
	endpoints(m, []fact.Fact{e, polFact("auth_policy:f", "src/Web/P.cs", "fallback")})
	if len(m.Endpoints) != 1 {
		t.Fatalf("%+v", m.Endpoints)
	}
	if a := m.Endpoints[0].Auth; a.State != "unknown" || a.Policy != "" {
		t.Errorf("policy applied: %+v", a)
	}
}
