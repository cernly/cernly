package dotnet

// A lexer for C# source, as the language grammar defines tokens: identifiers,
// numbers, string literals (regular, verbatim, interpolated and raw), character
// literals and punctuation. Comments and preprocessor directives yield no
// tokens, so the code of both #if branches is lexed. Patterns are matched on
// tokens, never on source text, so a name in a comment or inside a string is
// never read as code.

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type tokKind int

const (
	tokIdent tokKind = iota
	tokPunct
	tokNumber
	tokString
	tokChar
)

type token struct {
	kind    tokKind
	text    string // for tokString: the decoded value when literal, else the raw text
	literal bool   // tokString without interpolation holes
	line    int    // line of the token's first character; \r\n counts as one line break
	pair    int    // on an open bracket ( [ { <: the offset to the token that closes it; 0 when nothing does
}

// lex splits src into tokens. It never fails: an unterminated string or
// comment ends at the end of the input.
func lex(src []byte) []token {
	s := strings.TrimPrefix(string(src), "\ufeff")
	var out []token
	line := 1
	lineStart := true // only whitespace so far on this line
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			lineStart = true
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#' && lineStart:
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = len(s) - i - 2
			} else {
				end += 2
			}
			line += strings.Count(s[i:i+2+end], "\n")
			i += 2 + end
			lineStart = false
		case c == '"' || ((c == '$' || c == '@') && isStringPrefix(s, i)):
			text, literal, end := lexString(s, i)
			out = append(out, token{kind: tokString, text: text, literal: literal, line: line})
			line += strings.Count(s[i:end], "\n")
			i = end
			lineStart = false
		case c == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' && s[j] != '\n' {
				if s[j] == '\\' && j+1 < len(s) && s[j+1] != '\n' {
					j++
				}
				j++
			}
			j = min(j, len(s))
			out = append(out, token{kind: tokChar, text: unescape(s[i+1 : j]), line: line})
			if j < len(s) && s[j] == '\'' {
				j++
			}
			i = j
			lineStart = false
		case isIdentStart(c) || (c == '@' && i+1 < len(s) && isIdentStart(s[i+1])):
			j := i + 1
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			out = append(out, token{kind: tokIdent, text: s[i:j], line: line})
			i = j
			lineStart = false
		case c >= '0' && c <= '9':
			j := i + 1
			hex := c == '0' && j < len(s) && (s[j] == 'x' || s[j] == 'X')
			for j < len(s) {
				d := s[j]
				switch {
				case isIdentPart(d):
					j++
				case d == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9':
					j++
				case (d == '+' || d == '-') && !hex && (s[j-1] == 'e' || s[j-1] == 'E'):
					j++
				default:
					goto done
				}
			}
		done:
			out = append(out, token{kind: tokNumber, text: s[i:j], line: line})
			i = j
			lineStart = false
		default:
			_, n := utf8.DecodeRuneInString(s[i:])
			out = append(out, token{kind: tokPunct, text: s[i : i+n], line: line})
			i += n
			lineStart = false
		}
	}
	pairBrackets(out)
	return out
}

// pairBrackets matches every open bracket with the token that closes it, once
// per file and in one pass: one stack per bracket kind, so each kind nests on
// its own, as a count of that kind from the open would. < and > are paired the
// same way (callers only ask at a generic's <); an open that never closes keeps
// pair 0. The offset is relative, so it holds in any subslice that contains both.
func pairBrackets(toks []token) {
	var stacks [4][]int
	for i, t := range toks {
		if t.kind != tokPunct || len(t.text) != 1 {
			continue
		}
		k := strings.IndexByte("([{<", t.text[0])
		if k >= 0 {
			stacks[k] = append(stacks[k], i)
			continue
		}
		if k = strings.IndexByte(")]}>", t.text[0]); k >= 0 {
			if n := len(stacks[k]); n > 0 {
				o := stacks[k][n-1]
				stacks[k] = stacks[k][:n-1]
				toks[o].pair = i - o
			}
		}
	}
}

// isStringPrefix reports whether the $ and @ characters at i lead into a
// quote: $", @", $@", @$" and $$"""...
func isStringPrefix(s string, i int) bool {
	j := i
	for j < len(s) && (s[j] == '$' || s[j] == '@') {
		j++
	}
	return j < len(s) && s[j] == '"'
}

// lexString reads the string literal that starts at i (a prefix or a quote)
// and returns its text, whether it is a plain literal, and the index after it.
func lexString(s string, i int) (text string, literal bool, end int) {
	j := i
	dollars, verbatim := 0, false
	for j < len(s) && (s[j] == '$' || s[j] == '@') {
		if s[j] == '$' {
			dollars++
		} else {
			verbatim = true
		}
		j++
	}
	interpolated := dollars > 0
	if verbatim {
		return lexVerbatim(s, i, j, interpolated)
	}
	quotes := 0
	for j+quotes < len(s) && s[j+quotes] == '"' {
		quotes++
	}
	switch {
	case quotes >= 3:
		return lexRaw(s, i, j, quotes, dollars)
	case quotes == 2:
		return "", true, j + 2
	}
	// regular string: single line, backslash escapes
	k := j + 1
	hole := false
	for k < len(s) && s[k] != '"' && s[k] != '\n' {
		switch {
		case s[k] == '\\' && k+1 < len(s) && s[k+1] != '\n':
			k += 2
		case interpolated && s[k] == '{':
			if k+1 < len(s) && s[k+1] == '{' {
				k += 2
			} else {
				hole = true
				k = skipHole(s, k+1)
			}
		default:
			k++
		}
	}
	k = min(k, len(s))
	closed := k < len(s) && s[k] == '"'
	body := s[j+1 : k]
	if closed {
		k++
	}
	if hole {
		return s[i:k], false, k
	}
	return unbrace(unescape(body), interpolated), true, k
}

