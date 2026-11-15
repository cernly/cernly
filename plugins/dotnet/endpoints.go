package dotnet

// ASP.NET endpoints: minimal API registrations and MVC
// controllers, read from the token stream. What the code states is recorded;
// a receiver, prefix or path the reader cannot read gives `{?}` and
// ext.prefix_unresolved, and a call it cannot decide gives nothing.

import (
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/testfile"
	"github.com/cernly/cernly/plugin"
)

// marker is an auth marker as written, with the line that holds it.
type marker struct {
	text string
	line int
	anon bool   // AllowAnonymous; otherwise an authorize marker
	file string // the file that holds it, when not the endpoint's own
	cond string // the condition a group marker sits under
	weak bool   // conditional and the endpoint is not under it: it does not decide the state
}

// routeEp is one endpoint before it becomes a fact.
type routeEp struct {
	file, framework, method, raw, handler, cond string
	line                                        int
	path                                        string // set once resolved
	unresolved                                  bool
	prefixLines                                 []int
	markers                                     []marker // nearest first

	// minimal APIs: the path is built from the group chain at the end of the file
	group    *group
	recvUnk  bool // no group, and the receiver is not a known route builder
	template string
}

// group is a MapGroup: its prefix, its parent and the markers applied to it.
type group struct {
	prefix  string
	ok      bool // the prefix is read
	line    int
	parent  *group
	recvUnk bool // the receiver is not a known route builder (parent is nil)
	markers []marker
}

type objKind int

const (
	oNone objKind = iota
	oBuilder
	oRoot
	oGroup
	oEndpoints
	oMapped   // MapControllers() or MapRazorPages() on a root: scope holds which
	oServices // builder.Services
	oAuthBuilder
)

// obj is what an expression evaluates to, as far as the reader follows it.
type obj struct {
	kind  objKind
	g     *group
	eps   []*routeEp
	scope string
	pol   int  // oMapped: 1 + the index of the policy it recorded
	anon  bool // oMapped: AllowAnonymous was applied
}

var minimalVerbs = map[string]string{"MapGet": "GET", "MapPost": "POST", "MapPut": "PUT", "MapPatch": "PATCH", "MapDelete": "DELETE", "Map": "ANY"}

// endpoints reports ASP.NET endpoints from the .cs files of non-test SDK
// projects. A repeated key merges its evidence; clashing auth becomes unknown.
func endpoints(s *state, res *plugin.Result) {
	var out []fact.Fact
	idx := map[string]int{}
	for _, dir := range slices.Sorted(maps.Keys(s.sources)) {
		if s.testDir(dir) {
			continue
		}
		unit := s.appUnit(dir)
		add := func(fa fact.Fact) {
			i, ok := idx[fa.Key]
			if !ok {
				idx[fa.Key] = len(out)
				out = append(out, fa)
				return
			}
			if fa.Kind == fact.KindEndpoint {
				mergeEp(&out[i], fa)
			}
		}
		for _, f := range s.sources[dir] {
			ext := strings.ToLower(path.Ext(f))
			if testfile.Is(f) {
				continue
			}
			if ext == ".cshtml" || ext == ".razor" {
				for _, ep := range s.razorEndpoints(f, dir) {
					add(epFact(ep, dir, unit))
				}
				continue
			}
			if ext != ".cs" {
				continue
			}
			toks, err := s.tokens(f)
			if err != nil {
				continue
			}
			data, err := s.r.Read(f)
			if err != nil {
				continue
			}
			ef := &epFile{file: f, toks: toks, lines: splitLines(string(data)), consts: fileConsts(toks), scopes: []map[string]obj{{}}, visited: map[int]bool{}, pending: map[int]map[string]obj{}}
			ef.ranges = ifRanges(toks, ef.lines)
			ef.scan()
			ef.finishMinimal()
			ef.controllers()
			for _, ep := range ef.eps {
				add(epFact(ep, dir, unit))
			}
			for _, pol := range ef.policies {
				if pol.drop {
					continue
				}
				add(policyFact(f, dir, unit, pol))
			}
		}
	}
	res.Facts = append(res.Facts, out...)
}

// appUnit is the project folder when its SDK is a web one, else "".
func (s *state) appUnit(dir string) string {
	for _, p := range s.projects {
		if p.Dir == dir && (strings.EqualFold(p.SDK, "Microsoft.NET.Sdk.Web") || strings.EqualFold(p.SDK, "Microsoft.NET.Sdk.BlazorWebAssembly")) {
			return dir
		}
	}
	return ""
}

