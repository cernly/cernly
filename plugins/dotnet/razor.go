package dotnet

// Razor Pages and Blazor endpoints: the `@page` and `@attribute`
// directives of .cshtml and .razor files, read line by line. Markup is not
// parsed as C#.

import (
	"path"
	"slices"
	"strings"
)

// directive is the rest of a line that starts with `@name`; `@pageTitle` is
// not `@page`.
func directive(line, name string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "@"+name) {
		return "", false
	}
	rest := t[len(name)+1:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// razorEndpoints reads the GET endpoints of one .cshtml or .razor file of the
// project at dir.
func (s *state) razorEndpoints(f, dir string) []*routeEp {
	data, err := s.r.Read(f)
	if err != nil {
		return nil
	}
	cshtml := strings.EqualFold(path.Ext(f), ".cshtml")
	type pageDir struct {
		line     int
		template string
		has      bool
	}
	var pages []pageDir
	var markers []marker
	inComment := false
	for i, l := range splitLines(string(data)) {
		line := i + 1
		if inComment {
			inComment = !strings.Contains(l, "*@")
			continue
		}
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "@*") {
			inComment = !strings.Contains(t[2:], "*@")
			continue
		}
		if rest, ok := directive(l, "page"); ok {
			pd := pageDir{line: line}
			switch {
			case rest == "" || strings.HasPrefix(rest, "@*") || strings.HasPrefix(rest, "//"):
			case rest[0] == '"':
				end := strings.IndexByte(rest[1:], '"')
				if end < 0 || strings.Contains(rest[1:end+1], "\\") {
					if cshtml {
						return nil // the first @page decides, and it cannot be read
					}
					continue
				}
				pd.template, pd.has = rest[1:end+1], true
			default:
				if cshtml {
					return nil
				}
				continue // a template the reader cannot read
			}
			if !cshtml && !pd.has {
				continue // a component needs a route template
			}
			pages = append(pages, pd)
			continue
		}
		if rest, ok := directive(l, "attribute"); ok {
			attrs, _ := attrLists(lex([]byte(rest)), 0)
			for _, m := range authMarkers(attrs) {
				m.line = line
				markers = append(markers, m)
			}
		}
	}
	if len(pages) == 0 {
		return nil
	}
	if cshtml {
		pages = pages[:1] // a Razor Page has one @page
		markers = append(markers, s.pageModelMarkers(f, dir)...)
	}
	var out []*routeEp
	for _, pd := range pages {
		ep := &routeEp{file: f, framework: "blazor", method: "GET", raw: pd.template, line: pd.line, markers: append([]marker(nil), markers...)}
		if cshtml {
			ep.framework = "aspnet-razor"
			ep.path, ep.unresolved = razorPagePath(f, dir, pd.template, pd.has)
		} else {
			ep.path = normPath(pd.template)
		}
		out = append(out, ep)
	}
	return out
}

// razorPagePath is the route of a Razor Page: its folder path below the
// project's Pages folder or Areas/<name>/Pages folder (the area is a prefix),
// with Index dropped, or the rooted template. Without a Pages folder the path
// starts {?}.
func razorPagePath(f, dir, template string, has bool) (string, bool) {
	if has && (strings.HasPrefix(template, "/") || strings.HasPrefix(template, "~/")) {
		return normPath(template), false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(f, dir), "/")
	segs := strings.Split(strings.TrimSuffix(rel, path.Ext(rel)), "/")
	prefix, unresolved := "{?}", true
	switch {
	case len(segs) > 1 && segs[0] == "Pages":
		prefix, unresolved, segs = "", false, segs[1:]
	case len(segs) > 3 && segs[0] == "Areas" && segs[2] == "Pages":
		prefix, unresolved, segs = "/"+segs[1], false, segs[3:]
	}
	if n := len(segs); segs[n-1] == "Index" {
		segs = segs[:n-1]
	}
	p := prefix + "/" + strings.Join(segs, "/")
	if has {
		p += "/" + template
	}
	return normPath(p), unresolved
}

// pageModelMarkers are the auth markers on the page model class of the
// code-behind file next to a Razor Page: the class named <Page>Model, or one
// that derives from PageModel.
func (s *state) pageModelMarkers(f, dir string) []marker {
	cb := f + ".cs"
	if !slices.Contains(s.sources[dir], cb) {
		return nil // not tracked, or not the project's
	}
	toks, err := s.tokens(cb)
	if err != nil {
		return nil
	}
	stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
	var out []marker
	for c := 0; c+1 < len(toks); c++ {
		if !isID(toks[c], "class") || toks[c+1].kind != tokIdent {
			continue
		}
		b := c - 1
		for b >= 0 && !isP(toks[b], ";") && !isP(toks[b], "{") && !isP(toks[b], "}") {
			b--
		}
		attrs, _ := attrLists(toks, b+1)
		match := toks[c+1].text == stem+"Model"
		for i := c + 2; i < len(toks) && !isP(toks[i], "{") && !isP(toks[i], ";") && !match; i++ {
			if !isP(toks[i], ":") {
				continue
			}
			for j := i + 1; j < len(toks) && !isP(toks[j], "{") && !isID(toks[j], "where"); j++ {
				if toks[j].kind == tokIdent {
					n, k := dotted(toks, j)
					match = match || n[strings.LastIndexByte(n, '.')+1:] == "PageModel"
					j = max(k, j+1) - 1
				}
			}
			break
		}
		if match {
			for _, m := range authMarkers(attrs) {
				m.file = cb
				out = append(out, m)
			}
		}
	}
	return out
}
