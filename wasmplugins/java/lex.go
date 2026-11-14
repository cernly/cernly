package main

// A lexer for Java and the Groovy and Kotlin DSLs of Gradle build scripts,
// as their grammars define tokens: identifiers, string literals (with text
// blocks and triple quotes), character literals, numbers, comments and
// punctuation. Patterns are matched on tokens, never on source text, so a
// name in a comment or inside a string is never read as code.

import "strings"

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tNumber
	tPunct
)

type token struct {
	kind tokKind
	text string // identifier or punctuation as written; a string's value
	line int
	// interp marks a Groovy/Kotlin double-quoted string with $name or
	// ${...}: its value is computed when the script runs.
	interp bool
}

// lex splits src into tokens. groovy enables single-quoted strings, slashy
// strings are not read (rare in build scripts; their content is skipped as
// punctuation).
func lex(src []byte, groovy bool) []token {
	s := string(src)
	var out []token
	line := 1
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
		case strings.HasPrefix(s[i:], "//"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = len(s) - i - 2
			}
			line += strings.Count(s[i:i+2+end], "\n")
			i += end + 4
		case strings.HasPrefix(s[i:], `"""`) || (groovy && strings.HasPrefix(s[i:], `'''`)):
			q := s[i : i+3]
			start := line
			end := strings.Index(s[i+3:], q)
			if end < 0 {
				end = len(s) - i - 3
			}
			body := s[i+3 : i+3+end]
			line += strings.Count(body, "\n")
			out = append(out, token{kind: tString, text: body, line: start, interp: q == `"""` && groovy && hasInterp(body)})
			i += end + 6
		case c == '"' || (groovy && c == '\''):
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' && j+1 < len(s) {
					b.WriteByte(unescape(s[j+1]))
					j += 2
					continue
				}
				b.WriteByte(s[j])
				j++
			}
			v := b.String()
			out = append(out, token{kind: tString, text: v, line: line, interp: c == '"' && groovy && hasInterp(s[i+1:min(j, len(s))])})
			i = j + 1
		case c == '\'':
			// Java character literal
			j := i + 1
			for j < len(s) && s[j] != '\'' && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, token{kind: tString, text: s[i+1 : min(j, len(s))], line: line})
			i = j + 1
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			out = append(out, token{kind: tIdent, text: s[i:j], line: line})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (isIdentPart(s[j]) || s[j] == '.') {
				j++
			}
			out = append(out, token{kind: tNumber, text: s[i:j], line: line})
			i = j
		default:
			out = append(out, token{kind: tPunct, text: string(c), line: line})
			i++
		}
	}
	return out
}

// hasInterp reports $name or ${ in a Groovy/Kotlin string body.
func hasInterp(body string) bool {
	for i := 0; i+1 < len(body); i++ {
		if body[i] == '\\' {
			i++
			continue
		}
		if body[i] == '$' && (body[i+1] == '{' || isIdentStart(body[i+1])) {
			return true
		}
	}
	return false
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	}
	return c
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// is reports a punctuation or identifier token with text t.
func (t token) is(text string) bool { return t.kind != tString && t.text == text }

// closing returns the index of the token that closes the bracket at i.
func closing(ts []token, i int) int {
	open := ts[i].text
	shut := map[string]string{"(": ")", "{": "}", "[": "]"}[open]
	depth := 0
	for j := i; j < len(ts); j++ {
		if ts[j].kind != tPunct {
			continue
		}
		switch ts[j].text {
		case open:
			depth++
		case shut:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(ts) - 1
}