func epFact(ep *routeEp, dir, unit string) fact.Fact {
	state := "unknown"
	var deciding []marker
	for _, anon := range []bool{true, false} {
		for _, m := range ep.markers {
			if m.anon == anon && !m.weak {
				deciding = append(deciding, m)
			}
		}
		if len(deciding) > 0 {
			state = map[bool]string{true: "anonymous", false: "required"}[anon]
			break
		}
	}
	if state == "required" {
		for _, m := range ep.markers {
			if m.weak && m.anon {
				state = "unknown" // a conditional AllowAnonymous may open it
			}
		}
	}
	ev := func(line int) fact.Evidence { return fact.Evidence{Path: ep.file, Line: line, Unit: dir} }
	mev := func(m marker) fact.Evidence {
		e := ev(m.line)
		if m.file != "" {
			e.Path = m.file
		}
		return e
	}
	evidence := []fact.Evidence{ev(ep.line)}
	for _, l := range ep.prefixLines {
		evidence = append(evidence, ev(l))
	}
	texts := []any{}
	for _, m := range deciding {
		texts = append(texts, m.text)
		evidence = append(evidence, mev(m))
	}
	for _, m := range ep.markers { // conditional markers stay visible, with their condition
		if m.weak {
			texts = append(texts, m.text+" (if "+m.cond+")")
			evidence = append(evidence, mev(m))
		}
	}
	attrs := map[string]any{"file": ep.file, "line": ep.line, "language": "csharp", "framework": ep.framework, "method": ep.method, "path": ep.path, "raw": ep.raw,
		"auth": map[string]any{"state": state, "markers": texts}}
	if ep.handler != "" {
		attrs["handler"] = ep.handler
	}
	if unit != "" {
		attrs["app_unit"] = unit
	}
	extra := map[string]any{}
	if ep.unresolved {
		extra["prefix_unresolved"] = true
	}
	if ep.cond != "" {
		extra["conditional"] = ep.cond
	}
	if len(extra) > 0 {
		attrs["ext"] = extra
	}
	return fact.Fact{Kind: fact.KindEndpoint, Key: "endpoint:csharp:" + ep.file + ":" + ep.method + ":" + ep.path, Plugin: "dotnet",
		Attrs: attrs, Evidence: dedupeEv(evidence)}
}

