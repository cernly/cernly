// Package nginxconf lexes and parses nginx configuration for the nginx plugin
// (routes) and the env plugin (the ${VAR} names an envsubst template reads).
// The syntax as nginx reads it: tokens
// separated by whitespace, a directive ends with ';', a block is { … }, "…" and
// '…' quote with backslash escapes, # starts a comment at the start of a token,
// and a ${…} inside a token is part of it (nginx's variable syntax), not a
// block.
package nginxconf

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/defaults"
)

// IsConfig reports a file the shipped defaults call nginx configuration
// (files.nginx, path patterns).
func IsConfig(p string) bool {
	for _, pat := range defaults.Get().Files.Nginx {
		if config.Matches(pat, p) {
			return true
		}
	}
	return false
}

type Token struct {
	Text   string
	Line   int
	Quoted bool
	File   string // set by the caller after lexing (includes mix files)
}

// Node is one directive: its name and arguments, and its block when it has one.
type Node struct {
	Args  []Token // Args[0] is the directive's name
	Block []Node
	Open  bool // has a block
}

func (n Node) Name() string {
	if len(n.Args) == 0 {
		return ""
	}
	return n.Args[0].Text
}

func (n Node) Line() int {
	if len(n.Args) == 0 {
		return 0
	}
	return n.Args[0].Line
}

// Values returns the arguments' texts after the directive's name.
func (n Node) Values() []string {
	var out []string
	for _, a := range n.Args[1:] {
		out = append(out, a.Text)
	}
	return out
}

type item struct {
	tok  Token
	kind byte // 'w' word, ';', '{', '}'
}

func lex(src []byte) ([]item, error) {
	s := string(src)
	var out []item
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == ';' || c == '{' || c == '}':
			out = append(out, item{tok: Token{Text: string(c), Line: line}, kind: c})
			i++
		case c == '"' || c == '\'':
			start := line
			var b strings.Builder
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					b.WriteByte(s[i+1])
					if s[i+1] == '\n' {
						line++
					}
					i += 2
					continue
				}
				if s[i] == c {
					closed = true
					i++
					break
				}
				if s[i] == '\n' {
					line++
				}
				b.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("line %d: unterminated quote", start)
			}
			out = append(out, item{tok: Token{Text: b.String(), Line: start, Quoted: true}, kind: 'w'})
		default:
			var b strings.Builder
			for i < len(s) {
				d := s[i]
				if d == '$' && i+1 < len(s) && s[i+1] == '{' {
					end := strings.IndexByte(s[i:], '}')
					if end < 0 {
						return nil, fmt.Errorf("line %d: ${ not closed", line)
					}
					b.WriteString(s[i : i+end+1])
					i += end + 1
					continue
				}
				if d == '\\' && i+1 < len(s) {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if strings.IndexByte(" \t\r\n;{}\"'", d) >= 0 {
					break
				}
				b.WriteByte(d)
				i++
			}
			out = append(out, item{tok: Token{Text: b.String(), Line: line}, kind: 'w'})
		}
	}
	return out, nil
}

// ParseFile lexes and parses one file; every token records the file.
func ParseFile(file string, src []byte) ([]Node, error) {
	items, err := lex(src)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].tok.File = file
	}
	pos := 0
	return parse(items, &pos, 0)
}

// maxDepth caps block nesting so a crafted file cannot overflow the stack.
const maxDepth = 1000

func parse(items []item, pos *int, depth int) ([]Node, error) {
	var out []Node
	var cur Node
	for *pos < len(items) {
		it := items[*pos]
		*pos++
		switch it.kind {
		case ';':
			if len(cur.Args) == 0 {
				return nil, fmt.Errorf("line %d: unexpected ;", it.tok.Line)
			}
			out = append(out, cur)
			cur = Node{}
		case '{':
			if len(cur.Args) == 0 {
				return nil, fmt.Errorf("line %d: a block without a directive", it.tok.Line)
			}
			if depth >= maxDepth {
				return nil, fmt.Errorf("line %d: blocks nested more than %d deep", it.tok.Line, maxDepth)
			}
			block, err := parse(items, pos, depth+1)
			if err != nil {
				return nil, err
			}
			cur.Block, cur.Open = block, true
			out = append(out, cur)
			cur = Node{}
		case '}':
			if depth == 0 {
				return nil, fmt.Errorf("line %d: unexpected }", it.tok.Line)
			}
			if len(cur.Args) > 0 {
				return nil, fmt.Errorf("line %d: directive %s has no ;", cur.Line(), cur.Name())
			}
			return out, nil
		default:
			cur.Args = append(cur.Args, it.tok)
		}
	}
	if depth > 0 {
		return nil, fmt.Errorf("a block is not closed")
	}
	if len(cur.Args) > 0 {
		return nil, fmt.Errorf("line %d: directive %s has no ;", cur.Line(), cur.Name())
	}
	return out, nil
}

// Env is an environment variable a template reads, and its line.
type Env struct {
	Name string
	Line int
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// EnvNames returns the ${VAR} names of an envsubst template (the official
// nginx image renders /etc/nginx/templates/*.template at start). Only the
// braced form is read: $host and the like are nginx's own variables. Comments
// are not tokens, so a name in a comment is not read.
func EnvNames(src []byte) ([]Env, error) {
	items, err := lex(src)
	if err != nil {
		return nil, err
	}
	var out []Env
	for _, it := range items {
		for _, m := range envRe.FindAllStringSubmatch(it.tok.Text, -1) {
			out = append(out, Env{Name: m[1], Line: it.tok.Line})
		}
	}
	return out, nil
}
