// Package pyread reads Python source for the env and outbound plugins: the
// environment variable names the code reads and the public hosts it calls, is
// configured for, or mentions. A tokenizer that follows Python's lexical
// grammar (lex.go) and patterns on its token stream, as jsread does for
// TypeScript; the pure-Go Python parsers stop at Python 3.4. Nothing is
// executed or imported. See https://cernly.dev/how/plugins/.
package pyread

import (
	"path"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/internal/hostname"
	"github.com/cernly/cernly/repo"
)

// Usage grades, as for Go and TypeScript.
const (
	Mention = "mention"
	Default = "default"
	Request = "request"
)

type Env struct {
	Name string
	Line int
	How  string // "" for a direct read; "settings" for a pydantic BaseSettings field
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

// Source reports the files the reader takes: Python sources, not tests.
func Source(p string) bool {
	base := path.Base(p)
	if path.Ext(base) != ".py" && path.Ext(base) != ".pyw" {
		return false
	}
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py" {
		return false
	}
	for _, d := range []string{"tests", "test", "testing"} {
		if strings.Contains("/"+p, "/"+d+"/") {
			return false
		}
	}
	return true
}

// Manifest reports a Python manifest file: pyproject.toml,
// requirements*.txt, setup.py or setup.cfg.
func Manifest(p string) bool {
	b := path.Base(p)
	return b == "pyproject.toml" || b == "setup.py" || b == "setup.cfg" || (strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt"))
}

// Units is the set of folders that hold a Python manifest.
func Units(files []string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if Manifest(f) {
			out[path.Dir(f)] = true
		}
	}
	return out
}

// Unit is the folder of the nearest Python manifest, the unit deps-python
// uses, or ".".
func Unit(units map[string]bool, file string) string {
	for d := path.Dir(file); ; d = path.Dir(d) {
		if units[d] {
			return d
		}
		if d == "." || d == "/" {
			return "."
		}
	}
}

// Read reads one file. The result is cached for the scan by path and content,
// since the env and outbound plugins both read every file.
func Read(r *repo.Repo, file string, src []byte) (Result, error) {
	return repo.MemoFile(r, file, src, func() (Result, error) { return read(src) })
}

func read(src []byte) (Result, error) {
	var res Result
	toks, err := lex(string(src))
	if err == nil {
		res = match(toks)
	}
	return res, err
}

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true,
	"options": true, "request": true, "stream": true, "urlopen": true}

// hostOf reads a URL literal's public host, up to its first placeholder.
func hostOf(s string) string {
	if i := strings.IndexAny(s, "%{"); i >= 0 {
		s = s[:i]
	}
	return hostname.FromURL(s)
}

