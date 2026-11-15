// Package caddy reads Caddyfiles: which addresses a repo serves and where
// each path goes. One route fact per site, matcher and handler:
// reverse_proxy (with its upstreams), respond, redir, file_server,
// php_fastcgi. Snippets and imports of tracked files are expanded. Global
// options are not read; they hold settings such as the ACME email address,
// which is never stored. Nothing is run and no environment is read: a
// {$VAR} address is kept as the name of the variable. See
// https://cernly.dev/how/plugins/.
package caddy

import (
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/caddyfile"
	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "caddy" }
func (Plugin) Version() string { return "0.2.0" }

func (Plugin) Watches(p string) bool { return defaults.IsCaddyfile(p) }

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	routes := map[string]*fact.Fact{}
	files := r.Match(defaults.IsCaddyfile)
	// Caddy loads one root file; a file another one imports is part of that
	// root, not a config of its own.
	imported := map[string]bool{}
	for _, f := range files {
		rd := &reader{r: r, res: &plugin.Result{}, root: f, snippets: map[string][]caddyfile.Node{}, stack: []string{"file " + f}}
		if nodes, ok := rd.parseFile(f); ok {
			rd.topLevel(f, nodes, 0)
			rd.imports(f, nodes, imported, 0)
		}
	}
	for _, f := range files {
		if imported[f] {
			continue
		}
		rd := &reader{r: r, res: &res, routes: routes, root: f, stack: []string{"file " + f}}
		rd.readFile(f)
	}
	keys := make([]string, 0, len(routes))
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := routes[k]
		f.Evidence = uniqEvidence(f.Evidence)
		res.Facts = append(res.Facts, *f)
	}
	return res
}

type reader struct {
	r        *repo.Repo
	res      *plugin.Result
	routes   map[string]*fact.Fact
	root     string // the Caddyfile the routes belong to
	snippets map[string][]caddyfile.Node
	stack    []string // snippets and files being imported, the root first
	ordered  bool     // inside a route block: directives apply in the written order
	nimports int      // imports expanded, per site and for the top level
	nodes    int      // tokens of the directives produced or visited while expanding root
	over     bool     // the budget is used up
	parsed   map[string]parsedFile
	seen     map[string]bool
}

// maxImports bounds the imports expanded for one site (or the top level),
// so a diamond of imports cannot grow exponentially.
const maxImports = 1000

// maxNodes bounds the tokens one root Caddyfile expands to: each directive
// costs its arguments and the lines of its block, imported files counted
// every time they are imported, once for the top level and once for its
// sites, so at most twice this per root. Import events alone do not bound
// the work: 1000 imports of a file with 200k lines is 200M nodes.
const maxNodes = 1 << 20

type parsedFile struct {
	nodes []caddyfile.Node
	ok    bool
}

// spend charges a directive against the budget of the root and reports
// whether the budget is used up, with one error at the first overrun.
func (rd *reader) spend(at caddyfile.Node) bool {
	cost := 1 + len(at.Args) + len(at.Block)
	if rd.over || rd.nodes+cost > maxNodes {
		if !rd.over {
			rd.over = true
			rd.fail(at, "caddyfile: more than "+strconv.Itoa(maxNodes)+" tokens of directives from "+rd.root+"; rest skipped")
		}
		return true
	}
	rd.nodes += cost
	return false
}

// enter puts a snippet or file on the import stack. Caddy rejects an import
// cycle; here it is skipped with an error at the import.
func (rd *reader) enter(n caddyfile.Node, key string) bool {
	switch {
	case slices.Contains(rd.stack, key):
		rd.fail(n, "caddyfile: import cycle: "+key+" is already being imported")
		return false
	case rd.nimports >= maxImports:
		if rd.nimports == maxImports {
			rd.nimports++ // recorded once
			rd.fail(n, "caddyfile: more than "+strconv.Itoa(maxImports)+" imports from "+rd.root+"; rest skipped")
		}
		return false
	}
	rd.nimports++
	rd.stack = append(rd.stack, key)
	return true
}

