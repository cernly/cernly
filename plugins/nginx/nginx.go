// Package nginx reads nginx configuration into route facts, the same shape
// the caddy plugin gives: per server and location, what handles it
// (proxy_pass and the other *_pass directives, return, root/alias) and
// where a proxy sends it, upstream blocks resolved to their servers. Which
// files are nginx configuration is in the shipped defaults (files.nginx). A
// file another one includes belongs to the file that includes it. ${VAR} in
// a template is kept as the variable's name; nothing is run and no
// environment is read. See https://cernly.dev/how/plugins/.
package nginx

import (
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/nginxconf"
	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "nginx" }
func (Plugin) Version() string { return "0.2.0" }

func (Plugin) Watches(p string) bool { return nginxconf.IsConfig(p) }

type reader struct {
	r      *repo.Repo
	res    *plugin.Result
	routes map[string]*fact.Fact
	root   string
	stack  []string // files being expanded, the root first
	incs   int      // includes expanded from root
	nodes  int      // tokens of the directives produced by the expansion of root
	over   bool     // the budget is used up
	seen   map[string]bool
}

// maxIncludes bounds the includes expanded from one root file, so a diamond
// of includes cannot grow exponentially.
const maxIncludes = 1000

// maxNodes bounds the tokens of the directives one root file expands to,
// included files counted every time they are included. Include events alone do not bound
// the output: 1000 includes of a file with 200k directives is 200M nodes.
const maxNodes = 1 << 20

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	var files []string
	for _, f := range r.Files {
		if nginxconf.IsConfig(f) {
			files = append(files, f)
		}
	}
	parsed := map[string][]nginxconf.Node{}
	included := map[string]bool{}
	for _, f := range files {
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		nodes, err := nginxconf.ParseFile(f, data)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Line: 1, Message: "nginx: " + err.Error()})
			continue
		}
		parsed[f] = nodes
		walk(nodes, func(n nginxconf.Node) {
			if n.Name() == "include" && len(n.Args) > 1 {
				for _, inc := range includes(r, f, n.Args[1].Text) {
					included[inc] = true
				}
			}
		})
	}
	routes := map[string]*fact.Fact{}
	for _, f := range files {
		if included[f] || parsed[f] == nil {
			continue
		}
		rd := &reader{r: r, res: &res, routes: routes, root: f, stack: []string{f}, seen: map[string]bool{}}
		nodes := rd.expand(f, parsed[f], parsed, 0)
		ups := upstreams(nodes)
		walk(nodes, func(n nginxconf.Node) {
			if n.Name() == "server" && n.Open {
				rd.server(n, ups)
			}
		})
	}
	keys := make([]string, 0, len(routes))
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		res.Facts = append(res.Facts, *routes[k])
	}
	return res
}

// walk visits every directive, blocks included.
func walk(nodes []nginxconf.Node, fn func(nginxconf.Node)) {
	for _, n := range nodes {
		fn(n)
		walk(n.Block, fn)
	}
}

// includes resolves an include against the tracked files: a relative path
// against the including file's folder, an absolute one under /etc/nginx/
// against it too (where a repo keeps its nginx folder), globs within a folder.
func includes(r *repo.Repo, from, pattern string) []string {
	p := pattern
	if strings.HasPrefix(p, "/etc/nginx/") {
		p = strings.TrimPrefix(p, "/etc/nginx/")
	} else if path.IsAbs(p) {
		return nil
	}
	full, ok := repo.Join(repo.Dir(from), p)
	if !ok {
		return nil
	}
	if !strings.ContainsAny(full, "*?[") {
		if r.Has(full) {
			return []string{full}
		}
		return nil
	}
	var out []string
	for _, f := range r.Files {
		if m, _ := path.Match(full, f); m {
			out = append(out, f)
		}
	}
	return out
}

