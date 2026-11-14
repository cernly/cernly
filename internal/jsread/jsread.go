// Package jsread reads TypeScript and JavaScript files for the env and
// outbound plugins: the environment variable names the code reads and the
// public hosts it calls, is configured for, or mentions.
//
// esbuild turns TS, TSX and JSX into plain JavaScript (types and JSX gone)
// with a source map; tdewolff's JavaScript lexer splits that into tokens
// (strings, template literals, comments and regular expressions handled as
// the language defines them); the patterns below are matched on the token
// stream; the source map takes each match back to its line in the original
// file. Nothing is executed. tdewolff's syntax tree has no positions, which
// is why the reader works on tokens. See https://cernly.dev/how/plugins/.
package jsread

import (
	"fmt"
	"path"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/internal/hostname"
	"github.com/cernly/cernly/repo"
)

// Usage grades, as outbound-go has them.
const (
	Mention = "mention"
	Default = "default"
	Request = "request"
)

type Env struct {
	Name string
	Line int
}

type Host struct {
	Host  string
	Usage string
	Line  int
}

type Result struct {
	Env   []Env
	Hosts []Host
}

// Source reports the files the reader takes: TypeScript and JavaScript
// sources, not declaration files, tests or minified bundles.
func Source(p string) bool {
	base := path.Base(p)
	switch path.Ext(base) {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
	default:
		return false
	}
	for _, s := range []string{".d.ts", ".d.mts", ".d.cts", ".min.js"} {
		if strings.HasSuffix(base, s) {
			return false
		}
	}
	for _, s := range []string{".test.", ".spec."} {
		if strings.Contains(base, s) {
			return false
		}
	}
	for _, d := range []string{"__tests__", "__mocks__", "e2e", "tests"} {
		if strings.Contains("/"+p, "/"+d+"/") {
			return false
		}
	}
	return true
}

// Read reads one file. The result is cached for the scan by path and content,
// since the env and outbound plugins both read every file.
func Read(r *repo.Repo, file string, src []byte) (Result, error) {
	return repo.MemoFile(r, file, src, func() (Result, error) { return read(file, src) })
}

func loader(file string) api.Loader {
	switch path.Ext(file) {
	case ".ts", ".mts", ".cts":
		return api.LoaderTS
	case ".tsx":
		return api.LoaderTSX
	case ".jsx":
		return api.LoaderJSX
	}
	return api.LoaderJSX // plain JavaScript, JSX allowed: many projects put JSX in .js
}

func read(file string, src []byte) (Result, error) {
	out := api.Transform(string(src), api.TransformOptions{
		Loader:         loader(file),
		Sourcefile:     file,
		Sourcemap:      api.SourceMapExternal,
		SourcesContent: api.SourcesContentExclude,
		Target:         api.ESNext,
		JSX:            api.JSXTransform,
		LogLevel:       api.LogLevelSilent,
	})
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		line := 0
		if e.Location != nil {
			line = e.Location.Line
		}
		return Result{}, fmt.Errorf("line %d: %s", line, e.Text)
	}
	sm, err := parseSourceMap(out.Map)
	if err != nil {
		return Result{}, err
	}
	toks := tokens(out.Code)
	lineOf := lineIndex(out.Code)
	orig := func(off int) int {
		gl, gc := lineOf(off)
		return sm.original(gl, gc)
	}
	return match(toks, orig), nil
}

type token struct {
	tt   js.TokenType
	text string
	off  int // start offset in the transformed code
}

// tokens lexes JavaScript, skipping whitespace and comments. A / starts a
// regular expression where an expression may begin, a division elsewhere,
// decided by the previous token as the language grammar does.
func tokens(code []byte) []token {
	in := parse.NewInputBytes(code)
	l := js.NewLexer(in)
	var out []token
	for {
		tt, text := l.Next()
		if tt == js.ErrorToken {
			return out
		}
		switch tt {
		case js.WhitespaceToken, js.LineTerminatorToken, js.CommentToken, js.CommentLineTerminatorToken:
			continue
		case js.DivToken, js.DivEqToken:
			if regexAllowed(out) {
				if rt, rtext := l.RegExp(); rt == js.RegExpToken {
					tt, text = rt, rtext
				}
			}
		}
		out = append(out, token{tt: tt, text: string(text), off: in.Offset() - len(text)})
	}
}

var exprKeywords = map[string]bool{"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true}

func regexAllowed(prev []token) bool {
	if len(prev) == 0 {
		return true
	}
	p := prev[len(prev)-1]
	switch {
	case p.text == ")" || p.text == "]" || p.text == "}":
		return false
	case js.IsIdentifierName(p.tt):
		return exprKeywords[p.text]
	case js.IsNumeric(p.tt) || p.tt == js.StringToken || p.tt == js.TemplateToken || p.tt == js.TemplateEndToken || p.tt == js.RegExpToken:
		return false
	}
	return true
}

// literal returns the text a string or template token starts with: the whole
// string, or a template's text before its first ${.
func literal(t token) (string, bool) {
	switch t.tt {
	case js.StringToken:
		if len(t.text) >= 2 {
			return unescape(t.text[1 : len(t.text)-1]), true
		}
	case js.TemplateToken, js.TemplateStartToken:
		s := strings.TrimPrefix(t.text, "`")
		s = strings.TrimSuffix(strings.TrimSuffix(s, "`"), "${")
		return unescape(s), true
	}
	return "", false
}

func unescape(s string) string { return strings.ReplaceAll(s, `\/`, `/`) }

// hostOfLiteral reads a URL literal's public host, up to its first
// placeholder.
func hostOfLiteral(s string) string {
	if i := strings.Index(s, "%"); i >= 0 {
		s = s[:i]
	}
	return hostname.FromURL(s)
}

var requestMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true,
	"options": true, "request": true}