func dedupeEv(ev []fact.Evidence) []fact.Evidence {
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

// mergeEp folds b into a (same key): evidence joined, markers united, a
// condition kept only when both have it, and auth unknown when the two
// registrations disagree.
func mergeEp(a *fact.Fact, b fact.Fact) {
	a.Evidence = dedupeEv(append(a.Evidence, b.Evidence...))
	aa, ba := a.Attrs["auth"].(map[string]any), b.Attrs["auth"].(map[string]any)
	if aa["state"] != ba["state"] {
		aa["state"] = "unknown"
	}
	seen := map[string]bool{}
	ms := []any{}
	for _, l := range [][]any{aa["markers"].([]any), ba["markers"].([]any)} {
		for _, m := range l {
			if !seen[m.(string)] {
				seen[m.(string)] = true
				ms = append(ms, m)
			}
		}
	}
	aa["markers"] = ms
	ae, _ := a.Attrs["ext"].(map[string]any)
	be, _ := b.Attrs["ext"].(map[string]any)
	if ae["conditional"] != be["conditional"] && ae != nil {
		delete(ae, "conditional")
		if len(ae) == 0 {
			delete(a.Attrs, "ext")
		}
	}
	if _, ok := a.Attrs["handler"]; ok && a.Attrs["handler"] != b.Attrs["handler"] {
		delete(a.Attrs, "handler")
	}
}

// epFile reads the endpoints of one lexed file.
type epFile struct {
	file     string
	toks     []token
	lines    []string
	consts   map[string]string
	ranges   []ifRange
	scopes   []map[string]obj
	eps      []*routeEp
	visited  map[int]bool
	pending  map[int]map[string]obj // token index of a `{` -> parameter bindings
	policies []policy
	shadows  []shadow // parameters of an expression-bodied member or lambda
	pos      int      // the token the scan is at
}

// shadow binds parameter names over the tokens of an expression body.
type shadow struct {
	from, to int
	names    map[string]obj
}

// fileConsts are the const strings of the file by bare name and as Class.Name;
// a name with two values resolves to none.
func fileConsts(toks []token) map[string]string {
	consts := map[string]string{}
	clash := map[string]bool{}
	add := func(k, v string) {
		if old, ok := consts[k]; ok && old != v {
			clash[k] = true
		}
		consts[k] = v
	}
	for k, v := range constStrings(toks) {
		add(k, v)
	}
	classConsts(toks, func(class, name, v string) { add(class+"."+name, v) })
	for k := range clash {
		delete(consts, k)
	}
	return consts
}

func splitLines(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff")
	l := strings.Split(s, "\n")
	for i := range l {
		l[i] = strings.TrimSuffix(l[i], "\r")
	}
	return l
}

func (e *epFile) lookup(name string) obj {
	if strings.Contains(name, ".") {
		return obj{}
	}
	for i := len(e.shadows) - 1; i >= 0; i-- {
		if sh := e.shadows[i]; sh.from < e.pos && e.pos < sh.to {
			if o, ok := sh.names[name]; ok {
				return o
			}
		}
	}
	for i := len(e.scopes) - 1; i >= 0; i-- {
		if o, ok := e.scopes[i][name]; ok {
			return o
		}
	}
	switch name {
	case "app", "endpoints":
		return obj{kind: oRoot} // the conventional names, when nothing in scope binds them
	case "builder":
		return obj{kind: oBuilder}
	}
	return obj{}
}

var notMethod = map[string]bool{"if": true, "while": true, "for": true, "foreach": true, "using": true, "switch": true, "lock": true, "catch": true, "fixed": true, "when": true}

// scan walks the tokens once, keeping the variable scopes: a block opens one,
// the parameters of the method or lambda in front of it shadow outer names
// (the lambda of UseEndpoints gets a route builder), and `x = <call chain>`
// binds x to what the chain gives.
func (e *epFile) scan() {
	toks := e.toks
	pending := e.pending
	for i := 0; i < len(toks); i++ {
		e.pos = i
		t := toks[i]
		switch {
		case isP(t, "{"):
			sc := pending[i]
			if sc == nil {
				sc = map[string]obj{}
			}
			e.scopes = append(e.scopes, sc)
		case isP(t, "}"):
			if len(e.scopes) > 1 {
				e.scopes = e.scopes[:len(e.scopes)-1]
			}
		case isP(t, "("):
			e.params(i)
		case t.kind == tokIdent && (i == 0 || !isP(toks[i-1], ".")):
			if i+3 < len(toks) && isP(toks[i+1], "=") && isP(toks[i+2], ">") && isP(toks[i+3], "{") {
				if _, ok := pending[i+3]; !ok {
					pending[i+3] = map[string]obj{t.text: {}}
				}
				continue
			}
			if i+3 < len(toks) && isP(toks[i+1], "=") && isP(toks[i+2], ">") && !isP(toks[i+3], "{") {
				e.shadows = append(e.shadows, shadow{from: i + 2, to: exprEnd(toks, i+3), names: map[string]obj{t.text: {}}})
				continue
			}
			if i+2 < len(toks) && isP(toks[i+1], "=") && !isP(toks[i+2], "=") && !isP(toks[i+2], ">") {
				o := e.chain(i + 2)
				e.visited[i+2] = true
				e.scopes[len(e.scopes)-1][t.text] = o
				continue
			}
			if !e.visited[i] {
				e.visited[i] = true
				e.chain(i)
			}
		}
	}
}

// params records the parameter names of a method, local function or lambda
// whose body follows the parenthesis group at open: a block (the names bind
// in its scope) or an expression (they bind up to its end). A generic
// parameter list before the parentheses and where clauses after them are
// skipped. An entry already pending, such as the UseEndpoints lambda's, stays.
func (e *epFile) params(open int) {
	toks := e.toks
	c := closing(toks, open, "(", ")")
	if c < 0 || c+1 >= len(toks) {
		return
	}
	k := c + 1
	for k < len(toks) && isID(toks[k], "where") {
		for k < len(toks) && !isP(toks[k], "{") && !isP(toks[k], ";") && !(isP(toks[k], "=") && k+1 < len(toks) && isP(toks[k+1], ">")) {
			if t := toks[k]; t.kind == tokPunct && (t.text == "(" || t.text == "[" || t.text == "<") && t.pair > 0 {
				k += t.pair
			}
			k++
		}
	}
	if k >= len(toks) {
		return
	}
	// the name in front of the parentheses, past a generic parameter list
	n := open - 1
	if n >= 0 && isP(toks[n], ">") {
		depth := 0
		for ; n >= 0; n-- {
			if isP(toks[n], ">") {
				depth++
			} else if isP(toks[n], "<") {
				if depth--; depth == 0 {
					n--
					break
				}
			}
		}
	}
	named := n >= 0 && toks[n].kind == tokIdent && !notMethod[toks[n].text]
	arrow := k+1 < len(toks) && isP(toks[k], "=") && isP(toks[k+1], ">")
	pieces := splitTop(toks[open+1 : c])
	names := map[string]obj{}
	for _, p := range pieces {
		l := len(p)
		switch {
		case named: // a method: typed parameters
			if l >= 2 && p[l-1].kind == tokIdent {
				names[p[l-1].text] = obj{}
			}
		case !named && arrow && l >= 1 && p[l-1].kind == tokIdent: // a lambda
			names[p[l-1].text] = obj{}
		}
	}
	if len(names) == 0 {
		return
	}
	switch {
	case isP(toks[k], "{") && named:
		if _, ok := e.pending[k]; !ok {
			e.pending[k] = names
		}
	case arrow && k+2 < len(toks) && isP(toks[k+2], "{"):
		if _, ok := e.pending[k+2]; !ok {
			e.pending[k+2] = names
		}
	case arrow:
		e.shadows = append(e.shadows, shadow{from: k, to: exprEnd(toks, k+2), names: names})
	}
}

// exprEnd is the index of the token that ends the expression starting at i: a
// ; or an unmatched closing bracket or comma.
func exprEnd(toks []token, i int) int {
	for ; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			if t.pair > 0 {
				i += t.pair
			}
		case ";", ",", ")", "]", "}":
			return i
		}
	}
	return len(toks)
}

