package main

import (
	"strings"

	"github.com/cernly/cernly/internal/hostname"
)

// requestMethods take the URL they call as their first argument
// (RestTemplate, WebClient's uri, its builder's baseUrl, RestTemplateBuilder's
// rootUri, RestClient's baseUrl).
var requestMethods = map[string]bool{
	"getForObject": true, "getForEntity": true, "postForObject": true, "postForEntity": true,
	"postForLocation": true, "patchForObject": true, "exchange": true, "put": true, "delete": true,
	"headForHeaders": true, "optionsForAllow": true,
	"baseUrl": true, "rootUri": true, "uri": true,
}

type annotation struct {
	name string // last segment: jakarta.persistence.Table -> Table
	line int
	args []token
}

// arg returns a named string argument (name = "x"), or the single unnamed
// one for "value"; string concatenation "a" + "b" is joined.
func (a annotation) arg(key string) (string, bool) {
	ts := a.args
	for i := 0; i+2 < len(ts); i++ {
		if ts[i].kind == tIdent && ts[i].text == key && ts[i+1].is("=") {
			return concat(ts[i+2:])
		}
	}
	if key == "value" && len(ts) > 0 && ts[0].kind == tString {
		s, ok := concat(ts)
		if ok {
			return s, true
		}
	}
	return "", false
}

// boolArg reads name = true / false.
func (a annotation) boolArg(key string) (bool, bool) {
	ts := a.args
	for i := 0; i+2 < len(ts); i++ {
		if ts[i].kind == tIdent && ts[i].text == key && ts[i+1].is("=") && ts[i+2].kind == tIdent {
			switch ts[i+2].text {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	}
	return false, false
}

func (a annotation) has(key string) bool {
	for i := 0; i+1 < len(a.args); i++ {
		if a.args[i].kind == tIdent && a.args[i].text == key && a.args[i+1].is("=") {
			return true
		}
	}
	return false
}

// concat joins "a" + "b" up to the next , or the end; ok=false when a
// non-literal takes part.
func concat(ts []token) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(ts); i++ {
		switch {
		case ts[i].kind == tString:
			b.WriteString(ts[i].text)
		case ts[i].is("+"):
		case ts[i].is(","), ts[i].is(")"):
			return b.String(), true
		default:
			return b.String(), false
		}
	}
	return b.String(), true
}

// readAnnotation reads @a.b.C or @C(args) at i; it returns the annotation
// and the index after it.
func readAnnotation(ts []token, i int) (annotation, int) {
	a := annotation{line: ts[i].line}
	j := i + 1
	for j < len(ts) && ts[j].kind == tIdent {
		a.name = ts[j].text
		j++
		if j < len(ts) && ts[j].is(".") && j+1 < len(ts) && ts[j+1].kind == tIdent {
			j++
			continue
		}
		break
	}
	if j < len(ts) && ts[j].is("(") {
		end := closing(ts, j)
		a.args = ts[j+1 : end]
		j = end + 1
	}
	return a, j
}

func source(p *project, f string, e *emitter) {
	mod := p.module(f)
	ts := lex(p.files[f], false)
	// Constants: NAME = "literal" followed by ; or a new line.
	consts := map[string]token{}
	for i := 1; i+1 < len(ts); i++ {
		if ts[i].is("=") && ts[i-1].kind == tIdent && ts[i+1].kind == tString &&
			(i+2 >= len(ts) || ts[i+2].is(";") || ts[i+2].line != ts[i+1].line) {
			consts[ts[i-1].text] = ts[i+1]
		}
	}
	usage := map[int]string{} // string token index -> strongest usage
	rank := map[string]int{"mention": 1, "default": 2, "request": 3}
	mark := func(i int, u string) {
		if rank[u] > rank[usage[i]] {
			usage[i] = u
		}
	}
	index := map[token]int{}
	for i, t := range ts {
		if t.kind == tString {
			index[t] = i
			mark(i, "mention")
		}
	}
	for n, t := range consts {
		if configuredURL(n) {
			mark(index[t], "default")
		}
	}
	// requestArg marks the first argument after ( at j as a request.
	requestArg := func(j int) {
		if j+1 >= len(ts) {
			return
		}
		a := ts[j+1]
		switch {
		case a.kind == tString:
			mark(j+1, "request")
		case a.kind == tIdent:
			if c, ok := consts[a.text]; ok {
				mark(index[c], "request")
				if h := hostname.FromURL(c.text); h != "" {
					e.host(h, "request", ev(f, a.line, mod))
				}
			}
		}
	}
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		switch {
		case t.is("@"):
			a, next := readAnnotation(ts, i)
			switch a.name {
			case "Value":
				if v, ok := a.arg("value"); ok {
					for _, n := range placeholders(v) {
						if envShaped(n) {
							e.env(n, "code", ev(f, a.line, mod))
						}
					}
				}
			case "FeignClient":
				if v, ok := a.arg("url"); ok {
					for _, n := range placeholders(v) {
						if envShaped(n) {
							e.env(n, "code", ev(f, a.line, mod))
						}
					}
					for k, at := range a.args {
						if at.is("url") && k+2 < len(a.args) && a.args[k+2].kind == tString {
							mark(index[a.args[k+2]], "request")
						}
					}
				}
			}
			i = next - 1
		case t.kind == tIdent && i+2 < len(ts) && ts[i+1].is("("):
			switch {
			case t.text == "getenv" && ts[i+2].kind == tString:
				if envShaped(ts[i+2].text) {
					e.env(ts[i+2].text, "code", ev(f, t.line, mod))
				}
			case requestMethods[t.text] && i > 0 && ts[i-1].is("."):
				requestArg(i + 1)
			case t.text == "create" && i > 1 && ts[i-1].is(".") && (ts[i-2].is("WebClient") || ts[i-2].is("RestClient")):
				requestArg(i + 1)
			}
		}
	}
	for i, t := range ts {
		if t.kind != tString || strings.Contains(t.text, "${") {
			continue
		}
		if h := hostname.FromURL(t.text); h != "" {
			e.host(h, usage[i], ev(f, t.line, mod))
		}
	}
	if strings.HasSuffix(f, ".java") {
		jpa(p, f, mod, ts, e)
	}
}
