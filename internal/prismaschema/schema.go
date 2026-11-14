// Package prismaschema lexes and parses Prisma schema files for the prisma
// plugin (tables, columns) and the env plugin (the env("…") names a schema
// reads). See https://cernly.dev/how/plugins/.
package prismaschema

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The Prisma Schema Language as Prisma documents it: blocks
// `keyword Name { … }` (model, view, enum, type, datasource, generator), one
// field or attribute per line, // and /// comments, "…" strings with
// escapes, and attributes @name(…) / @@name(…) whose arguments nest.

type Token struct {
	Text string
	Line int
}

type lineErr struct {
	line int
	msg  string
}

func (e *lineErr) Error() string { return fmt.Sprintf("line %d: %s", e.line, e.msg) }

func LineOf(err error) int {
	var le *lineErr
	if errors.As(err, &le) {
		return le.line
	}
	return 1
}

func Lex(src []byte) ([]Token, error) {
	s := string(src)
	var out []Token
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			out = append(out, Token{"\n", line})
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case strings.HasPrefix(s[i:], "//"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				if j < len(s) && s[j] == '\n' {
					return nil, &lineErr{line, "unterminated string"}
				}
				j++
			}
			if j >= len(s) {
				return nil, &lineErr{line, "unterminated string"}
			}
			out = append(out, Token{s[i : j+1], line})
			i = j + 1
		case strings.HasPrefix(s[i:], "@@"):
			j := i + 2
			for j < len(s) && IsIdent(s[j]) {
				j++
			}
			out = append(out, Token{s[i:j], line})
			i = j
		case c == '@':
			j := i + 1
			for j < len(s) && (IsIdent(s[j]) || s[j] == '.') {
				j++ // @db.VarChar
			}
			out = append(out, Token{s[i:j], line})
			i = j
		case IsIdent(c):
			j := i
			for j < len(s) && IsIdent(s[j]) {
				j++
			}
			out = append(out, Token{s[i:j], line})
			i = j
		case strings.ContainsRune("{}()[]?,:=.!", rune(c)):
			out = append(out, Token{string(c), line})
			i++
		default:
			return nil, &lineErr{line, fmt.Sprintf("unexpected %q", c)}
		}
	}
	return out, nil
}

func IsIdent(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

type Block struct {
	Kind, Name string
	File       string
	Line, End  int
	Lines      []Line
}

// Line is one line of a block, its tokens.
type Line struct{ Toks []Token }

func Parse(file string, src []byte) ([]Block, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	var out []Block
	i := 0
	skipNL := func() {
		for i < len(toks) && toks[i].Text == "\n" {
			i++
		}
	}
	for {
		skipNL()
		if i >= len(toks) {
			return out, nil
		}
		if i+2 >= len(toks) || toks[i+2].Text != "{" {
			return nil, &lineErr{toks[i].Line, "expected `keyword Name {`"}
		}
		b := Block{Kind: toks[i].Text, Name: toks[i+1].Text, File: file, Line: toks[i].Line}
		i += 3
		var cur Line
		depth := 0 // parentheses and brackets, which may span lines
		for {
			if i >= len(toks) {
				return nil, &lineErr{b.Line, "block " + b.Name + " not closed"}
			}
			t := toks[i]
			i++
			switch {
			case t.Text == "}" && depth == 0:
				if len(cur.Toks) > 0 {
					b.Lines = append(b.Lines, cur)
				}
				b.End = t.Line
				out = append(out, b)
				goto next
			case t.Text == "\n" && depth == 0:
				if len(cur.Toks) > 0 {
					b.Lines = append(b.Lines, cur)
				}
				cur = Line{}
				continue
			case t.Text == "\n":
				continue
			case t.Text == "(" || t.Text == "[":
				depth++
			case t.Text == ")" || t.Text == "]":
				depth--
			}
			cur.Toks = append(cur.Toks, t)
		}
	next:
	}
}

func Unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `"`)
}

// attrStart returns the index of the attribute token name on the line, or -1.
func (l Line) attrStart(name string) int {
	for i, t := range l.Toks {
		if t.Text == name {
			return i
		}
	}
	return -1
}

// BlockAttr reads @@map("x") / @@schema("x"): the string argument.
func (l Line) BlockAttr(name string) string {
	if len(l.Toks) == 0 || l.Toks[0].Text != name {
		return ""
	}
	return l.firstString(1)
}

// FieldAttr reports a field attribute (@id, @unique, @relation).
func (l Line) FieldAttr(name string) bool { return l.attrStart(name) > 0 }

// FieldAttrArg reads @map("x") on a field line.
func (l Line) FieldAttrArg(name string) string {
	i := l.attrStart(name)
	if i <= 0 {
		return ""
	}
	return l.firstString(i + 1)
}

// firstString returns the first string argument of the parenthesised
// arguments starting at index i, also when named (name: "x").
func (l Line) firstString(i int) string {
	if i >= len(l.Toks) || l.Toks[i].Text != "(" {
		return ""
	}
	depth := 0
	for j := i; j < len(l.Toks); j++ {
		switch t := l.Toks[j].Text; {
		case t == "(" || t == "[":
			depth++
		case t == ")" || t == "]":
			depth--
			if depth == 0 {
				return ""
			}
		case strings.HasPrefix(t, `"`) && depth == 1:
			return Unquote(t) // @@map("x") and @@map(name: "x") alike
		}
	}
	return ""
}

// Fields reads @@id([a, b]): the field names of the first list argument.
func (l Line) Fields() []string {
	var out []string
	depth := 0
	for _, t := range l.Toks[1:] {
		switch t.Text {
		case "(", "[":
			depth++
			continue
		case ")", "]":
			depth--
			if depth <= 1 && len(out) > 0 {
				return out
			}
			continue
		}
		if depth == 2 && t.Text != "," && IsIdent(t.Text[0]) {
			out = append(out, t.Text)
		}
	}
	return out
}

// Native reads a field's native type: @db.VarChar(255) as VarChar(255).
func (l Line) Native() string {
	for i, t := range l.Toks {
		if !strings.HasPrefix(t.Text, "@db.") {
			continue
		}
		s := strings.TrimPrefix(t.Text, "@db.")
		if i+1 < len(l.Toks) && l.Toks[i+1].Text == "(" {
			for j := i + 1; j < len(l.Toks); j++ {
				s += l.Toks[j].Text
				if l.Toks[j].Text == ")" {
					break
				}
			}
		}
		return s
	}
	return ""
}

// ArgsAfter renders the parenthesised arguments that follow index i, for
// Unsupported("…").
func (l Line) ArgsAfter(i int) string {
	var b strings.Builder
	depth := 0
	for j := i; j < len(l.Toks); j++ {
		t := l.Toks[j].Text
		if t == "(" {
			depth++
		}
		if depth == 0 {
			break
		}
		b.WriteString(t)
		if t == ")" {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	return b.String()
}

// Env is an environment variable a schema reads, and the line it is on.
type Env struct {
	Name string
	Line int
}

// EnvNames returns the env("NAME") calls of a schema (datasource url,
// directUrl, shadowDatabaseUrl, generator settings), in order. Comments are
// not tokens, so a call in a comment is not read.
func EnvNames(src []byte) ([]Env, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	var out []Env
	for i := 0; i+3 < len(toks); i++ {
		if toks[i].Text == "env" && toks[i+1].Text == "(" && strings.HasPrefix(toks[i+2].Text, `"`) && toks[i+3].Text == ")" {
			out = append(out, Env{Name: Unquote(toks[i+2].Text), Line: toks[i+2].Line})
		}
	}
	return out, nil
}