// chain evaluates the call chain that starts at toks[i] (`a.B(...).C(...)`),
// registering the endpoints and groups it holds, and returns its result.
func (e *epFile) chain(i int) obj {
	toks := e.toks
	name, j := dotted(toks, i)
	if name == "" || j >= len(toks) || !isP(toks[j], "(") {
		return obj{}
	}
	k := strings.LastIndexByte(name, '.')
	if k < 0 {
		return obj{}
	}
	recv, method := name[:k], name[k+1:]
	cur := e.lookup(recv)
	if r, ok := strings.CutSuffix(recv, ".Services"); ok && e.lookup(r).kind == oBuilder {
		cur = obj{kind: oServices}
	}
	if recv == "WebApplication" || recv == "Microsoft.AspNetCore.Builder.WebApplication" {
		cur = obj{kind: oBuilder}
		if method == "Create" {
			cur = obj{kind: oRoot}
		}
	}
	for {
		end := closing(toks, j, "(", ")")
		if end < 0 {
			return obj{}
		}
		cur = e.call(cur, method, splitTop(toks[j+1:end]), j-1)
		j = end + 1
		if j+1 < len(toks) && isP(toks[j], ".") && toks[j+1].kind == tokIdent {
			method = toks[j+1].text
			jj := j + 2
			if jj < len(toks) && isP(toks[jj], "<") && toks[jj].pair > 0 {
				jj += toks[jj].pair + 1
			}
			if jj < len(toks) && isP(toks[jj], "(") {
				j = jj
				continue
			}
		}
		return cur
	}
}

