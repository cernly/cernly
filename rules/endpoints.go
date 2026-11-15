package rules

import (
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/hostname"
)

const FindingEndpointAnonymousPublic = "endpoint_anonymous_public"

// EndpointRow is one endpoint fact on one subject: a service, a CI image, or
// the repo when no link covers its registration file.
type EndpointRow struct {
	Endpoint  string       `json:"endpoint"`
	Service   string       `json:"service,omitempty"` // a service key or ci_image key; empty: repo-level
	Method    string       `json:"method"`
	Path      string       `json:"path"`
	Framework string       `json:"framework"`
	Auth      EndpointAuth `json:"auth"`
	Routes    []string     `json:"routes"` // route edge keys that reach it
	Public    bool         `json:"public"`
	Link      string       `json:"link,omitempty"` // "broad": the link keeps more than one app
	From      []string     `json:"from"`
	joined    []int        // indexes into Map.Routes of the edges that reach it
}

// EndpointAuth is the endpoint's auth state; Policy is the auth_policy fact
// that decided it, when one did.
type EndpointAuth struct {
	State   string   `json:"state"`
	Markers []string `json:"markers,omitempty"`
	Policy  string   `json:"policy,omitempty"`
}

// endpoints puts endpoint facts on the services and CI images whose link
// sources cover the registration file (attrs.file), applies auth policies of
// the same subject, and marks rows of broad links.
func endpoints(m *Map, facts []fact.Fact) {
	// One subject per service or CI image: the links of a service are merged,
	// so a file several of them cover gives one row. A file is covered when any
	// link covers it; the subject is kept when any link is kept.
	type scope struct {
		recipe            string
		sources, excluded []string
	}
	type subject struct {
		key     string
		scopes  []scope
		kept    bool
		service bool
	}
	var subs []*subject
	index := map[string]*subject{}
	add := func(key string, service bool, sc scope, kept bool) {
		id := key + "|" + strconv.FormatBool(service)
		s, ok := index[id]
		if !ok {
			s = &subject{key: key, service: service}
			index[id] = s
			subs = append(subs, s)
		}
		s.scopes = append(s.scopes, sc)
		s.kept = s.kept || kept
	}
	for _, l := range m.Links {
		add(l.Service, true, scope{l.Recipe, l.Sources, l.Excluded}, l.Narrowing.State == NarrowingKept)
	}
	for _, c := range m.CIImages {
		add(c.Image, false, scope{c.Recipe, c.Sources, c.Excluded}, c.Narrowing.State == NarrowingKept)
	}
	// coveredBy returns the recipes of the subject's links that cover the file.
	coveredBy := func(s *subject, file string) []string {
		var out []string
		for _, sc := range s.scopes {
			if covers(file, sc.sources, sc.excluded) {
				out = append(out, sc.recipe)
			}
		}
		return out
	}
	var eps, pols []fact.Fact
	for _, f := range facts {
		switch f.Kind {
		case fact.KindEndpoint:
			eps = append(eps, f)
		case fact.KindAuthPolicy:
			pols = append(pols, f)
		}
	}
	policyScope := map[string]string{"controllers": "aspnet-mvc", "razor": "aspnet-razor"}
	var rows []EndpointRow
	for _, s := range subs {
		var mine []EndpointRow
		var mineF []fact.Fact
		units := map[string]bool{}
		for _, e := range eps {
			recipes := coveredBy(s, e.Str("file"))
			if len(recipes) == 0 {
				continue
			}
			if u := e.Str("app_unit"); u != "" {
				units[u] = true
			}
			r := endpointRow(e, s.key)
			r.From = append([]string{e.Key}, recipes...)
			if s.service {
				r.From = append(r.From, s.key)
			}
			mine = append(mine, r)
			mineF = append(mineF, e)
		}
		broad := s.kept && len(units) > 1
		for k := range mine {
			e, r := mineF[k], &mine[k]
			if broad {
				r.Link = "broad" // no policy applies: it may belong to another app
				continue
			}
			if r.Auth.State != "unknown" || hasAnonymousMarker(r.Auth.Markers) {
				continue // an anonymous marker with state unknown: the registrations disagree
			}
			// A specific policy wins over the fallback; ties go to the lowest key.
			var pick *fact.Fact
			for j := range pols {
				p := pols[j]
				if len(coveredBy(s, p.Str("file"))) == 0 {
					continue
				}
				if pu, eu := p.Str("app_unit"), e.Str("app_unit"); pu != "" && eu != "" && pu != eu {
					continue
				}
				pst := p.Str("state")
				if p.Str("framework") != "aspnet" || (pst != "required" && pst != "anonymous") {
					continue
				}
				scope := p.Str("scope")
				fw := e.Str("framework")
				if scope == "fallback" {
					if !strings.HasPrefix(fw, "aspnet") && fw != "blazor" {
						continue
					}
				} else if policyScope[scope] != fw {
					continue
				}
				if pick == nil || (scope != "fallback" && pick.Str("scope") == "fallback") ||
					((scope == "fallback") == (pick.Str("scope") == "fallback") && p.Key < pick.Key) {
					pick = &pols[j]
				}
			}
			if pick != nil {
				r.Auth.State, r.Auth.Policy = pick.Str("state"), pick.Key
				r.From = append(r.From, pick.Key)
			}
		}
		rows = append(rows, mine...)
	}
	covered := map[string]bool{}
	for _, r := range rows {
		covered[r.Endpoint] = true
	}
	for _, e := range eps {
		if !covered[e.Key] {
			r := endpointRow(e, "")
			r.From = []string{e.Key}
			rows = append(rows, r)
		}
	}
	for k := range rows {
		rows[k].From = uniq(rows[k].From)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		return a.Endpoint < b.Endpoint
	})
	routeFacts := map[string]fact.Fact{}
	for _, f := range facts {
		if f.Kind == fact.KindRoute {
			routeFacts[f.Key] = f
		}
	}
	for k := range rows {
		rows[k].joined = joinedEdges(m, routeFacts, rows[k])
		for _, j := range rows[k].joined {
			rows[k].Routes = append(rows[k].Routes, m.Routes[j].Route)
			if publicRoute(routeFacts[m.Routes[j].Route]) {
				rows[k].Public = true
			}
		}
		rows[k].Routes = uniq(rows[k].Routes)
	}
	m.Endpoints = rows
}

// joinedEdges returns the indexes of the route edges that reach the row: edges
// to its service whose match covers the endpoint's path as the app sees it.
// Repo-level and broad rows are never reached.
func joinedEdges(m *Map, routeFacts map[string]fact.Fact, r EndpointRow) []int {
	if r.Service == "" || r.Link == "broad" {
		return nil
	}
	var out []int
	for i, e := range m.Routes {
		if e.To == r.Service && routeReaches(routeFacts[e.Route], e.Rewrite, r.Path) {
			out = append(out, i)
		}
	}
	return out
}

// publicRoute: the route serves a host that hostname.Public accepts.
func publicRoute(f fact.Fact) bool {
	for _, a := range maps(f.Attrs["addresses"]) {
		if h, _ := a["host"].(string); hostname.Public(h) {
			return true
		}
	}
	return false
}

// routeReaches: the route's match covers the endpoint path. A rewrite that is
// unknown or missing, a regex or "other" matcher, or a match type that is
// missing reaches nothing by path; only a host-only route without an unknown
// rewrite covers everything.
func routeReaches(f fact.Fact, rw *Rewrite, path string) bool {
	if rw == nil || rw.Kind == "unknown" || rw.Kind == "" {
		return false
	}
	typ := f.Str("match_type")
	if typ == "host" {
		return true
	}
	if typ != "prefix" && typ != "exact" {
		return false
	}
	ep := pathSegs(path)
	if len(ep) > 0 && ep[0] == "{?}" || strings.HasPrefix(path, "{?}") {
		return false // the endpoint's own prefix is unresolved: only a host route reaches it
	}
	for _, p := range f.Strs("paths") {
		req := pathSegs(routePath(p))
		var want []string // the app-side prefix (prefix match) or path (exact match)
		switch rw.Kind {
		case "strip":
			s := pathSegs(rw.Prefix)
			switch {
			case segmentsPrefix(s, req):
				want = req[len(s):]
			case typ == "prefix" && segmentsPrefix(req, s):
				return true // the strip is deeper than the match: any app path
			default:
				want = req // the strip never fires
			}
		case "replace":
			want = pathSegs(rw.To)
			// proxy_pass http://app/v2 for location /api/ gives /v2x for /api/x.
			if strings.HasSuffix(rw.Prefix, "/") && !strings.HasSuffix(rw.To, "/") {
				if len(ep) == len(want) && segmentsPrefix(want, ep) {
					return true
				}
				continue
			}
		default:
			want = req
		}
		if typ == "exact" && len(ep) == len(want) && segmentsPrefix(want, ep) || typ == "prefix" && segmentsPrefix(want, ep) {
			return true
		}
	}
	return false
}

// routePath reads the request path of a route's path matcher: the last word
// (nginx writes "= /x"), without a trailing *. A trailing / is dropped when the
// path is split into segments, so /api/* is taken to reach the endpoint /api
// (a request for /api/ reaches an ASP.NET /api route).
func routePath(p string) string {
	if i := strings.LastIndex(p, " "); i >= 0 {
		p = p[i+1:]
	}
	p = strings.TrimSuffix(p, "*")
	return p
}

func pathSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// segmentsPrefix: prefix is a whole-segment prefix of path. A literal segment
// never matches a {param} segment.
func segmentsPrefix(prefix, path []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i, s := range prefix {
		if !strings.EqualFold(s, path[i]) {
			return false
		}
	}
	return true
}

// endpointFindings raises endpoint_anonymous_public: an anonymous endpoint
// that an edge reaches on a public host in a production (confirmed or
// unconfirmed) stack. It runs after the stack roles are known. One finding per
// endpoint; the lowest route key among the qualifying edges is in Detail.
func endpointFindings(m *Map, facts []fact.Fact) {
	routeFacts := map[string]fact.Fact{}
	for _, f := range facts {
		if f.Kind == fact.KindRoute {
			routeFacts[f.Key] = f
		}
	}
	prod := map[string]bool{}
	for _, s := range m.Stacks {
		if s.Role == RoleProduction || s.Role == RoleUnconfirmed {
			prod[s.Stack] = true
		}
	}
	seen := map[string]bool{}
	for _, r := range m.Endpoints {
		if r.Auth.State != "anonymous" || seen[r.Endpoint] {
			continue
		}
		best := ""
		for _, j := range r.joined {
			e := m.Routes[j]
			if e.Stack != "" && prod[e.Stack] && publicRoute(routeFacts[e.Route]) && (best == "" || e.Route < best) {
				best = e.Route
			}
		}
		if best == "" {
			continue
		}
		seen[r.Endpoint] = true
		detail := best
		if len(r.Auth.Markers) > 0 {
			detail += " (" + r.Auth.Markers[0] + ")"
		}
		from := []string{r.Endpoint, best}
		if strings.HasPrefix(r.Service, "service:") {
			from = append(from, r.Service)
		}
		m.Findings = append(m.Findings, Finding{Rule: FindingEndpointAnonymousPublic, Subject: r.Endpoint, Detail: detail, From: from})
	}
}

func endpointRow(e fact.Fact, service string) EndpointRow {
	au, _ := e.Attrs["auth"].(map[string]any)
	state, _ := au["state"].(string)
	var markers []string
	switch ms := au["markers"].(type) {
	case []any:
		for _, x := range ms {
			if s, ok := x.(string); ok {
				markers = append(markers, s)
			}
		}
	case []string:
		markers = ms
	}
	return EndpointRow{Endpoint: e.Key, Service: service, Method: e.Str("method"), Path: e.Str("path"),
		Framework: e.Str("framework"), Auth: EndpointAuth{State: state, Markers: markers}, Routes: []string{}}
}

func hasAnonymousMarker(markers []string) bool {
	for _, m := range markers {
		if strings.Contains(m, "AllowAnonymous") {
			return true
		}
	}
	return false
}