// expand replaces include directives with the included files' directives.
// A file already being expanded is not included again: nginx rejects such a
// cycle, and so does this, with an error at the include.
func (rd *reader) expand(file string, nodes []nginxconf.Node, parsed map[string][]nginxconf.Node, depth int) []nginxconf.Node {
	var out []nginxconf.Node
	for _, n := range nodes {
		if rd.over || rd.nodes+len(n.Args) > maxNodes {
			if !rd.over {
				rd.over = true
				msg := "nginx: more than " + strconv.Itoa(maxNodes) + " tokens of directives from " + rd.root + "; rest skipped"
				if len(n.Args) > 0 {
					rd.fail(n, msg)
				} else {
					rd.res.Errors = append(rd.res.Errors, fact.LocationError{Path: rd.root, Message: msg})
				}
			}
			break
		}
		if n.Name() == "include" && len(n.Args) > 1 && depth < 8 {
			for _, inc := range includes(rd.r, file, n.Args[1].Text) {
				switch {
				case slices.Contains(rd.stack, inc):
					rd.fail(n, "nginx: include cycle: "+inc+" is already being included")
				case rd.incs >= maxIncludes:
					if rd.incs == maxIncludes {
						rd.incs++ // recorded once
						rd.fail(n, "nginx: more than "+strconv.Itoa(maxIncludes)+" includes from "+rd.root+"; rest skipped")
					}
				default:
					rd.incs++
					rd.stack = append(rd.stack, inc)
					out = append(out, rd.expand(inc, parsed[inc], parsed, depth+1)...)
					rd.stack = rd.stack[:len(rd.stack)-1]
				}
			}
			continue
		}
		rd.nodes += len(n.Args)
		if n.Open {
			n.Block = rd.expand(file, n.Block, parsed, depth)
		}
		out = append(out, n)
	}
	return out
}

// fail records an error at a directive, once per place and message.
func (rd *reader) fail(n nginxconf.Node, msg string) {
	e := fact.LocationError{Path: n.Args[0].File, Line: n.Line(), Message: msg}
	k := e.Path + ":" + strconv.Itoa(e.Line) + ":" + msg
	if rd.seen[k] {
		return
	}
	rd.seen[k] = true
	rd.res.Errors = append(rd.res.Errors, e)
}

// upstreams reads upstream blocks: name to its servers' addresses.
func upstreams(nodes []nginxconf.Node) map[string][]string {
	out := map[string][]string{}
	walk(nodes, func(n nginxconf.Node) {
		if n.Name() == "upstream" && n.Open && len(n.Args) > 1 {
			for _, s := range n.Block {
				if s.Name() == "server" && len(s.Args) > 1 {
					out[n.Args[1].Text] = append(out[n.Args[1].Text], s.Args[1].Text)
				}
			}
		}
	})
	return out
}

type address struct{ raw, scheme, host, port, hostEnv string }

func (a address) attrs() map[string]any {
	m := map[string]any{"address": a.raw}
	for k, v := range map[string]string{"scheme": a.scheme, "host": a.host, "port": a.port, "host_env": a.hostEnv} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// envName reads a ${VAR} placeholder (an envsubst template).
func envName(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		return s[2 : len(s)-1]
	}
	return ""
}

type server struct {
	addrs []address
	at    []fact.Evidence // the server line and its server_name line
	root  bool            // a root directive at server level
}

func (rd *reader) server(n nginxconf.Node, ups map[string][]string) {
	s := server{at: []fact.Evidence{{Path: n.Args[0].File, Line: n.Line()}}}
	port, scheme := "", "http"
	var names []string
	for _, d := range n.Block {
		switch d.Name() {
		case "listen":
			if len(d.Args) > 1 {
				a := d.Args[1].Text
				if i := strings.LastIndex(a, ":"); i >= 0 && !strings.HasSuffix(a, "]") {
					a = a[i+1:]
				}
				if _, err := strconv.Atoi(a); err == nil && port == "" {
					port = a
				}
				for _, v := range d.Values()[1:] {
					if v == "ssl" {
						scheme = "https"
					}
				}
			}
		case "server_name":
			names = append(names, d.Values()...)
			s.at = append(s.at, fact.Evidence{Path: d.Args[0].File, Line: d.Line()})
		case "root", "alias":
			s.root = true
		}
	}
	if port == "" {
		port = "80"
	}
	for _, nm := range names {
		if nm == "_" || nm == "" || nm == `""` {
			continue
		}
		a := address{raw: nm, scheme: scheme, port: port}
		if v := envName(nm); v != "" {
			a.hostEnv = v
		} else {
			a.host = strings.ToLower(strings.TrimPrefix(nm, "."))
		}
		s.addrs = append(s.addrs, a)
	}
	if len(s.addrs) == 0 {
		s.addrs = []address{{raw: ":" + port, scheme: scheme, port: port}}
	}
	for _, d := range n.Block {
		if d.Name() == "location" && d.Open {
			rd.location(s, d, ups)
		}
	}
	// Directives at server level handle what no location matches.
	rd.handle(s, "*", n.Block, n.Args[0], ups, s.root)
}