// call applies one call of a chain to what the chain holds so far; idx is the
// token of the method name.
func (e *epFile) call(cur obj, method string, args [][]token, idx int) obj {
	line := e.toks[idx].line
	switch method {
	case "Build":
		if len(args) == 0 && cur.kind == oBuilder {
			return obj{kind: oRoot}
		}
	case "MapGroup":
		if len(args) < 1 {
			return obj{}
		}
		g := &group{line: line}
		g.prefix, g.ok = csValue(args[0], e.consts)
		switch cur.kind {
		case oGroup:
			g.parent = cur.g
		case oRoot:
		default:
			g.recvUnk = true
		}
		return obj{kind: oGroup, g: g}
	case "UseEndpoints":
		// the lambda's parameter is the root only when the receiver is
		if cur.kind == oRoot {
			e.useEndpointsRoot(idx)
		}
	case "MapControllers", "MapRazorPages":
		if cur.kind == oRoot && len(args) == 0 {
			return obj{kind: oMapped, scope: map[string]string{"MapControllers": "controllers", "MapRazorPages": "razor"}[method]}
		}
		return obj{}
	case "AddAuthorization":
		if cur.kind == oServices {
			e.fallbackAssigned(args, line)
		}
		return obj{}
	case "AddAuthorizationBuilder":
		if cur.kind == oServices {
			return obj{kind: oAuthBuilder}
		}
		return obj{}
	case "SetFallbackPolicy":
		if cur.kind == oAuthBuilder && len(args) == 1 && !isNull(args[0]) {
			e.policies = append(e.policies, policy{line: line, scope: "fallback"})
		}
		return cur
	case "RequireAuthorization", "AllowAnonymous":
		if cur.kind == oMapped {
			if method == "RequireAuthorization" && !cur.anon && cur.pol == 0 {
				e.policies = append(e.policies, policy{line: line, scope: cur.scope})
				cur.pol = len(e.policies)
			} else if method == "AllowAnonymous" {
				cur.anon = true
				if cur.pol > 0 {
					e.policies[cur.pol-1].drop = true
				}
			}
			return cur
		}
		m := marker{text: method + "(" + render(joinArgs(args)) + ")", line: line, anon: method == "AllowAnonymous"}
		switch cur.kind {
		case oGroup:
			m.cond = joinCond(ppCond(e.lines, line), e.ifConds(idx))
			cur.g.markers = append(cur.g.markers, m)
		case oEndpoints:
			for _, ep := range cur.eps {
				ep.markers = append(ep.markers, m)
			}
		}
		return cur
	case "MapGet", "MapPost", "MapPut", "MapPatch", "MapDelete", "Map", "MapMethods", "MapHealthChecks", "MapControllerRoute":
		return e.register(cur, method, args, idx)
	}
	if strings.HasPrefix(method, "Map") {
		return obj{} // MapControllers, MapRazorPages, MapHub...: not a route builder we follow
	}
	return cur
}

// useEndpointsRoot makes the parameter of the lambda argument of the
// UseEndpoints call at idx a root route builder inside its block body.
func (e *epFile) useEndpointsRoot(idx int) {
	toks := e.toks
	j := idx + 2
	if j+3 >= len(toks) {
		return
	}
	name := ""
	switch {
	case toks[j].kind == tokIdent:
		name = toks[j].text
		j++
	case isP(toks[j], "("):
		c := closing(toks, j, "(", ")")
		if c == j+2 && toks[j+1].kind == tokIdent {
			name = toks[j+1].text
		}
		j = c + 1
	}
	if name != "" && j+2 < len(toks) && isP(toks[j], "=") && isP(toks[j+1], ">") && isP(toks[j+2], "{") {
		e.pending[j+2] = map[string]obj{name: {kind: oRoot}}
	}
}

// branchMethod reports a method group handler that the file declares with an
// IApplicationBuilder first parameter: it configures a middleware branch.
func (e *epFile) branchMethod(h []token) bool {
	name, j := dotted(h, 0)
	if name == "" || j != len(h) {
		return false
	}
	name = name[strings.LastIndexByte(name, '.')+1:]
	for i := 0; i+2 < len(e.toks); i++ {
		if isID(e.toks[i], name) && isP(e.toks[i+1], "(") && e.toks[i+2].kind == tokIdent {
			if t, _ := dotted(e.toks, i+2); t == "IApplicationBuilder" || strings.HasSuffix(t, ".IApplicationBuilder") {
				return true
			}
		}
	}
	return false
}

// branches reports a handler argument that is a middleware branch (a lambda
// that calls Use*, Run or UseEndpoints on its builder): Map with it is
// IApplicationBuilder branching, not an endpoint.
func branches(h []token) bool {
	arrow := -1
	for i := 0; i+1 < len(h); i++ {
		if isP(h[i], "=") && isP(h[i+1], ">") {
			arrow = i
			break
		}
	}
	if arrow < 0 {
		return false
	}
	for i := arrow + 2; i+1 < len(h); i++ {
		if h[i].kind != tokIdent || !(strings.HasPrefix(h[i].text, "Use") || h[i].text == "Run") || i == 0 || !isP(h[i-1], ".") {
			continue
		}
		j := i + 1
		if isP(h[j], "<") && h[j].pair > 0 {
			j += h[j].pair + 1
		}
		if j < len(h) && isP(h[j], "(") {
			return true
		}
	}
	return false
}

