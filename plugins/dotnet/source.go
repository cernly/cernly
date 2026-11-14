package dotnet

// Env vars and outbound hosts from C#, in the shapes the java
// plugin emits: env_var with origin code, outbound_host via literal_url with
// usage request, default or mention. Test projects give neither.

import (
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/envname"
	"github.com/cernly/cernly/internal/hostname"
	"github.com/cernly/cernly/plugin"
)

// requestMethods are the HttpClient methods (and the System.Net.Http.Json
// extensions) whose first argument is the URL they call.
var requestMethods = map[string]bool{
	"GetAsync": true, "PostAsync": true, "PutAsync": true, "PatchAsync": true, "DeleteAsync": true,
	"GetStringAsync": true, "GetByteArrayAsync": true, "GetStreamAsync": true,
	"GetFromJsonAsync": true, "PostAsJsonAsync": true, "PutAsJsonAsync": true, "PatchAsJsonAsync": true,
	"DeleteFromJsonAsync": true,
}

// emitter collects env and outbound facts before they are merged per key.
type emitter struct{ facts []fact.Fact }

func (e *emitter) env(n string, ev fact.Evidence) {
	e.facts = append(e.facts, fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + n, Plugin: "dotnet",
		Attrs: map[string]any{"name": n, "origins": []any{"code"}}, Evidence: []fact.Evidence{ev}})
}

func (e *emitter) host(h, usage string, ev fact.Evidence) {
	e.facts = append(e.facts, fact.Fact{Kind: fact.KindOutboundHost, Key: "outbound_host:" + h, Plugin: "dotnet",
		Attrs: map[string]any{"host": h, "via": "literal_url", "usage": usage}, Evidence: []fact.Evidence{ev}})
}

// merged folds facts with one key into one, as java does: evidence joined and
// sorted, env origins united, the strongest outbound usage kept.
func (e *emitter) merged() []fact.Fact {
	rank := map[string]int{"mention": 1, "default": 2, "request": 3}
	idx := map[string]int{}
	var out []fact.Fact
	for _, f := range e.facts {
		id := f.Kind + "\x00" + f.Key
		i, ok := idx[id]
		if !ok {
			idx[id] = len(out)
			out = append(out, f)
			continue
		}
		o := &out[i]
		o.Evidence = append(o.Evidence, f.Evidence...)
		if f.Kind == fact.KindOutboundHost && rank[f.Attrs["usage"].(string)] > rank[o.Attrs["usage"].(string)] {
			o.Attrs["usage"] = f.Attrs["usage"]
		}
	}
	for i := range out {
		sort.SliceStable(out[i].Evidence, func(a, b int) bool {
			x, y := out[i].Evidence[a], out[i].Evidence[b]
			if x.Path != y.Path {
				return x.Path < y.Path
			}
			return x.Line < y.Line
		})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Kind != out[b].Kind {
			return out[a].Kind < out[b].Kind
		}
		return out[a].Key < out[b].Key
	})
	return out
}

// envOutbound reads C# sources and appsettings files of non-test projects.
func envOutbound(s *state, res *plugin.Result) {
	var e emitter
	for _, dir := range slices.Sorted(maps.Keys(s.sources)) {
		if s.testDir(dir) {
			continue
		}
		s.csharpEnvOutbound(dir, &e)
	}
	res.Errors = append(res.Errors, s.appSettings(&e)...)
	res.Facts = append(res.Facts, e.merged()...)
}

// testDir reports a folder that holds a test project.
func (s *state) testDir(dir string) bool {
	for _, p := range s.projects {
		if p.Dir == dir && p.IsTest {
			return true
		}
	}
	return false
}

// legacyDir reports a folder whose projects are all non-SDK-style.
func (s *state) legacyDir(dir string) bool {
	legacy := false
	for _, p := range s.projects {
		if p.Dir == dir {
			if p.SDKStyle {
				return false
			}
			legacy = true
		}
	}
	return legacy
}

// projectDir is the nearest folder above f that holds a project, if any.
func (s *state) projectDir(f string) (string, bool) {
	for d := path.Dir(f); ; d = path.Dir(d) {
		if s.dirs[d] {
			return d, true
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}

// csharpEnvOutbound reads the .cs files of one project folder. Constants are
// shared across its files, by bare name and as Class.Name; a name declared
// with two values resolves to none.
func (s *state) csharpEnvOutbound(dir string, e *emitter) {
	files := map[string][]token{}
	consts := map[string]string{}
	clash := map[string]bool{}
	var paths []string
	for _, f := range s.sources[dir] {
		if strings.ToLower(path.Ext(f)) != ".cs" {
			continue
		}
		toks, err := s.tokens(f)
		if err != nil {
			continue
		}
		files[f] = toks
		paths = append(paths, f)
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
	}
	for k := range clash {
		delete(consts, k)
	}
	for _, f := range paths {
		csharpFile(files[f], consts, func(line int) fact.Evidence { return fact.Evidence{Path: f, Line: line, Unit: dir} }, e)
	}
}

// classConsts calls fn for every `const string X = "…";` declared directly in
// a class, struct, record or interface body, with that type's name.
func classConsts(toks []token, fn func(class, name, val string)) {
	type open struct {
		name  string
		depth int
	}
	var stack []open
	depth, pending := 0, ""
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case (isID(t, "class") || isID(t, "struct") || isID(t, "record") || isID(t, "interface")) && i+1 < len(toks) && toks[i+1].kind == tokIdent:
			pending = toks[i+1].text
		case isP(t, "{"):
			depth++
			if pending != "" {
				stack = append(stack, open{pending, depth})
				pending = ""
			}
		case isP(t, "}"):
			if n := len(stack); n > 0 && stack[n-1].depth == depth {
				stack = stack[:n-1]
			}
			depth--
		case isP(t, ";"):
			pending = ""
		case isID(t, "const") && len(stack) > 0 && stack[len(stack)-1].depth == depth && i+5 < len(toks) &&
			isID(toks[i+1], "string") && toks[i+2].kind == tokIdent && isP(toks[i+3], "=") &&
			toks[i+4].kind == tokString && toks[i+4].literal && isP(toks[i+5], ";"):
			fn(stack[len(stack)-1].name, toks[i+2].text, toks[i+4].text)
		}
	}
}