// match finds env reads and hosts in a file's tokens.
func match(t []token, line func(int) int) Result {
	var res Result
	at := func(i int) token {
		if i >= 0 && i < len(t) {
			return t[i]
		}
		return token{}
	}
	is := func(i int, s string) bool { return at(i).text == s }
	cfg := defaults.Get()

	// Names that hold a URL: const|let|var NAME = "…" | `…` | new URL("…").
	names := map[string]string{}
	valueAt := func(i int) (string, bool) {
		if s, ok := literal(at(i)); ok {
			if h := hostOfLiteral(s); h != "" {
				return h, true
			}
			if at(i).tt == js.TemplateStartToken && s == "" && js.IsIdentifierName(at(i+1).tt) {
				h, ok := names[at(i+1).text] // `${API_URL}/x`
				return h, ok
			}
			return "", false
		}
		if is(i, "new") && is(i+1, "URL") && is(i+2, "(") {
			if s, ok := literal(at(i + 3)); ok {
				h := hostOfLiteral(s)
				return h, h != ""
			}
		}
		if js.IsIdentifierName(at(i).tt) {
			h, ok := names[at(i).text]
			return h, ok
		}
		return "", false
	}
	for i := range t {
		if (is(i, "const") || is(i, "let") || is(i, "var")) && js.IsIdentifierName(at(i+1).tt) && is(i+2, "=") {
			if h, ok := valueAt(i + 3); ok {
				names[at(i+1).text] = h
				if cfg.ConfigName(at(i + 1).text) {
					res.Hosts = append(res.Hosts, Host{Host: h, Usage: Default, Line: line(at(i + 3).off)})
				}
			}
		}
	}

	for i := range t {
		// process.env.X, process.env["X"], import.meta.env.X
		if is(i, "process") && is(i+1, ".") && is(i+2, "env") && !is(i-1, ".") {
			switch {
			case is(i+3, ".") && js.IsIdentifierName(at(i+4).tt):
				res.Env = append(res.Env, Env{Name: at(i + 4).text, Line: line(at(i + 4).off)})
			case is(i+3, "[") && at(i+4).tt == js.StringToken && is(i+5, "]"):
				if s, ok := literal(at(i + 4)); ok {
					res.Env = append(res.Env, Env{Name: s, Line: line(at(i + 4).off)})
				}
			case is(i-1, "=") && is(i-2, "}"):
				res.Env = append(res.Env, destructured(t, i-2, line)...)
			}
		}
		if is(i, "import") && is(i+1, ".") && is(i+2, "meta") && is(i+3, ".") && is(i+4, "env") && is(i+5, ".") && js.IsIdentifierName(at(i+6).tt) {
			res.Env = append(res.Env, Env{Name: at(i + 6).text, Line: line(at(i + 6).off)})
		}
		// Every literal with a public host is at least a mention.
		if s, ok := literal(at(i)); ok {
			if h := hostOfLiteral(s); h != "" {
				res.Hosts = append(res.Hosts, Host{Host: h, Usage: Mention, Line: line(at(i).off)})
			}
		}
		// Requests: fetch(…), window.fetch(…), axios.get(…), axios(…)
		arg := -1
		switch {
		case is(i, "fetch") && is(i+1, "(") && (!is(i-1, ".") || is(i-2, "window") || is(i-2, "globalThis") || is(i-2, "self")):
			arg = i + 2
		case is(i, "axios") && is(i+1, ".") && requestMethods[at(i+2).text] && is(i+3, "("):
			arg = i + 4
		case is(i, "axios") && is(i+1, "(") && !is(i-1, "."):
			arg = i + 2
		}
		if arg >= 0 {
			if h, ok := valueAt(arg); ok {
				res.Hosts = append(res.Hosts, Host{Host: h, Usage: Request, Line: line(at(arg).off)})
			}
		}
	}
	return res
}

// destructured reads const { A, B: b, C = 1 } = process.env: the keys of the
// object pattern that closes at index end.
func destructured(t []token, end int, line func(int) int) []Env {
	depth, start := 0, -1
	for j := end; j >= 0; j-- {
		switch t[j].text {
		case "}":
			depth++
		case "{":
			depth--
		}
		if depth == 0 {
			start = j
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []Env
	depth = 0
	for j := start; j <= end; j++ {
		switch t[j].text {
		case "{", "[", "(":
			depth++
			continue
		case "}", "]", ")":
			depth--
			continue
		}
		prev := t[j-1].text
		if depth == 1 && js.IsIdentifierName(t[j].tt) && (prev == "{" || prev == ",") && t[j].text != "..." {
			out = append(out, Env{Name: t[j].text, Line: line(t[j].off)})
		}
	}
	return out
}

// Package returns the folder of the nearest package.json, the unit deps-npm
// uses, or "." when there is none.
func Package(has func(string) bool, file string) string {
	for d := path.Dir(file); ; d = path.Dir(d) {
		if has(path.Join(d, "package.json")) {
			return d
		}
		if d == "." || d == "/" {
			return "."
		}
	}
}