func joinArgs(args [][]token) []token {
	var out []token
	for i, a := range args {
		if i > 0 {
			out = append(out, token{kind: tokPunct, text: ","})
		}
		out = append(out, a...)
	}
	return out
}

// namedArg is the value of `name: value` among args.
func namedArg(args [][]token, name string) ([]token, bool) {
	for _, a := range args {
		if len(a) > 2 && isID(a[0], name) && isP(a[1], ":") {
			return a[2:], true
		}
	}
	return nil, false
}

// literalStrings are the string literals of a methods argument such as
// new[] { "PUT", "PATCH" } or ["PUT"].
func literalStrings(a []token) []string {
	var out []string
	for _, t := range a {
		if t.kind == tokString && t.literal {
			out = append(out, strings.ToUpper(t.text))
		}
	}
	return out
}

func (e *epFile) register(cur obj, method string, args [][]token, idx int) obj {
	if len(args) == 0 {
		return obj{}
	}
	var pattern []token
	var methods []string
	var handlerArg []token
	framework := "aspnet-minimal"
	switch method {
	case "MapGet", "MapPost", "MapPut", "MapPatch", "MapDelete", "Map":
		if method == "Map" && cur.kind != oRoot && cur.kind != oGroup {
			return obj{} // Map on an unknown receiver is as likely a mapper
		}
		pattern, methods = args[0], []string{minimalVerbs[method]}
		if len(args) > 1 {
			handlerArg = args[1]
		}
		if method == "Map" && (branches(handlerArg) || e.branchMethod(handlerArg)) {
			return obj{}
		}
	case "MapMethods":
		if len(args) < 2 {
			return obj{}
		}
		pattern, methods = args[0], literalStrings(args[1])
		if len(args) > 2 {
			handlerArg = args[2]
		}
	case "MapHealthChecks":
		pattern, methods = args[0], []string{"ANY"}
	case "MapControllerRoute":
		framework = "aspnet-mvc"
		methods = []string{"ANY"}
		if p, ok := namedArg(args, "pattern"); ok {
			pattern = p
		} else if len(args) > 1 {
			pattern = args[1]
		}
	}
	raw, ok := csValue(pattern, e.consts)
	if !ok || len(methods) == 0 {
		return obj{}
	}
	handler := ""
	if h, j := dotted(handlerArg, 0); h != "" && j == len(handlerArg) {
		handler = h
	}
	var own []marker
	for i := 0; i+1 < len(handlerArg); i++ { // [Authorize] () => ... on the handler
		if !isP(handlerArg[i], "[") {
			continue
		}
		name, _ := dotted(handlerArg, i+1)
		name = strings.TrimSuffix(name[strings.LastIndexByte(name, '.')+1:], "Attribute")
		if name != "Authorize" && name != "AllowAnonymous" {
			continue
		}
		if c := closing(handlerArg, i, "[", "]"); c > 0 {
			own = append(own, marker{text: "[" + render(handlerArg[i+1:c]) + "]", line: handlerArg[i+1].line, anon: name == "AllowAnonymous"})
		}
	}
	line := e.toks[idx].line
	cond := joinCond(ppCond(e.lines, line), e.ifConds(idx))
	var out []*routeEp
	for _, m := range methods {
		ep := &routeEp{file: e.file, framework: framework, method: m, raw: raw, handler: handler, cond: cond, line: line, template: raw,
			markers: append([]marker(nil), own...)}
		switch cur.kind {
		case oGroup:
			ep.group = cur.g
		case oRoot:
		default:
			ep.recvUnk = true
		}
		out = append(out, ep)
	}
	e.eps = append(e.eps, out...)
	return obj{kind: oEndpoints, eps: out}
}