func match(t []token) Result {
	var res Result
	at := func(i int) token {
		if i >= 0 && i < len(t) {
			return t[i]
		}
		return token{kind: -1}
	}
	is := func(i int, s string) bool { k := at(i); return (k.kind == tName || k.kind == tOp) && k.text == s }
	str := func(i int) (string, bool) {
		k := at(i)
		return k.text, k.kind == tString || k.kind == tFString
	}
	cfg := defaults.Get()

	// Names bound by imports: environ, getenv, urlopen, httpx, requests …
	envNames := map[string]bool{}    // environ, or its alias
	getenvNames := map[string]bool{} // getenv, or its alias
	urlopenNames := map[string]bool{}
	for i := range t {
		if is(i, "from") && (is(i+1, "os") || (is(i+1, "urllib") && is(i+2, ".") && is(i+3, "request"))) {
			mod := at(i + 1).text
			j := i + 2
			for j < len(t) && !is(j, "import") && !at(j).first {
				j++
			}
			for j++; j < len(t) && !at(j).first; j++ {
				if at(j).kind != tName || at(j).text == "as" {
					continue
				}
				name, bound := at(j).text, at(j).text
				if is(j+1, "as") && at(j+2).kind == tName {
					bound = at(j + 2).text
				}
				switch {
				case mod == "os" && name == "environ":
					envNames[bound] = true
				case mod == "os" && name == "getenv":
					getenvNames[bound] = true
				case mod == "urllib" && (name == "urlopen" || name == "Request"):
					urlopenNames[bound] = true
				}
			}
		}
	}

	// os.environ[...], os.environ.get(...), os.getenv(...) and the bare names.
	envAt := func(i int) (int, bool) { // the index of the name string, when t[i] starts an env read
		switch {
		case is(i, "os") && is(i+1, ".") && is(i+2, "environ") && !is(i-1, "."):
			return envArg(i+3, is)
		case is(i, "os") && is(i+1, ".") && is(i+2, "getenv") && is(i+3, "(") && !is(i-1, "."):
			return i + 4, true
		case at(i).kind == tName && envNames[at(i).text] && !is(i-1, "."):
			return envArg(i+1, is)
		case at(i).kind == tName && getenvNames[at(i).text] && is(i+1, "(") && !is(i-1, "."):
			return i + 2, true
		}
		return 0, false
	}

	// Names bound to a URL: NAME = "…" | f"…" | NAME2 + … | os.getenv("X", "https://default").
	names := map[string]string{}
	clients := map[string]bool{}
	valueAt := func(i int) (string, bool) {
		if s, ok := str(i); ok {
			if h := hostOf(s); h != "" {
				return h, true
			}
			if at(i).kind == tFString && s == "" && at(i+1).kind == tName {
				h, ok := names[at(i+1).text] // f"{BASE}/x"
				return h, ok
			}
			return "", false
		}
		if j, ok := envAt(i); ok {
			// An env read with a default: the default's host.
			for k := j + 1; k < j+4 && k < len(t); k++ {
				if is(k, ",") {
					if s, ok := str(k + 1); ok {
						h := hostOf(s)
						return h, h != ""
					}
				}
				if is(k, ")") {
					break
				}
			}
			return "", false
		}
		if at(i).kind == tName {
			h, ok := names[at(i).text]
			return h, ok
		}
		return "", false
	}
	for i := range t {
		// NAME = value, NAME: annotation = value
		if at(i).kind != tName || !at(i).first {
			continue
		}
		j := i + 1
		if is(j, ":") {
			for j < len(t) && !is(j, "=") && !at(j).first {
				j++
			}
		}
		if !is(j, "=") {
			continue
		}
		name := at(i).text
		if h, ok := valueAt(j + 1); ok {
			names[name] = h
			if cfg.ConfigName(name) {
				res.Hosts = append(res.Hosts, Host{Host: h, Usage: Default, Line: at(j + 1).line})
			}
		}
		if isClientCtor(j+1, is) {
			clients[name] = true
		}
	}
	// with httpx.Client() as NAME, async with aiohttp.ClientSession() as NAME
	for i := range t {
		if is(i, "with") && isClientCtor(i+1, is) {
			depth := 0
			for j := i + 1; j < len(t) && !(at(j).first); j++ {
				switch at(j).text {
				case "(", "[":
					depth++
				case ")", "]":
					depth--
				}
				if depth == 0 && is(j, "as") && at(j+1).kind == tName {
					clients[at(j+1).text] = true
					break
				}
			}
		}
	}

	for i := range t {
		if j, ok := envAt(i); ok {
			if s, ok := str(j); ok && at(j).kind == tString {
				res.Env = append(res.Env, Env{Name: s, Line: at(j).line})
			}
		}
		if s, ok := str(i); ok {
			if h := hostOf(s); h != "" {
				res.Hosts = append(res.Hosts, Host{Host: h, Usage: Mention, Line: at(i).line})
			}
		}
		// Requests: httpx.get(…), requests.post(…), client.get(…), urlopen(…)
		arg := -1
		switch {
		case (is(i, "httpx") || is(i, "requests")) && is(i+1, ".") && httpMethods[at(i+2).text] && is(i+3, "(") && !is(i-1, "."):
			arg = i + 4
			if at(i+2).text == "request" || at(i+2).text == "stream" {
				arg = nextArg(t, arg)
			}
		case at(i).kind == tName && clients[at(i).text] && is(i+1, ".") && httpMethods[at(i+2).text] && is(i+3, "("):
			arg = i + 4
			if at(i+2).text == "request" || at(i+2).text == "stream" {
				arg = nextArg(t, arg)
			}
		case is(i, "urllib") && is(i+1, ".") && is(i+2, "request") && is(i+3, ".") && (is(i+4, "urlopen") || is(i+4, "Request")) && is(i+5, "("):
			arg = i + 6
		case at(i).kind == tName && urlopenNames[at(i).text] && is(i+1, "(") && !is(i-1, "."):
			arg = i + 2
		}
		if arg >= 0 && arg < len(t) {
			if h, ok := valueAt(arg); ok {
				res.Hosts = append(res.Hosts, Host{Host: h, Usage: Request, Line: at(arg).line})
			}
		}
	}

	res.Env = append(res.Env, settingsFields(t, is)...)
	return res
}

