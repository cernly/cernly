// Package traefik reads Traefik's routers into route facts, the shape the
// caddy and nginx plugins give, from its two common sources:
//
//   - Docker labels on compose services (traefik.http.routers.<r>.rule and
//     friends): the labelled service is the upstream; the route names the
//     proxy by its image (traefik), so the routes rule can find it in each
//     stack that holds the compose file.
//   - Dynamic file configuration (http.routers and http.services, YAML or
//     TOML): placed like any config file, by the service that mounts it.
//
// Only router rules, router services and service ports and URLs are read;
// other labels (middlewares can hold basic-auth hashes) are not. See
// https://cernly.dev/how/plugins/.
package traefik

import (
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/internal/yamlnode"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "traefik" }
func (Plugin) Version() string { return "0.1.1" }

// The image a label route's proxy runs; the routes rule matches it by name.
const proxyImage = "traefik"

func isDynamic(p string) bool {
	for _, pat := range defaults.Get().Files.Traefik {
		if config.Matches(pat, p) {
			return true
		}
	}
	return false
}

func (Plugin) Watches(p string) bool { return defaults.IsCompose(p) || isDynamic(p) }

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	for _, f := range r.Files {
		var facts []fact.Fact
		var errs []fact.LocationError
		switch {
		case defaults.IsCompose(f):
			facts, errs = fromLabels(r, f)
		case isDynamic(f):
			facts, errs = fromFile(r, f)
		default:
			continue
		}
		res.Facts = append(res.Facts, facts...)
		res.Errors = append(res.Errors, errs...)
	}
	return res
}

// router is one router as either source states it.
type router struct {
	name, rule, service string
	ruleLine            int
	upstreams           []upstream
}

type upstream struct {
	address, scheme, host, port string
}

func (u upstream) attrs() map[string]any {
	m := map[string]any{"address": u.address}
	for k, v := range map[string]string{"scheme": u.scheme, "host": u.host, "port": u.port} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// route turns a router into a route fact.
func route(file string, rt router, extra map[string]any, ev []fact.Evidence) (fact.Fact, *fact.LocationError) {
	ms, err := parseRule(rt.rule)
	if err != nil {
		return fact.Fact{}, &fact.LocationError{Path: file, Line: rt.ruleLine, Message: "traefik: " + err.Error()}
	}
	attrs := map[string]any{"file": file, "router": rt.name, "rule": rt.rule, "action": "proxy"}
	addrs := []any{}
	var paths, methods []any
	seen := map[string]bool{}
	for _, m := range ms {
		switch {
		case m.Name == "Host" && !m.Negated:
			for _, h := range m.Args {
				if !seen[h] {
					seen[h] = true
					addrs = append(addrs, map[string]any{"address": h, "host": strings.ToLower(h)})
				}
			}
		case m.Name == "PathPrefix" && !m.Negated:
			for _, p := range m.Args {
				paths = append(paths, p+"*")
			}
		case m.Name == "Path" && !m.Negated:
			for _, p := range m.Args {
				paths = append(paths, p)
			}
		case m.Name == "Method" && !m.Negated:
			for _, x := range m.Args {
				methods = append(methods, strings.ToUpper(x))
			}
		}
	}
	attrs["addresses"] = addrs
	if len(paths) > 0 {
		attrs["paths"] = paths
	}
	if len(methods) > 0 {
		attrs["methods"] = methods
	}
	ups := make([]any, 0, len(rt.upstreams))
	for _, u := range rt.upstreams {
		ups = append(ups, u.attrs())
	}
	attrs["upstreams"] = ups
	for k, v := range extra {
		attrs[k] = v
	}
	return fact.Fact{Kind: fact.KindRoute, Key: "route:traefik:" + file + ":" + rt.name, Plugin: "traefik", Attrs: attrs, Evidence: ev}, nil
}

// fromLabels reads the traefik labels of every service of a compose file.
func fromLabels(r *repo.Repo, file string) ([]fact.Fact, []fact.LocationError) {
	data, err := r.Read(file)
	if err != nil {
		return nil, []fact.LocationError{{Path: file, Message: err.Error()}}
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil, nil // the compose plugin reports invalid YAML
	}
	services := yamlnode.Get(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []fact.Fact
	var errs []fact.LocationError
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc, node := services.Content[i].Value, services.Content[i+1]
		labels := readLabels(yamlnode.Get(node, "labels"))
		if len(labels) == 0 || labels["traefik.enable"].value == "false" {
			continue
		}
		routers := map[string]*router{}
		ports := map[string]string{}
		for k, l := range labels {
			parts := strings.Split(k, ".")
			if len(parts) < 5 || parts[0] != "traefik" || parts[1] != "http" {
				continue
			}
			switch {
			case parts[2] == "routers" && parts[4] == "rule" && len(parts) == 5:
				rt := routerOf(routers, parts[3])
				rt.rule, rt.ruleLine = l.value, l.line
			case parts[2] == "routers" && parts[4] == "service" && len(parts) == 5:
				routerOf(routers, parts[3]).service = l.value
			case parts[2] == "services" && strings.EqualFold(strings.Join(parts[4:], "."), "loadbalancer.server.port"):
				ports[parts[3]] = l.value
			}
		}
		names := make([]string, 0, len(routers))
		for n := range routers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			rt := routers[n]
			if rt.rule == "" {
				continue
			}
			// The router's service: the one it names, else the container's
			// only service, else the container itself on its exposed port.
			port := ports[rt.service]
			if rt.service == "" && len(ports) == 1 {
				for _, p := range ports {
					port = p
				}
			}
			addr := svc
			if port != "" {
				addr += ":" + port
			}
			rt.upstreams = []upstream{{address: addr, host: svc, port: port}}
			ev := []fact.Evidence{{Path: file, Line: rt.ruleLine, Unit: svc}}
			f, lerr := route(file, *rt, map[string]any{"labels_on": svc, "proxy_image": proxyImage}, ev)
			if lerr != nil {
				errs = append(errs, *lerr)
				continue
			}
			out = append(out, f)
		}
	}
	return out, errs
}