// csValue is a literal string, or a name that a const string of the project
// holds: K by its bare name, Keys.K only as K declared in a class Keys.
func csValue(a []token, consts map[string]string) (string, bool) {
	if len(a) == 1 && a[0].kind == tokString && a[0].literal {
		return a[0].text, true
	}
	name, j := dotted(a, 0)
	if name == "" || j != len(a) {
		return "", false
	}
	if k := strings.LastIndexByte(name, '.'); k >= 0 {
		q := name[:k]
		name = q[strings.LastIndexByte(q, '.')+1:] + name[k:]
	}
	v, ok := consts[name]
	return v, ok
}

// newUri reads `new Uri(x, …)` and `new System.Uri(x, …)` at a[0].
func newUri(a []token) ([]token, bool) {
	if len(a) < 4 || !isID(a[0], "new") {
		return nil, false
	}
	name, j := dotted(a, 1)
	if (name != "Uri" && name != "System.Uri") || j >= len(a) || !isP(a[j], "(") {
		return nil, false
	}
	end := closing(a, j, "(", ")")
	if end < 0 {
		return nil, false
	}
	args := splitTop(a[j+1 : end])
	if len(args) == 0 {
		return nil, false
	}
	return args[0], true
}

// constURL is the convention for constants that hold a URL the app calls: the
// spec's suffixes and the shipped config-name suffixes, any case.
func constURL(name string) bool {
	return hasSuffixFold(name, "url", "uri", "endpoint") || defaults.Get().ConfigName(name)
}

func hasSuffixFold(name string, suffixes ...string) bool {
	n := strings.ToLower(name)
	for _, s := range suffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// csharpFile adds the env vars and hosts of one lexed file. A literal URL
// used by a request is request, one held by a *Url constant default, any
// other public URL literal mention. Interpolated strings with holes and
// comments are never read.
func csharpFile(toks []token, consts map[string]string, ev func(int) fact.Evidence, e *emitter) {
	marked := map[int]bool{}
	// request marks the argument a's literal, or reports the const it names.
	request := func(a []token) {
		if inner, ok := newUri(a); ok {
			a = inner
		}
		if len(a) == 0 {
			return
		}
		v, ok := csValue(a, consts)
		if !ok {
			return
		}
		if h := hostname.FromURL(v); h != "" {
			e.host(h, "request", ev(a[0].line))
		}
		if len(a) == 1 && a[0].kind == tokString {
			marked[cap(toks)-cap(a)] = true // a is a subslice of toks
		}
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokIdent {
			continue
		}
		switch {
		case t.text == "GetEnvironmentVariable" && i+1 < len(toks) && isP(toks[i+1], "("):
			end := closing(toks, i+1, "(", ")")
			if end < 0 {
				continue
			}
			if args := splitTop(toks[i+2 : end]); len(args) > 0 {
				if n, ok := csValue(args[0], consts); ok && envname.Valid(n) {
					e.env(n, ev(t.line))
				}
			}
		case t.text == "BaseAddress" && i+2 < len(toks) && isP(toks[i+1], "=") && isID(toks[i+2], "new"):
			end := -1
			for j := i + 2; j < len(toks); j++ {
				if isP(toks[j], "(") {
					end = closing(toks, j, "(", ")") + 1
					break
				}
			}
			if end <= 0 {
				continue // never opens, or never closes
			}
			if _, ok := newUri(toks[i+2 : end]); ok {
				request(toks[i+2 : end])
			}
		case requestMethods[t.text] && i > 0 && isP(toks[i-1], ".") && i+1 < len(toks) && isP(toks[i+1], "("):
			if end := closing(toks, i+1, "(", ")"); end >= 0 {
				if args := splitTop(toks[i+2 : end]); len(args) > 0 {
					request(args[0])
				}
			}
		case t.text == "new" && i+2 < len(toks):
			name, j := dotted(toks, i+1)
			if (name == "HttpRequestMessage" || name == "System.Net.Http.HttpRequestMessage") && j < len(toks) && isP(toks[j], "(") {
				if end := closing(toks, j, "(", ")"); end >= 0 {
					if args := splitTop(toks[j+1 : end]); len(args) > 1 {
						request(args[1])
					}
				}
			}
		}
	}
	for i := 0; i+5 < len(toks); i++ {
		if isID(toks[i], "const") && isID(toks[i+1], "string") && toks[i+2].kind == tokIdent && isP(toks[i+3], "=") &&
			toks[i+4].kind == tokString && toks[i+4].literal && isP(toks[i+5], ";") && constURL(toks[i+2].text) && !marked[i+4] {
			if h := hostname.FromURL(toks[i+4].text); h != "" {
				e.host(h, "default", ev(toks[i+4].line))
			}
			marked[i+4] = true
		}
	}
	for i, t := range toks {
		if t.kind != tokString || !t.literal || marked[i] {
			continue
		}
		if h := hostname.FromURL(t.text); h != "" {
			e.host(h, "mention", ev(t.line))
		}
	}
}