func (rd *reader) leave() { rd.stack = rd.stack[:len(rd.stack)-1] }

// fail records an error at a directive, once per place and message.
func (rd *reader) fail(n caddyfile.Node, msg string) {
	at := n.At()
	k := at.Path + ":" + strconv.Itoa(at.Line) + ":" + msg
	if rd.seen[k] {
		return
	}
	if rd.seen == nil {
		rd.seen = map[string]bool{}
	}
	rd.seen[k] = true
	rd.res.Errors = append(rd.res.Errors, fact.LocationError{Path: at.Path, Line: at.Line, Message: msg})
}

// parseFile lexes and parses one file, reporting errors against it.
func (rd *reader) parseFile(p string) ([]caddyfile.Node, bool) {
	if c, ok := rd.parsed[p]; ok {
		return c.nodes, c.ok
	}
	nodes, ok := rd.parseFileUncached(p)
	if rd.parsed == nil {
		rd.parsed = map[string]parsedFile{}
	}
	rd.parsed[p] = parsedFile{nodes, ok}
	return nodes, ok
}

func (rd *reader) parseFileUncached(p string) ([]caddyfile.Node, bool) {
	data, err := rd.r.Read(p)
	if err != nil {
		rd.res.Errors = append(rd.res.Errors, fact.LocationError{Path: p, Message: err.Error()})
		return nil, false
	}
	toks, err := caddyfile.Lex(data)
	for i := range toks {
		toks[i].File = p
	}
	if err == nil {
		var nodes []caddyfile.Node
		if nodes, err = caddyfile.Parse(toks); err == nil {
			return nodes, true
		}
	}
	rd.res.Errors = append(rd.res.Errors, fact.LocationError{Path: p, Line: 1, Message: "caddyfile: " + err.Error()})
	return nil, false
}

func (rd *reader) readFile(p string) {
	nodes, ok := rd.parseFile(p)
	if !ok {
		return
	}
	rd.snippets = map[string][]caddyfile.Node{}
	nodes = rd.topLevel(p, nodes, 0)
	rd.nodes, rd.over = 0, false // the sites get a budget of their own
	// A Caddyfile with one site may leave out the braces: the first line is
	// the addresses, the rest its directives.
	if len(nodes) > 0 && !nodes[0].Open && nodes[0].Name() != "" {
		rd.site(p, nodes[0].Args, nodes[1:])
		return
	}
	for _, n := range nodes {
		if !n.Open || n.Name() == "" {
			continue // global options, or a stray line
		}
		rd.site(p, n.Args, n.Block)
	}
}

// topLevel collects snippets and replaces top-level imports of files with
// their contents.
func (rd *reader) topLevel(p string, nodes []caddyfile.Node, depth int) []caddyfile.Node {
	var out []caddyfile.Node
	for _, n := range nodes {
		if rd.spend(n) {
			break
		}
		name := n.Name()
		switch {
		case n.Open && len(n.Args) == 1 && strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")"):
			rd.snippets[strings.Trim(name, "()")] = n.Block
		case name == "import" && len(n.Args) >= 2 && depth < 4:
			for _, f := range rd.importFiles(p, n.Args[1].Text) {
				if !rd.enter(n, "file "+f) {
					continue
				}
				if sub, ok := rd.parseFile(f); ok {
					out = append(out, rd.topLevel(f, sub, depth+1)...)
				}
				rd.leave()
			}
		default:
			out = append(out, n)
		}
	}
	return out
}

// imports marks the files a file imports, at any depth of its blocks.
func (rd *reader) imports(p string, nodes []caddyfile.Node, out map[string]bool, depth int) {
	for _, n := range nodes {
		if n.Name() == "import" && len(n.Args) >= 2 {
			for _, f := range rd.importFiles(p, n.Args[1].Text) {
				out[f] = true
			}
		}
		if depth < 8 {
			rd.imports(p, n.Block, out, depth+1)
		}
	}
}

