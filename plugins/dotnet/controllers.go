package dotnet

// MVC controllers: attribute-routed actions of the classes that
// are controllers by the spec's three tests.

import "strings"

// attr is one attribute of an attribute list, as written.
type attr struct {
	name string // last segment of the name, without an Attribute suffix
	args [][]token
	line int
	text string // [Name(args)], as written
}

// attrLists reads the attribute lists `[A, B(x)][C]` that start at toks[i] and
// returns them with the index after the last list. Lists with a target
// (`[return: X]`, `[assembly: X]`) are skipped.
func attrLists(toks []token, i int) ([]attr, int) {
	var out []attr
	for i < len(toks) && isP(toks[i], "[") {
		c := closing(toks, i, "[", "]")
		if c < 0 {
			break
		}
		inner := toks[i+1 : c]
		if !(len(inner) > 1 && inner[0].kind == tokIdent && isP(inner[1], ":")) {
			for _, piece := range splitTop(inner) {
				name, j := dotted(piece, 0)
				if name == "" {
					continue
				}
				a := attr{name: name[strings.LastIndexByte(name, '.')+1:], line: piece[0].line, text: "[" + render(piece) + "]"}
				a.name = strings.TrimSuffix(a.name, "Attribute")
				if j < len(piece) && isP(piece[j], "(") {
					if end := closing(piece, j, "(", ")"); end > 0 {
						a.args = splitTop(piece[j+1 : end])
					}
				}
				out = append(out, a)
			}
		}
		i = c + 1
	}
	return out, i
}

// firstArg is the first positional argument as a string, when it is a literal
// or a const of the file. ok is false for a non-literal.
func (e *epFile) firstArg(a attr) (string, bool, bool) {
	if len(a.args) == 0 {
		return "", false, true
	}
	if len(a.args[0]) > 2 && a.args[0][0].kind == tokIdent && isP(a.args[0][1], ":") || len(a.args[0]) > 1 && a.args[0][0].kind == tokIdent && isP(a.args[0][1], "=") {
		return "", false, true // a named argument, not a template
	}
	v, ok := csValue(a.args[0], e.consts)
	return v, true, ok
}

var classMods = map[string]bool{"public": true, "internal": true, "private": true, "protected": true, "sealed": true, "abstract": true,
	"partial": true, "static": true, "unsafe": true, "new": true, "file": true}

var httpVerbs = map[string]string{"HttpGet": "GET", "HttpPost": "POST", "HttpPut": "PUT", "HttpDelete": "DELETE", "HttpPatch": "PATCH", "HttpHead": "HEAD", "HttpOptions": "OPTIONS"}

func authMarkers(attrs []attr) []marker {
	var out []marker
	for _, a := range attrs {
		if a.name == "Authorize" || a.name == "AllowAnonymous" {
			out = append(out, marker{text: a.text, line: a.line, anon: a.name == "AllowAnonymous"})
		}
	}
	return out
}

// controllers reads every class of the file that is a controller.
func (e *epFile) controllers() {
	toks := e.toks
	for c := 0; c+1 < len(toks); c++ {
		if !isID(toks[c], "class") || toks[c+1].kind != tokIdent {
			continue
		}
		b := c - 1
		for b >= 0 && !isP(toks[b], ";") && !isP(toks[b], "{") && !isP(toks[b], "}") {
			b--
		}
		attrs, j := attrLists(toks, b+1)
		mods := map[string]bool{}
		for ; j < c; j++ {
			if toks[j].kind != tokIdent || !classMods[toks[j].text] {
				break
			}
			mods[toks[j].text] = true
		}
		if j != c || mods["abstract"] {
			continue
		}
		e.controller(toks[c+1].text, attrs, c+2)
	}
}