func routerOf(m map[string]*router, name string) *router {
	if m[name] == nil {
		m[name] = &router{name: name}
	}
	return m[name]
}

type label struct {
	value string
	line  int
}

// readLabels reads compose labels, list form (- key=value) or map form.
func readLabels(n *yaml.Node) map[string]label {
	out := map[string]label{}
	if n == nil {
		return out
	}
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if k, v, ok := strings.Cut(c.Value, "="); ok {
				out[strings.TrimSpace(k)] = label{strings.TrimSpace(v), c.Line}
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[n.Content[i].Value] = label{n.Content[i+1].Value, n.Content[i].Line}
		}
	}
	return out
}

// fromFile reads the routers of a dynamic configuration file.
func fromFile(r *repo.Repo, file string) ([]fact.Fact, []fact.LocationError) {
	data, err := r.Read(file)
	if err != nil {
		return nil, []fact.LocationError{{Path: file, Message: err.Error()}}
	}
	var routers []router
	var servers map[string][]string
	if strings.HasSuffix(file, ".toml") {
		routers, servers, err = tomlRouters(data)
	} else {
		routers, servers, err = yamlRouters(data)
	}
	if err != nil {
		return nil, []fact.LocationError{{Path: file, Line: 1, Message: "traefik: " + err.Error()}}
	}
	var out []fact.Fact
	var errs []fact.LocationError
	for _, rt := range routers {
		for _, u := range servers[strings.TrimSuffix(rt.service, "@file")] {
			rt.upstreams = append(rt.upstreams, parseURL(u))
		}
		f, lerr := route(file, rt, nil, []fact.Evidence{{Path: file, Line: rt.ruleLine}})
		if lerr != nil {
			errs = append(errs, *lerr)
			continue
		}
		out = append(out, f)
	}
	return out, errs
}

func yamlRouters(data []byte) ([]router, map[string][]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, nil
	}
	http := yamlnode.Get(doc.Content[0], "http")
	var out []router
	if rs := yamlnode.Get(http, "routers"); rs != nil && rs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(rs.Content); i += 2 {
			rt := router{name: rs.Content[i].Value, ruleLine: rs.Content[i].Line}
			if v := yamlnode.Get(rs.Content[i+1], "rule"); v != nil {
				rt.rule, rt.ruleLine = v.Value, v.Line
			}
			if v := yamlnode.Get(rs.Content[i+1], "service"); v != nil {
				rt.service = v.Value
			}
			if rt.rule != "" {
				out = append(out, rt)
			}
		}
	}
	servers := map[string][]string{}
	if ss := yamlnode.Get(http, "services"); ss != nil && ss.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(ss.Content); i += 2 {
			lb := yamlnode.Get(ss.Content[i+1], "loadBalancer")
			if list := yamlnode.Get(lb, "servers"); list != nil {
				for _, s := range list.Content {
					if u := yamlnode.Get(s, "url"); u != nil {
						servers[ss.Content[i].Value] = append(servers[ss.Content[i].Value], u.Value)
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, servers, nil
}

func tomlRouters(data []byte) ([]router, map[string][]string, error) {
	var doc struct {
		HTTP struct {
			Routers map[string]struct {
				Rule    string `toml:"rule"`
				Service string `toml:"service"`
			} `toml:"routers"`
			Services map[string]struct {
				LoadBalancer struct {
					Servers []struct {
						URL string `toml:"url"`
					} `toml:"servers"`
				} `toml:"loadBalancer"`
			} `toml:"services"`
		} `toml:"http"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(data), "\n")
	var out []router
	for name, rt := range doc.HTTP.Routers {
		if rt.Rule == "" {
			continue
		}
		out = append(out, router{name: name, rule: rt.Rule, service: rt.Service, ruleLine: ruleLine(lines, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	servers := map[string][]string{}
	for name, s := range doc.HTTP.Services {
		for _, x := range s.LoadBalancer.Servers {
			servers[name] = append(servers[name], x.URL)
		}
	}
	return out, servers, nil
}

// ruleLine is the line of `rule =` in the TOML table [http.routers.<name>].
func ruleLine(lines []string, name string) int {
	in := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = strings.Trim(t, "[] ") == "http.routers."+name
			continue
		}
		if k, _, ok := strings.Cut(t, "="); in && ok && strings.TrimSpace(k) == "rule" {
			return i + 1
		}
	}
	return 1
}

func parseURL(u string) upstream {
	u = redact.Userinfo(u)
	out := upstream{address: u}
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		out.scheme, rest = rest[:i], rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.HasSuffix(rest, "]") {
		out.host, out.port = rest[:i], rest[i+1:]
	} else {
		out.host = rest
	}
	return out
}