// lexVerbatim reads @"..." (and $@"...", @$"...") where "" is an escaped quote.
func lexVerbatim(s string, start, quote int, interpolated bool) (string, bool, int) {
	k := quote + 1
	hole := false
	for k < len(s) {
		switch {
		case s[k] == '"' && k+1 < len(s) && s[k+1] == '"':
			k += 2
		case s[k] == '"':
			goto closed
		case interpolated && s[k] == '{':
			if k+1 < len(s) && s[k+1] == '{' {
				k += 2
			} else {
				hole = true
				k = skipHole(s, k+1)
			}
		default:
			k++
		}
	}
closed:
	k = min(k, len(s))
	end := k
	if k < len(s) {
		end = k + 1
	}
	if hole {
		return s[start:end], false, end
	}
	return unbrace(strings.ReplaceAll(s[quote+1:k], `""`, `"`), interpolated), true, end
}

// lexRaw reads a C# 11 raw string: n >= 3 opening quotes end at the first run of
// n quotes. With dollars > 0 it is interpolated and its raw text is returned.
func lexRaw(s string, start, quote, n, dollars int) (string, bool, int) {
	bodyStart := quote + n
	k := bodyStart
	bodyEnd, end := len(s), len(s)
	for k < len(s) {
		if s[k] != '"' {
			k++
			continue
		}
		r := 0
		for k+r < len(s) && s[k+r] == '"' {
			r++
		}
		if r >= n {
			bodyEnd, end = k+r-n, k+r
			break
		}
		k += r
	}
	if dollars > 0 && hasBraceRun(s[bodyStart:bodyEnd], dollars) {
		return s[start:end], false, end
	}
	return rawValue(s[bodyStart:bodyEnd]), true, end
}

// hasBraceRun reports a run of at least n '{', which opens a hole in a raw
// string with n dollar signs.
func hasBraceRun(b string, n int) bool {
	run := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '{' {
			run = 0
			continue
		}
		if run++; run >= n {
			return true
		}
	}
	return false
}

// unbrace turns {{ and }} into { and } in the text of an interpolated string
// without holes.
func unbrace(b string, interpolated bool) string {
	if !interpolated {
		return b
	}
	return strings.NewReplacer("{{", "{", "}}", "}").Replace(b)
}

// rawValue applies the raw string rules: a body that starts and ends with a line
// break loses both, and the whitespace before the closing quotes is removed
// from every line.
func rawValue(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[len(lines)-1]) != "" {
		return body
	}
	indent := lines[len(lines)-1]
	mid := lines[1 : len(lines)-1]
	for i, l := range mid {
		mid[i] = strings.TrimPrefix(l, indent)
	}
	return strings.Join(mid, "\n")
}

// skipHole returns the index after the } that closes an interpolation hole whose
// content starts at i. Strings and chars inside the hole are skipped whole.
func skipHole(s string, i int) int {
	depth := 1
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"' || ((c == '$' || c == '@') && isStringPrefix(s, i)):
			_, _, i = lexString(s, i)
			continue
		case c == '\'':
			i++
			for i < len(s) && s[i] != '\'' && s[i] != '\n' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(s)
}

// unescape decodes the C# backslash escapes of a regular string or char body.
func unescape(b string) string {
	if !strings.Contains(b, `\`) {
		return b
	}
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			sb.WriteByte(b[i])
			continue
		}
		i++
		switch b[i] {
		case 'n':
			sb.WriteByte('\n')
		case 't':
			sb.WriteByte('\t')
		case 'r':
			sb.WriteByte('\r')
		case '0':
			sb.WriteByte(0)
		case 'a':
			sb.WriteByte(7)
		case 'b':
			sb.WriteByte(8)
		case 'f':
			sb.WriteByte(12)
		case 'v':
			sb.WriteByte(11)
		case 'u', 'U', 'x':
			max := map[byte]int{'u': 4, 'U': 8, 'x': 4}[b[i]]
			j := i + 1
			for j < len(b) && j-i-1 < max && isHex(b[j]) {
				j++
			}
			v, err := strconv.ParseUint(b[i+1:j], 16, 32)
			if err != nil || j == i+1 {
				sb.WriteByte(b[i])
				continue
			}
			sb.WriteRune(rune(v))
			i = j - 1
		default:
			sb.WriteByte(b[i])
		}
	}
	return sb.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }
