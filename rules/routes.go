package rules

import (
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/hostname"
)

// RouteEdge is one route of a web server config: the stack it runs in,
// through the service that mounts the config file, and the service it sends
// the request to. An upstream that names a service of the same stack becomes
// that edge; any other upstream (host.docker.internal, an outside address)
// stays as written, with no target.
type RouteEdge struct {
	Route    string   `json:"route"`
	Stack    string   `json:"stack,omitempty"` // empty: no compose service mounts the file
	Via      string   `json:"via,omitempty"`   // the service that mounts the file
	Sites    []string `json:"sites"`
	Match    string   `json:"match"`
	Action   string   `json:"action"`
	Upstream string   `json:"upstream,omitempty"`
	To       string   `json:"to,omitempty"`
	From     []string `json:"from"`
}

// routes joins route facts to the stacks whose services mount their file. A
// route from container labels (labels_on: the labelled service) is placed in
// every stack whose compose files hold it, through the stack's service that
// runs the proxy image the route names (proxy_image).
func routes(m *Map, facts []fact.Fact) {
	type mount struct{ stack, service string }
	mounts := map[string][]mount{}            // repo file -> services that mount it
	members := map[string]map[string]string{} // stack name -> service name -> fact key
	images := map[string]map[string]string{}  // stack name -> image name (last path segment) -> service key
	stackFiles := map[string][]string{}       // repo file -> stacks that use it
	for _, f := range facts {
		if f.Kind == fact.KindStack {
			for _, file := range f.Strs("files") {
				stackFiles[file] = append(stackFiles[file], f.Str("name"))
			}
		}
	}
	for _, f := range facts {
		if f.Kind != fact.KindService && f.Kind != fact.KindDatastore {
			continue
		}
		st, name := f.Str("stack"), f.Str("name")
		if members[st] == nil {
			members[st] = map[string]string{}
		}
		members[st][name] = f.Key
		if ref, ok := f.Attrs["image_ref"].(map[string]any); ok {
			if repoName, _ := ref["repository"].(string); repoName != "" {
				if images[st] == nil {
					images[st] = map[string]string{}
				}
				img := repoName[strings.LastIndex(repoName, "/")+1:]
				if _, taken := images[st][img]; !taken {
					images[st][img] = f.Key
				}
			}
		}
		for _, mt := range maps(f.Attrs["mounts"]) {
			if src, _ := mt["source"].(string); src != "" {
				mounts[src] = append(mounts[src], mount{st, f.Key})
			}
		}
	}
	for _, f := range facts {
		if f.Kind != fact.KindRoute {
			continue
		}
		var sites []string
		for _, a := range maps(f.Attrs["addresses"]) {
			s, _ := a["address"].(string)
			sites = append(sites, s)
		}
		var ups []map[string]any
		ups = append(ups, maps(f.Attrs["upstreams"])...)
		if len(ups) == 0 {
			ups = []map[string]any{nil}
		}
		base := RouteEdge{Route: f.Key, Sites: sites, Match: match(f), Action: f.Str("action")}
		at := mounts[f.Str("file")]
		if on := f.Str("labels_on"); on != "" {
			at = nil
			for _, st := range stackFiles[f.Str("file")] {
				if via, ok := images[st][f.Str("proxy_image")]; ok && members[st][on] != "" {
					at = append(at, mount{st, via})
				}
			}
		}
		if len(at) == 0 {
			at = []mount{{}}
		}
		for _, mt := range at {
			for _, u := range ups {
				e := base
				e.From = []string{f.Key}
				if mt.service != "" {
					e.Stack, e.Via = "stack:compose:"+mt.stack, mt.service
					e.From = append(e.From, mt.service)
				}
				if u != nil {
					e.Upstream, _ = u["address"].(string)
					if h, _ := u["host"].(string); h != "" && mt.service != "" {
						if to, ok := members[mt.stack][h]; ok {
							e.To = to
							e.From = append(e.From, to)
						}
					}
				}
				e.From = uniq(e.From)
				m.Routes = append(m.Routes, e)
			}
		}
	}
	sort.SliceStable(m.Routes, func(i, j int) bool {
		a, b := m.Routes[i], m.Routes[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		if a.Via != b.Via {
			return a.Via < b.Via
		}
		return a.Upstream < b.Upstream
	})
}

// match is a route's matcher in one line: methods, paths, or a named matcher.
func match(f fact.Fact) string {
	var parts []string
	if ms := f.Strs("methods"); len(ms) > 0 {
		parts = append(parts, strings.Join(ms, ","))
	}
	if ps := f.Strs("paths"); len(ps) > 0 {
		parts = append(parts, strings.Join(ps, ","))
	}
	if n := f.Str("matcher"); n != "" {
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, " ")
}

// servedHosts returns the public hosts the repo's route facts serve, each
// with the routes that serve it.
func servedHosts(facts []fact.Fact) map[string][]string {
	out := map[string][]string{}
	for _, f := range facts {
		if f.Kind != fact.KindRoute {
			continue
		}
		for _, a := range maps(f.Attrs["addresses"]) {
			h, _ := a["host"].(string)
			if hostname.Public(h) { // a wildcard (*.example.com) is not a host: left out
				out[h] = append(out[h], f.Key)
			}
		}
	}
	return out
}

func maps(v any) []map[string]any {
	xs, _ := v.([]any)
	var out []map[string]any
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