// importFiles resolves an import of files, relative to the importing file's
// folder, against the tracked files. Globs match within one folder.
func (rd *reader) importFiles(from, pattern string) []string {
	if _, ok := rd.snippets[pattern]; ok || strings.Contains(pattern, "{") {
		return nil
	}
	p, ok := repo.Join(repo.Dir(from), pattern)
	if !ok {
		return nil
	}
	if !strings.ContainsAny(p, "*?[") {
		if rd.r.Has(p) {
			return []string{p}
		}
		return nil
	}
	var out []string
	for _, f := range rd.r.Files {
		if m, _ := path.Match(p, f); m {
			out = append(out, f)
		}
	}
	return out
}

// address is a site address as written, split where it is literal.
type address struct {
	raw, scheme, host, port, hostEnv string
}

func parseAddress(raw string) address {
	raw = redact.Userinfo(raw)
	a := address{raw: raw}
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		a.scheme, s = s[:i], s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "{$") {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return a
		}
		name := s[2:end]
		if i := strings.IndexByte(name, ':'); i >= 0 {
			name = name[:i] // {$VAR:default}
		}
		a.hostEnv, s = name, s[end+1:]
		if strings.HasPrefix(s, ":") {
			a.port = s[1:]
		}
		return a
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		a.host, a.port = s[:i], s[i+1:]
	} else {
		a.host = s
	}
	a.host = strings.ToLower(a.host)
	return a
}

func (a address) attrs() map[string]any {
	m := map[string]any{"address": a.raw}
	for k, v := range map[string]string{"scheme": a.scheme, "host": a.host, "port": a.port, "host_env": a.hostEnv} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// matcher is what a handler applies to: paths and methods, or a named
// matcher that also tests something else.
type matcher struct {
	paths, methods []string
	other          bool   // a matcher type the reader does not split out
	named          string // @name, when the handler used one
	rw             string // "", "strip" or "unknown": how the path changes before the upstream
	rwPrefix       string
}

// matchType classifies the path matcher: prefix (/a/*), exact (/a), host (no
// path matcher), or other (a named matcher that tests more, globs, mixed types).
func (m matcher) matchType() string {
	if m.other {
		return "other"
	}
	if len(m.paths) == 0 {
		return "host"
	}
	typ := ""
	for _, p := range m.paths {
		t := "exact"
		switch {
		case strings.Count(p, "*") == 0:
		case strings.Count(p, "*") == 1 && strings.HasSuffix(p, "*"):
			t = "prefix"
		default:
			return "other"
		}
		if typ != "" && typ != t {
			return "other"
		}
		typ = t
	}
	return typ
}

func (m matcher) rewrite() map[string]any {
	switch m.rw {
	case "strip":
		return map[string]any{"kind": "strip", "prefix": m.rwPrefix}
	case "unknown":
		return map[string]any{"kind": "unknown"}
	}
	return map[string]any{"kind": "none"}
}

// withRewrite adds one path rewrite; a second one is no longer readable.
func (m matcher) withRewrite(kind, prefix string) matcher {
	if m.rw != "" || kind == "unknown" {
		m.rw, m.rwPrefix = "unknown", ""
		return m
	}
	m.rw, m.rwPrefix = kind, prefix
	return m
}

func (m matcher) key() string {
	var parts []string
	if len(m.methods) > 0 {
		parts = append(parts, strings.Join(m.methods, ","))
	}
	if len(m.paths) > 0 {
		parts = append(parts, strings.Join(m.paths, ","))
	}
	if m.other {
		parts = append(parts, m.named)
	}
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, " ")
}

// and narrows an outer matcher (handle /api/*) with an inner one.
func (m matcher) and(n matcher) matcher {
	out := m
	if len(n.paths) > 0 {
		out.paths = n.paths
	}
	if len(n.methods) > 0 {
		out.methods = n.methods
	}
	if n.other {
		out.other, out.named = true, n.named
	}
	if n.rw != "" {
		out = out.withRewrite(n.rw, n.rwPrefix)
	}
	return out
}

