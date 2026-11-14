// Package caddyfile lexes and parses Caddyfiles for the caddy plugin (routes)
// and the env plugin (the {$VAR} names a Caddyfile reads),
// https://cernly.dev/how/plugins/.
package caddyfile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cernly/cernly/fact"
)

// The Caddyfile grammar as Caddy documents it: tokens separated by
// whitespace, a newline ends a directive, "…" quotes (\" inside) and `…`
// quotes (raw), # starts a comment at the start of a token, <<MARKER opens a
// heredoc, a lone { opens a block at the end of a line and a lone } closes
// it. {$VAR} environment placeholders are kept as written: Caddy replaces
// them before lexing, which needs the deployment's environment.

type Token struct {
	Text   string
	Line   int
	Quoted bool   // written in quotes: never a block delimiter
	Body   int    // a heredoc's first body line; Line is its <<MARKER line
	File   string // the file it was read from, set after lexing (imports mix files)
}

func Lex(src []byte) ([]Token, error) {
	var out []Token
	s := string(src)
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			out = append(out, Token{Text: "\n", Line: line})
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			line++ // line continuation
			i += 2
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '"' || c == '`':
			start := line
			var b strings.Builder
			i++
			closed := false
			for i < len(s) {
				if c == '"' && s[i] == '\\' && i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
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
			out = append(out, Token{Text: b.String(), Line: start, Quoted: true})
		case strings.HasPrefix(s[i:], "<<") && i+2 < len(s) && isMarker(s[i+2]):
			start := line
			j := i + 2
			for j < len(s) && isMarker(s[j]) {
				j++
			}
			marker := s[i+2 : j]
			nl := strings.IndexByte(s[j:], '\n')
			if nl < 0 {
				return nil, fmt.Errorf("line %d: heredoc without body", start)
			}
			j += nl + 1
			line++
			bodyLine := line
			// The closing marker may be indented and followed by more tokens
			// (respond <<HTML … HTML 200); its indentation is taken off
			// every body line.
			var body []string
			closed := false
			for j < len(s) {
				end := strings.IndexByte(s[j:], '\n')
				if end < 0 {
					end = len(s) - j
				}
				l := s[j : j+end]
				trimmed := strings.TrimLeft(l, " \t")
				if rest, ok := strings.CutPrefix(trimmed, marker); ok && (rest == "" || strings.ContainsAny(rest[:1], " \t\r")) {
					indent := l[:len(l)-len(trimmed)]
					for k := range body {
						body[k] = strings.TrimPrefix(body[k], indent)
					}
					closed = true
					j += len(l) - len(rest)
					break
				}
				body = append(body, l)
				j += end
				if j < len(s) {
					j++
					line++
				}
			}
			if !closed {
				return nil, fmt.Errorf("line %d: heredoc %s not closed", start, marker)
			}
			out = append(out, Token{Text: strings.Join(body, "\n"), Line: start, Quoted: true, Body: bodyLine})
			i = j
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[j])) {
				j++
			}
			out = append(out, Token{Text: s[i:j], Line: line})
			i = j
		}
	}
	return out, nil
}

func isMarker(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

// node is one line of a Caddyfile, the tokens of a directive or of a site's
// addresses, with the block that follows it.
type Node struct {
	Args  []Token
	Block []Node
	Open  bool // ended with {, Block holds its contents
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

// at is the node's evidence: its file and line.
func (n Node) At() fact.Evidence {
	if len(n.Args) == 0 {
		return fact.Evidence{}
	}
	return fact.Evidence{Path: n.Args[0].File, Line: n.Args[0].Line}
}

// parse splits the tokens into lines and nests the { } blocks.
func Parse(toks []Token) ([]Node, error) {
	pos := 0
	nodes, err := parseBlock(toks, &pos, 0)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// maxDepth caps block nesting so a crafted file cannot overflow the stack.
const maxDepth = 1000

func parseBlock(toks []Token, pos *int, depth int) ([]Node, error) {
	var out []Node
	var cur Node
	flush := func() {
		if len(cur.Args) > 0 || cur.Open {
			out = append(out, cur)
		}
		cur = Node{}
	}
	for *pos < len(toks) {
		t := toks[*pos]
		*pos++
		switch {
		case t.Text == "\n" && !t.Quoted:
			flush()
		case t.Text == "{" && !t.Quoted:
			startLine := t.Line
			if depth >= maxDepth {
				return nil, fmt.Errorf("line %d: blocks nested more than %d deep", t.Line, maxDepth)
			}
			block, err := parseBlock(toks, pos, depth+1)
			if err != nil {
				return nil, err
			}
			if len(cur.Args) == 0 {
				// A block with nothing before it on its line: the global
				// options at the top, or a block that opens on the next line.
				cur.Args = []Token{{Text: "", Line: startLine, File: t.File}}
			}
			cur.Block, cur.Open = block, true
			flush()
		case t.Text == "}" && !t.Quoted:
			if depth == 0 {
				return nil, fmt.Errorf("line %d: unexpected }", t.Line)
			}
			flush()
			return out, nil
		default:
			cur.Args = append(cur.Args, t)
		}
	}
	if depth > 0 {
		return nil, fmt.Errorf("unclosed {")
	}
	flush()
	return out, nil
}

// Env is an environment variable a Caddyfile reads, and the line it is on.
type Env struct {
	Name string
	Line int
}

// envRe matches {$VAR} and {$VAR:default}, replaced when Caddy loads the
// file, and {env.VAR}, the runtime placeholder.
var envRe = regexp.MustCompile(`\{(?:\$([A-Za-z_][A-Za-z0-9_]*)(?::[^}]*)?|env\.([A-Za-z_][A-Za-z0-9_]*))\}`)

// EnvNames returns the environment variables a Caddyfile reads, in token
// order. Comments are not tokens, so a name in a comment is not read.
func EnvNames(src []byte) ([]Env, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	var out []Env
	for _, t := range toks {
		line := t.Line
		if t.Body > 0 {
			line = t.Body
		}
		for _, m := range envRe.FindAllStringSubmatchIndex(t.Text, -1) {
			name := t.Text[max(m[2], 0):max(m[3], 0)] + t.Text[max(m[4], 0):max(m[5], 0)]
			out = append(out, Env{Name: name, Line: line + strings.Count(t.Text[:m[0]], "\n")})
		}
	}
	return out, nil
}