// finishMinimal builds the path of every minimal endpoint from its group
// chain, and adds the markers of the groups, nearest first. Markers on a group
// apply to all its endpoints, whenever they come in the method.
func (e *epFile) finishMinimal() {
	for _, ep := range e.eps {
		var chain []*group
		for g := ep.group; g != nil; g = g.parent {
			chain = append([]*group{g}, chain...)
			for _, m := range g.markers {
				m.weak = m.cond != "" && !condCovers(ep.cond, m.cond)
				ep.markers = append(ep.markers, m)
			}
		}
		acc := ""
		unk := ep.recvUnk
		if len(chain) > 0 {
			unk = chain[0].recvUnk
		}
		if unk {
			acc, ep.unresolved = "{?}", true
		}
		for _, g := range chain {
			if !g.ok {
				acc, ep.unresolved = "{?}", true
			} else {
				acc += "/" + g.prefix
			}
			ep.prefixLines = append(ep.prefixLines, g.line)
		}
		ep.path = normPath(acc + "/" + ep.template)
	}
}

// ifRange is the token span a condition covers.
type ifRange struct {
	from, to int
	text     string
}

func (e *epFile) ifConds(idx int) string {
	var parts []string
	for _, r := range e.ranges {
		if r.from < idx && idx < r.to {
			parts = append(parts, r.text)
		}
	}
	return strings.Join(parts, " and ")
}

// condCovers reports that every condition in marker also holds for the
// endpoint, whose condition is ep.
func condCovers(ep, marker string) bool {
	have := map[string]bool{}
	for _, c := range strings.Split(ep, " and ") {
		have[c] = true
	}
	for _, c := range strings.Split(marker, " and ") {
		if !have[c] {
			return false
		}
	}
	return true
}

func joinCond(pp, ifs string) string {
	switch {
	case pp == "":
		return ifs
	case ifs == "":
		return pp
	}
	return pp + " and " + ifs
}

// ifRanges finds every `if (cond)` statement and the tokens of its body: the
// block, or the single statement up to its `;`. An else branch carries no
// condition.
func ifRanges(toks []token, lines []string) []ifRange {
	var out []ifRange
	perLine := map[int]int{} // if keywords seen per line
	for i := 0; i+1 < len(toks); i++ {
		if !isID(toks[i], "if") || !isP(toks[i+1], "(") {
			continue
		}
		nth := perLine[toks[i].line]
		perLine[toks[i].line]++
		c := closing(toks, i+1, "(", ")")
		if c < 0 || c+1 >= len(toks) {
			continue
		}
		end := -1
		if isP(toks[c+1], "{") {
			end = closing(toks, c+1, "{", "}")
		} else {
			for j := c + 1; j < len(toks); j++ {
				if t := toks[j]; t.kind == tokPunct && (t.text == "(" || t.text == "[" || t.text == "{") && t.pair > 0 {
					j += t.pair
				} else if isP(t, ";") {
					end = j
					break
				}
			}
		}
		if end < 0 {
			continue
		}
		text := ifText(lines, toks[i].line, nth)
		if text == "" {
			text = render(toks[i+2 : c])
		}
		out = append(out, ifRange{from: c, to: end, text: text})
	}
	return out
}

// ifText is the text between the parentheses of the nth `if (` on a source
// line, as written; "" when it does not close on that line.
func ifText(lines []string, line, nth int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	l := lines[line-1]
	pos := 0
	for found := 0; ; {
		k := strings.Index(l[pos:], "if")
		if k < 0 {
			return ""
		}
		k += pos
		pos = k + 2
		if k > 0 && isIdentByte(l[k-1]) {
			continue
		}
		rest := strings.TrimLeft(l[pos:], " \t")
		if !strings.HasPrefix(rest, "(") {
			continue
		}
		if found++; found-1 < nth {
			continue
		}
		start := len(l) - len(rest) + 1
		depth, inStr := 1, false
		for m := start; m < len(l); m++ {
			switch c := l[m]; {
			case inStr:
				if c == '\\' {
					m++
				} else if c == '"' {
					inStr = false
				}
			case c == '"':
				inStr = true
			case c == '(':
				depth++
			case c == ')':
				if depth--; depth == 0 {
					return strings.TrimSpace(l[start:m])
				}
			}
		}
		return ""
	}
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ppCond is the nearest unclosed #if or #elif above line, as written.
func ppCond(lines []string, line int) string {
	var stack []string
	for i := 0; i < line-1 && i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "#") {
			continue
		}
		d := strings.TrimLeft(t[1:], " \t")
		word := d
		if k := strings.IndexAny(d, " \t/("); k >= 0 {
			word = d[:k]
		}
		switch word {
		case "if":
			stack = append(stack, t)
		case "elif":
			if n := len(stack); n > 0 {
				stack[n-1] = t
			}
		case "else":
			if n := len(stack); n > 0 {
				stack[n-1] = "" // the negation of a condition is not written
			}
		case "endif":
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] != "" {
			return stack[i]
		}
	}
	return ""
}