type site struct {
	addrs []address
	at    fact.Evidence // the line with the addresses
	named map[string]matcher
}

// site reads one site block.
func (rd *reader) site(p string, addrTokens []caddyfile.Token, body []caddyfile.Node) {
	s := &site{at: fact.Evidence{Path: addrTokens[0].File, Line: addrTokens[0].Line}, named: map[string]matcher{}}
	for _, t := range addrTokens {
		for _, a := range strings.Split(t.Text, ",") {
			if a = strings.TrimSpace(a); a != "" {
				s.addrs = append(s.addrs, parseAddress(a))
			}
		}
	}
	if len(s.addrs) == 0 {
		return
	}
	rd.nimports = 0
	rd.ordered = false
	rd.directives(p, s, body, matcher{}, nil, 0)
}

// directives walks a block of directives under a matcher.
func (rd *reader) directives(p string, s *site, nodes []caddyfile.Node, under matcher, via []fact.Evidence, depth int) {
	if depth > 8 {
		return
	}
	// Named matchers are defined anywhere in the site and used by name.
	for _, n := range nodes {
		if name := n.Name(); strings.HasPrefix(name, "@") {
			s.named[name] = namedMatcher(name, n)
		}
	}
	// A rewrite in this block changes the path for the handlers in it. A
	// nested handle matches after it (the default directive order), so it gets
	// the incoming matcher, not this one; a route block keeps the written order.
	ordered := rd.ordered
	apply := func(mt matcher, ns []caddyfile.Node) matcher {
		for _, n := range ns {
			switch n.Name() {
			case "rewrite":
				mt = mt.withRewrite("unknown", "")
			case "uri":
				if len(n.Args) == 3 && n.Args[1].Text == "strip_prefix" {
					mt = mt.withRewrite("strip", "/"+strings.Trim(n.Args[2].Text, "/"))
				} else if len(n.Args) > 1 {
					mt = mt.withRewrite("unknown", "")
				}
			case "import":
				mt = mt.withRewrite("unknown", "") // contents cannot be seen
			}
		}
		return mt
	}
	local := apply(under, rd.rewriteNodes(p, nodes, 0))
	cur := under // route block: only what was written before a handler counts
	for _, n := range nodes {
		if rd.spend(n) {
			return
		}
		name := n.Name()
		args := n.Args[1:]
		m, rest := rd.matcherArg(s, args)
		h := local
		if ordered {
			cur = apply(cur, rd.rewriteNodes(p, []caddyfile.Node{n}, 0))
			h = cur
		}
		switch name {
		case "import":
			if len(args) == 0 {
				continue
			}
			ev := append(append([]fact.Evidence(nil), via...), n.At())
			if snip, ok := rd.snippets[args[0].Text]; ok {
				if rd.enter(n, "snippet "+args[0].Text) {
					rd.directives(p, s, snip, under, ev, depth+1)
					rd.leave()
				}
				continue
			}
			for _, f := range rd.importFiles(p, args[0].Text) {
				if !rd.enter(n, "file "+f) {
					continue
				}
				if sub, ok := rd.parseFile(f); ok {
					rd.directives(f, s, sub, under, ev, depth+1)
				}
				rd.leave()
			}
		case "handle":
			rd.nested(false, p, s, n.Block, under.and(m), via, depth+1)
		case "route":
			in := under
			if routeRewritesBeforeMatcher(n.Block) {
				in = in.withRewrite("unknown", "")
			}
			rd.nested(true, p, s, n.Block, in.and(m), via, depth+1)
		case "handle_path":
			if len(m.paths) == 1 && !m.other {
				m = m.withRewrite("strip", "/"+strings.Trim(strings.TrimSuffix(m.paths[0], "*"), "/"))
			} else {
				m = m.withRewrite("unknown", "")
			}
			rd.nested(false, p, s, n.Block, under.and(m), via, depth+1)
		case "reverse_proxy":
			ups := upstreams(rest, n.Block)
			rd.emit(s, h.and(m), "proxy", n, via, func(a map[string]any) {
				list := make([]any, 0, len(ups))
				for _, u := range ups {
					list = append(list, u.attrs())
				}
				a["upstreams"] = list
			})
		case "php_fastcgi":
			ups := upstreams(rest, nil)
			rd.emit(s, h.and(m), "fastcgi", n, via, func(a map[string]any) {
				list := make([]any, 0, len(ups))
				for _, u := range ups {
					list = append(list, u.attrs())
				}
				a["upstreams"] = list
			})
		case "respond":
			rd.emit(s, h.and(m), "respond", n, via, func(a map[string]any) {
				if st := status(rest); st != 0 {
					a["status"] = st
				}
			})
		case "redir":
			rd.emit(s, h.and(m), "redirect", n, via, func(a map[string]any) {
				if len(rest) > 0 {
					a["target"] = redact.Userinfo(rest[0].Text)
				}
				if st := status(rest[min(1, len(rest)):]); st != 0 {
					a["status"] = st
				}
			})
		case "file_server":
			rd.emit(s, h.and(m), "file_server", n, via, nil)
		}
	}
}