// envArg returns the index of the name string after environ: [ "X" ],
// .get( "X", .setdefault( "X", .pop( "X".
func envArg(i int, is func(int, string) bool) (int, bool) {
	switch {
	case is(i, "["):
		return i + 1, true
	case is(i, ".") && (is(i+1, "get") || is(i+1, "setdefault") || is(i+1, "pop")) && is(i+2, "("):
		return i + 3, true
	}
	return 0, false
}

// nextArg returns the index after the first argument at i (for request(method, url)).
func nextArg(t []token, i int) int {
	depth := 0
	for j := i; j < len(t); j++ {
		switch t[j].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return -1
			}
			depth--
		case ",":
			if depth == 0 && t[j].kind == tOp {
				return j + 1
			}
		}
	}
	return -1
}

// isClientCtor reports httpx.Client(…), httpx.AsyncClient(…),
// requests.Session(…), aiohttp.ClientSession(…) at i.
func isClientCtor(i int, is func(int, string) bool) bool {
	switch {
	case is(i, "httpx") && is(i+1, ".") && (is(i+2, "Client") || is(i+2, "AsyncClient")) && is(i+3, "("):
		return true
	case is(i, "requests") && is(i+1, ".") && is(i+2, "Session") && is(i+3, "("):
		return true
	case is(i, "aiohttp") && is(i+1, ".") && is(i+2, "ClientSession") && is(i+3, "("):
		return true
	}
	return false
}

// settingsFields reads pydantic BaseSettings classes: each field annotated in
// the class body is read from the environment as env_prefix + its name, upper
// case (pydantic-settings' documented default, case-insensitive). Fields with
// an alias are left out: the alias decides their name.
func settingsFields(t []token, is func(int, string) bool) []Env {
	var out []Env
	for i := range t {
		if !is(i, "class") || !t[i].first || i+2 >= len(t) || !is(i+2, "(") {
			continue
		}
		base := false
		j := i + 3
		for ; j < len(t) && !is(j, ")"); j++ {
			if t[j].kind == tName && t[j].text == "BaseSettings" {
				base = true
			}
		}
		if !base || j >= len(t) {
			continue // not settings, or the base list is never closed
		}
		col := t[i].col
		end := j + 1
		for end < len(t) && !(t[end].first && t[end].col <= col) {
			end++
		}
		body := t[j+1 : end]
		prefix := ""
		for k := range body {
			if body[k].kind == tName && body[k].text == "env_prefix" && k+2 < len(body) && (body[k+1].text == "=" || body[k+1].text == ":") {
				for m := k + 2; m < len(body) && m < k+5; m++ {
					if body[m].kind == tString {
						prefix = body[m].text
						break
					}
				}
			}
		}
		fieldCol := -1
		for k := range body {
			b := body[k]
			if !b.first || b.kind != tName {
				continue
			}
			if fieldCol < 0 {
				fieldCol = b.col
			}
			if b.col != fieldCol || k+1 >= len(body) || body[k+1].text != ":" {
				continue
			}
			if strings.HasPrefix(b.text, "_") || b.text == "model_config" {
				continue
			}
			aliased := false
			for m := k + 2; m < len(body) && !body[m].first; m++ {
				if body[m].kind == tName && (body[m].text == "alias" || body[m].text == "validation_alias") {
					aliased = true
				}
			}
			if !aliased {
				out = append(out, Env{Name: strings.ToUpper(prefix + b.text), Line: b.line, How: "settings"})
			}
		}
	}
	return out
}