// controller reads one class whose header continues at toks[i].
func (e *epFile) controller(name string, attrs []attr, i int) {
	toks := e.toks
	if i < len(toks) && isP(toks[i], "<") && toks[i].pair > 0 {
		i += toks[i].pair + 1
	}
	var bases []string
	if i < len(toks) && isP(toks[i], ":") {
		for i++; i < len(toks) && !isP(toks[i], "{") && !isP(toks[i], ";") && !isID(toks[i], "where"); {
			if toks[i].kind == tokIdent {
				n, k := dotted(toks, i)
				bases = append(bases, n[strings.LastIndexByte(n, '.')+1:])
				i = k
				if i < len(toks) && isP(toks[i], "<") && toks[i].pair > 0 {
					i += toks[i].pair + 1
				}
				continue
			}
			i++
		}
	}
	for i < len(toks) && !isP(toks[i], "{") && !isP(toks[i], ";") {
		i++
	}
	end := closing(toks, i, "{", "}")
	if end < 0 {
		return
	}
	var routes []attr
	area, hasArea := "", false
	apiController := false
	for _, a := range attrs {
		switch a.name {
		case "Route":
			routes = append(routes, a)
		case "Area":
			if v, _, ok := e.firstArg(a); ok {
				area, hasArea = v, true
			}
		case "ApiController":
			apiController = true
		}
	}
	derived := false
	for _, b := range bases {
		derived = derived || b == "Controller" || b == "ControllerBase"
	}
	if !apiController && !derived && !(strings.HasSuffix(name, "Controller") && len(routes) > 0) {
		return
	}
	ctrl := strings.TrimSuffix(name, "Controller")
	classMarkers := authMarkers(attrs)

	for j := i + 1; j < end; {
		start := j
		mattrs, k := attrLists(toks, j)
		// the member runs to its first top-level ; or body, unless an = or => comes first
		assigned := false
		paren := -1
		m := k
		for ; m < end; m++ {
			t := toks[m]
			if t.kind == tokPunct {
				if isP(t, "=") {
					assigned = true
				}
				if isP(t, "(") && paren < 0 && !assigned {
					paren = m
				}
				if (t.text == "(" || t.text == "[" || t.text == "{") && t.pair > 0 && m+t.pair < end+1 {
					m += t.pair
					if t.text == "{" && !assigned {
						break
					}
					continue
				}
				if t.text == ";" {
					break
				}
			}
		}
		j = m + 1
		if j <= start {
			j = start + 1
		}
		if paren < k+2 || toks[paren-1].kind != tokIdent {
			continue
		}
		before := toks[paren-2]
		if !(before.kind == tokIdent && !classMods[before.text] || isP(before, ">") || isP(before, "]") || isP(before, "?") || isP(before, ")")) {
			continue
		}
		public, static := false, false
		for _, t := range toks[k:paren] {
			public = public || isID(t, "public")
			static = static || isID(t, "static")
		}
		if !public || static {
			continue
		}
		e.action(ctrl, name, toks[paren-1].text, mattrs, classMarkers, routes, area, hasArea)
	}
}

// action reports the endpoints of one action method.
func (e *epFile) action(ctrl, class, method string, attrs []attr, classMarkers []marker, classRoutes []attr, area string, hasArea bool) {
	type reg struct {
		verb string
		tpl  attr // the attribute that carries the template, zero when none
		line int
	}
	var routeAttr *attr
	var regs []reg
	for i, a := range attrs {
		switch a.name {
		case "NonAction":
			return
		case "Route":
			routeAttr = &attrs[i]
		}
	}
	for _, a := range attrs {
		if v, ok := httpVerbs[a.name]; ok {
			regs = append(regs, reg{verb: v, tpl: a, line: a.line})
		} else if a.name == "AcceptVerbs" {
			for _, arg := range a.args {
				if len(arg) == 1 && arg[0].kind == tokString && arg[0].literal {
					regs = append(regs, reg{verb: strings.ToUpper(arg[0].text), line: a.line})
				}
			}
		}
	}
	if len(regs) == 0 && routeAttr != nil {
		regs = append(regs, reg{verb: "ANY", line: routeAttr.line})
	}
	own := append(authMarkers(attrs), classMarkers...)
	for _, r := range regs {
		tplAttr := r.tpl
		if tplAttr.name == "" || len(tplAttr.args) == 0 {
			if routeAttr != nil {
				tplAttr = *routeAttr
			}
		}
		tpl, has, ok := e.firstArg(tplAttr)
		if has && !ok {
			continue // a template the reader cannot read
		}
		rooted := strings.HasPrefix(tpl, "/") || strings.HasPrefix(tpl, "~/")
		crs := classRoutes
		if rooted || len(crs) == 0 {
			crs = []attr{{}}
		}
		for _, cr := range crs {
			full, unresolved := tpl, false
			var prefixLines []int
			if cr.name != "" {
				cv, chas, cok := e.firstArg(cr)
				if chas && !cok {
					full, unresolved = "{?}/"+tpl, true
				} else {
					full = cv + "/" + tpl
				}
				prefixLines = append(prefixLines, cr.line)
			}
			full = strings.NewReplacer("[controller]", ctrl, "[action]", method).Replace(full)
			if strings.Contains(full, "[area]") {
				if hasArea {
					full = strings.ReplaceAll(full, "[area]", area)
				} else {
					full, unresolved = strings.ReplaceAll(full, "[area]", "{?}"), true
				}
			}
			cond := ppCond(e.lines, r.line)
			e.eps = append(e.eps, &routeEp{file: e.file, framework: "aspnet-mvc", method: r.verb, raw: tpl, handler: class + "." + method,
				line: r.line, path: normPath(full), unresolved: unresolved, prefixLines: prefixLines, markers: own, cond: cond})
		}
	}
}