// normPath turns a route template into the path the fact carries: a leading
// "/" (not before {?}), parameters as {name} or {*name}, no "//", no trailing
// "/" except the root.
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		p = p[1:]
	}
	var b strings.Builder
	for i := 0; i < len(p); {
		if p[i] != '{' {
			b.WriteByte(p[i])
			i++
			continue
		}
		depth, j := 0, i
		for ; j < len(p); j++ {
			if p[j] == '{' {
				depth++
			} else if p[j] == '}' {
				if depth--; depth == 0 {
					break
				}
			}
		}
		if j >= len(p) {
			b.WriteString(p[i:])
			break
		}
		b.WriteString(routeParam(p[i+1 : j]))
		i = j + 1
	}
	out := b.String()
	for strings.Contains(out, "//") {
		out = strings.ReplaceAll(out, "//", "/")
	}
	if !strings.HasPrefix(out, "{?}") && !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	if len(out) > 1 {
		out = strings.TrimSuffix(out, "/")
	}
	return out
}

// routeParam is {name} for the inside of a route parameter: constraints,
// optional marks and defaults dropped, catch-alls as {*name}.
func routeParam(inner string) string {
	if inner == "?" {
		return "{?}"
	}
	stars := 0
	for stars < len(inner) && inner[stars] == '*' {
		stars++
	}
	name := inner[stars:]
	if k := strings.IndexAny(name, ":?="); k >= 0 {
		name = name[:k]
	}
	if stars > 0 {
		return "{*" + name + "}"
	}
	return "{" + name + "}"
}

// render writes tokens back as source text for markers and conditions:
// strings quoted, a space after commas and around a lone =.
func render(toks []token) string {
	var b strings.Builder
	for i, t := range toks {
		switch {
		case t.kind == tokString && t.literal:
			b.WriteString(`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(t.text) + `"`)
		case isP(t, ","):
			b.WriteString(", ")
		case isP(t, "=") && !(i > 0 && isPunctOp(toks[i-1])) && !(i+1 < len(toks) && (isP(toks[i+1], "=") || isP(toks[i+1], ">"))):
			b.WriteString(" = ")
		default:
			b.WriteString(t.text)
		}
	}
	return strings.TrimSpace(b.String())
}

func isPunctOp(t token) bool {
	return t.kind == tokPunct && strings.Contains("=!<>", t.text)
}

// policy is a host-level auth policy the code states: a scope and its line.
type policy struct {
	drop  bool // AllowAnonymous followed it on the same chain
	line  int
	scope string
}

func isNull(a []token) bool { return len(a) == 1 && isID(a[0], "null") }

// fallbackAssigned records `FallbackPolicy = <not null>` inside the arguments
// of an AddAuthorization call, at the line of the assignment.
func (e *epFile) fallbackAssigned(args [][]token, callLine int) {
	for _, a := range args {
		for i := 1; i+2 < len(a); i++ {
			if isID(a[i], "FallbackPolicy") && isP(a[i-1], ".") && isP(a[i+1], "=") && !isP(a[i+2], "=") && !isP(a[i+2], ">") {
				end := i + 2
				for end < len(a) && !isP(a[end], ";") {
					end++
				}
				if !isNull(a[i+2 : end]) {
					e.policies = append(e.policies, policy{line: a[i].line, scope: "fallback"})
					return
				}
			}
		}
	}
}

func policyFact(file, dir, unit string, p policy) fact.Fact {
	attrs := map[string]any{"file": file, "line": p.line, "language": "csharp", "framework": "aspnet", "scope": p.scope, "state": "required"}
	if unit != "" {
		attrs["app_unit"] = unit
	}
	return fact.Fact{Kind: fact.KindAuthPolicy, Key: "auth_policy:csharp:" + file + ":" + strconv.Itoa(p.line) + ":" + p.scope, Plugin: "dotnet",
		Attrs: attrs, Evidence: []fact.Evidence{{Path: file, Line: p.line, Unit: dir}}}
}