// location reads one location block (nested ones too) and its handler.
func (rd *reader) location(s server, n nginxconf.Node, ups map[string][]string) {
	match := strings.Join(n.Values(), " ")
	rd.handle(s, match, n.Block, n.Args[0], ups, s.root)
	for _, d := range n.Block {
		if d.Name() == "location" && d.Open {
			rd.location(s, d, ups)
		}
	}
}

// handle emits the route of one location (or the server itself, match "*"):
// the first handler directive in its block.
func (rd *reader) handle(s server, match string, block []nginxconf.Node, at nginxconf.Token, ups map[string][]string, inheritRoot bool) {
	attrs := map[string]any{}
	var line nginxconf.Node
	for _, d := range block {
		switch d.Name() {
		case "proxy_pass", "fastcgi_pass", "uwsgi_pass", "grpc_pass", "scgi_pass":
			if len(d.Args) < 2 {
				continue
			}
			action := "proxy"
			if d.Name() == "fastcgi_pass" {
				action = "fastcgi"
			}
			attrs["action"] = action
			attrs["upstreams"] = upstreamAttrs(d.Args[1].Text, ups)
		case "return":
			v := d.Values()
			if len(v) == 0 {
				continue
			}
			code, err := strconv.Atoi(v[0])
			switch {
			case err != nil: // return URL: a 302
				attrs["action"], attrs["status"], attrs["target"] = "redirect", 302, redact.Userinfo(v[0])
			case code >= 301 && code <= 308 && code != 304 && len(v) > 1:
				attrs["action"], attrs["status"], attrs["target"] = "redirect", code, redact.Userinfo(v[1])
			default:
				attrs["action"], attrs["status"] = "respond", code
			}
		case "root", "alias":
			attrs["action"] = "file_server"
		default:
			continue
		}
		line = d
		break
	}
	if attrs["action"] == nil {
		if match == "*" || !inheritRoot {
			return
		}
		attrs["action"] = "file_server" // the server's root serves it
	}
	addrs := make([]any, 0, len(s.addrs))
	for _, a := range s.addrs {
		addrs = append(addrs, a.attrs())
	}
	attrs["file"] = rd.root
	attrs["addresses"] = addrs
	if match != "*" {
		attrs["paths"] = []any{match}
	}
	ev := append([]fact.Evidence(nil), s.at...)
	if match != "*" {
		ev = append(ev, fact.Evidence{Path: at.File, Line: at.Line})
	}
	if len(line.Args) > 0 {
		ev = append(ev, fact.Evidence{Path: line.Args[0].File, Line: line.Line()})
	}
	// The listen scheme and port are part of the key: a port 80 server that
	// redirects and a 443 one that proxies, same server_name, are two routes.
	a := s.addrs[0]
	listen := a.scheme + "://" + a.raw
	if !strings.HasPrefix(a.raw, ":") {
		listen += ":" + a.port
	}
	key := "route:nginx:" + rd.root + ":" + listen + ":" + match
	if f, ok := rd.routes[key]; ok {
		f.Evidence = append(f.Evidence, ev...)
		return
	}
	rd.routes[key] = &fact.Fact{Kind: fact.KindRoute, Key: key, Plugin: "nginx", Attrs: attrs, Evidence: ev}
}

// upstreamAttrs reads a proxy_pass target: a URL to a host and port, an
// upstream block's name to its servers, a ${VAR} to its name.
func upstreamAttrs(target string, ups map[string][]string) []any {
	target = redact.Userinfo(target)
	raw := target
	scheme := ""
	if i := strings.Index(target, "://"); i >= 0 {
		scheme, target = target[:i], target[i+3:]
	}
	if i := strings.IndexAny(target, "/?"); i >= 0 {
		target = target[:i]
	}
	if servers, ok := ups[target]; ok {
		var out []any
		for _, s := range servers {
			out = append(out, hostPort(s, scheme).attrs())
		}
		return out
	}
	a := hostPort(target, scheme)
	a.raw = raw
	return []any{a.attrs()}
}

func hostPort(s, scheme string) address {
	a := address{raw: s, scheme: scheme}
	if v := envName(s); v != "" {
		a.hostEnv = v
		return a
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.HasSuffix(s, "]") {
		a.host, a.port = s[:i], s[i+1:]
	} else {
		a.host = s
	}
	if strings.HasPrefix(a.host, "unix:") {
		a.host = ""
	}
	return a
}