// matcherArg reads a handler's matcher token: *, a path, or @name.
func (rd *reader) matcherArg(s *site, args []caddyfile.Token) (matcher, []caddyfile.Token) {
	if len(args) == 0 || args[0].Quoted {
		return matcher{}, args
	}
	t := args[0].Text
	switch {
	case t == "*":
		return matcher{}, args[1:]
	case strings.HasPrefix(t, "/"):
		return matcher{paths: []string{t}}, args[1:]
	case strings.HasPrefix(t, "@"):
		if m, ok := s.named[t]; ok {
			return m, args[1:]
		}
		return matcher{other: true, named: t}, args[1:]
	}
	return matcher{}, args
}

// namedMatcher reads @name path /a /b, @name { path …; method … } and the
// other matcher types, which make it "other".
func namedMatcher(name string, n caddyfile.Node) matcher {
	m := matcher{named: name}
	lines := n.Block
	if len(n.Args) > 1 {
		lines = append([]caddyfile.Node{{Args: n.Args[1:]}}, lines...)
	}
	for _, l := range lines {
		if len(l.Args) == 0 {
			continue
		}
		vals := make([]string, 0, len(l.Args)-1)
		for _, a := range l.Args[1:] {
			vals = append(vals, a.Text)
		}
		switch t := l.Args[0].Text; {
		case t == "path":
			m.paths = append(m.paths, vals...)
		case t == "method":
			for _, v := range vals {
				m.methods = append(m.methods, strings.ToUpper(v))
			}
		case strings.HasPrefix(t, "/") && len(n.Args) > 1 && l.Args[0].Line == n.Line():
			m.paths = append(m.paths, t) // @name /path shorthand
			m.paths = append(m.paths, vals...)
		default:
			m.other = true
		}
	}
	sort.Strings(m.methods)
	return m
}

func upstreams(args []caddyfile.Token, block []caddyfile.Node) []address {
	var raw []string
	for _, a := range args {
		raw = append(raw, a.Text)
	}
	for _, n := range block {
		if n.Name() == "to" {
			for _, a := range n.Args[1:] {
				raw = append(raw, a.Text)
			}
		}
	}
	out := make([]address, 0, len(raw))
	for _, u := range raw {
		out = append(out, parseAddress(u))
	}
	return out
}

func status(args []caddyfile.Token) int {
	for _, a := range args {
		if a.Quoted {
			continue
		}
		if n, err := strconv.Atoi(a.Text); err == nil && n >= 100 && n <= 599 {
			return n
		}
		switch a.Text {
		case "permanent":
			return 301
		case "temporary":
			return 302
		}
	}
	return 0
}

// emit adds one route fact, or merges into the route with the same key.
func (rd *reader) emit(s *site, m matcher, action string, n caddyfile.Node, via []fact.Evidence, extra func(map[string]any)) {
	key := "route:caddy:" + rd.root + ":" + s.addrs[0].raw + ":" + m.key()
	ev := []fact.Evidence{s.at}
	ev = append(ev, via...)
	ev = append(ev, n.At())
	if f, ok := rd.routes[key]; ok {
		// Caddy serves the first matching terminal handler, so the first
		// route's attrs (upstreams too) stay; the later one is evidence only.
		f.Evidence = append(f.Evidence, ev...)
		return
	}
	addrs := make([]any, 0, len(s.addrs))
	for _, a := range s.addrs {
		addrs = append(addrs, a.attrs())
	}
	attrs := map[string]any{"file": rd.root, "addresses": addrs, "action": action,
		"match_type": m.matchType(), "rewrite": m.rewrite()}
	if len(m.paths) > 0 {
		attrs["paths"] = fact.Strings(m.paths)
	}
	if len(m.methods) > 0 {
		attrs["methods"] = fact.Strings(m.methods)
	}
	if m.other {
		attrs["matcher"] = m.named
	}
	if extra != nil {
		extra(attrs)
	}
	rd.routes[key] = &fact.Fact{Kind: fact.KindRoute, Key: key, Plugin: "caddy", Attrs: attrs, Evidence: ev}
}

func uniqEvidence(ev []fact.Evidence) []fact.Evidence {
	seen := map[fact.Evidence]bool{}
	out := ev[:0]
	for _, e := range ev {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// rewriteNodes returns the rewrite and uri directives of a block, and the
// import directives whose contents are not readable (as unknown markers named
// "import"); imports of snippets and tracked files are expanded.
func (rd *reader) rewriteNodes(p string, nodes []caddyfile.Node, depth int) []caddyfile.Node {
	return rd.rewriteSeen(p, nodes, depth, map[string]bool{})
}

// rewriteSeen reads each snippet and file once per call, so cycles and diamonds
// of imports stay linear.
func (rd *reader) rewriteSeen(p string, nodes []caddyfile.Node, depth int, seen map[string]bool) []caddyfile.Node {
	var out []caddyfile.Node
	for _, n := range nodes {
		switch n.Name() {
		case "rewrite", "uri":
			out = append(out, n)
		case "import":
			if len(n.Args) < 2 || depth > 4 {
				continue
			}
			arg := n.Args[1].Text
			if snip, ok := rd.snippets[arg]; ok {
				if !seen["s "+arg] {
					seen["s "+arg] = true
					out = append(out, rd.rewriteSeen(p, snip, depth+1, seen)...)
				}
				continue
			}
			if strings.Contains(arg, "{") {
				out = append(out, caddyfile.Node{Args: []caddyfile.Token{{Text: "import"}}})
				continue
			}
			for _, f := range rd.importFiles(p, arg) {
				if seen["f "+f] {
					continue
				}
				seen["f "+f] = true
				errs := len(rd.res.Errors)
				if sub, ok := rd.parseFile(f); ok {
					out = append(out, rd.rewriteSeen(f, sub, depth+1, seen)...)
				}
				rd.res.Errors = rd.res.Errors[:errs]
			}
		}
	}
	return out
}

// routeRewritesBeforeMatcher: in a route block, a rewrite followed by a nested
// matcher (handle, handle_path, route) applies in the written order.
func routeRewritesBeforeMatcher(block []caddyfile.Node) bool {
	seen := false
	for _, n := range block {
		switch n.Name() {
		case "rewrite", "uri":
			seen = true
		case "handle", "handle_path", "route":
			if seen {
				return true
			}
		}
	}
	return false
}

// nested walks a block in its own order mode: route blocks keep the written
// order, handle blocks apply their rewrites to all their handlers.
func (rd *reader) nested(ordered bool, p string, s *site, block []caddyfile.Node, under matcher, via []fact.Evidence, depth int) {
	prev := rd.ordered
	rd.ordered = ordered
	rd.directives(p, s, block, under, via, depth)
	rd.ordered = prev
}
