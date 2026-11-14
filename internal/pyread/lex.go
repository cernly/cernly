package pyread

import (
	"fmt"
	"strings"
)

// The Python lexical grammar as the language reference gives it, as far as
// the patterns need: names, numbers, operators, comments, all string prefixes
// (r, u, b, f, t and their pairs, any case), single and triple quotes, and
// f-strings whose replacement fields are lexed as code (PEP 701: they may hold
// strings with the same quotes, and nest). Indentation is kept as each line's
// first column, not as INDENT/DEDENT tokens.

type kind int

const (
	tName kind = iota
	tNumber
	tString  // a plain string: text is its value
	tFString // an f-string (or t-string): text is its literal text up to the first replacement field
	tFEnd    // the end of an f-string's replacement fields
	tOp
)

type token struct {
	kind  kind
	text  string
	line  int
	col   int  // 0-based column
	first bool // the first token of its logical line
	raw   bool // a string with an r prefix (no escapes)
}

type lexer struct {
	src    string
	pos    int
	line   int
	col    int
	depth  int // open brackets: inside them a newline does not end the line
	fields int // open f-string replacement fields
	out    []token
	lineAt bool // the next token starts a logical line
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src, line: 1, lineAt: true}
	if err := l.run(false); err != nil {
		return nil, err
	}
	return l.out, nil
}

func (l *lexer) peek(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

func (l *lexer) advance(n int) {
	for i := 0; i < n && l.pos < len(l.src); i++ {
		if l.src[l.pos] == '\n' {
			l.line++
			l.col = 0
		} else {
			l.col++
		}
		l.pos++
	}
}

func (l *lexer) emit(k kind, text string, line, col int, raw bool) {
	l.out = append(l.out, token{kind: k, text: text, line: line, col: col, first: l.lineAt, raw: raw})
	l.lineAt = false
}

var twoCharOps = []string{"**", "//", "==", "!=", "<=", ">=", "->", ":=", "<<", ">>", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "@="}

// run lexes until the end, or, inside an f-string's replacement field
// (field), until the field's closing '}', a '!' conversion or a ':' format
// spec at bracket depth zero, which it leaves for the caller.
func (l *lexer) run(field bool) error {
	depth0 := l.depth
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			if l.depth == 0 && !field {
				l.lineAt = true
			}
			l.advance(1)
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			l.advance(1)
		case c == '\\' && l.peek(1) == '\n':
			l.advance(2) // explicit line joining
		case c == '\\' && l.peek(1) == '\r' && l.peek(2) == '\n':
			l.advance(3)
		case c == '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance(1)
			}
		case field && l.depth == depth0 && (c == '}' || c == ':' || (c == '!' && l.peek(1) != '=')):
			return nil
		case isNameStart(c):
			start, line, col := l.pos, l.line, l.col
			for l.pos < len(l.src) && isNamePart(l.src[l.pos]) {
				l.advance(1)
			}
			word := l.src[start:l.pos]
			if q := l.peek(0); (q == '"' || q == '\'') && isPrefix(word) {
				if err := l.str(strings.ToLower(word), line, col); err != nil {
					return err
				}
				continue
			}
			l.emit(tName, word, line, col, false)
		case c >= '0' && c <= '9' || (c == '.' && l.peek(1) >= '0' && l.peek(1) <= '9'):
			start, line, col := l.pos, l.line, l.col
			for l.pos < len(l.src) && (isNamePart(l.src[l.pos]) || l.src[l.pos] == '.' ||
				((l.src[l.pos] == '+' || l.src[l.pos] == '-') && (l.src[l.pos-1] == 'e' || l.src[l.pos-1] == 'E'))) {
				l.advance(1)
			}
			l.emit(tNumber, l.src[start:l.pos], line, col, false)
		case c == '"' || c == '\'':
			if err := l.str("", l.line, l.col); err != nil {
				return err
			}
		default:
			line, col := l.line, l.col
			op := string(c)
			for _, o := range twoCharOps {
				if strings.HasPrefix(l.src[l.pos:], o) {
					op = o
					break
				}
			}
			switch op {
			case "(", "[", "{":
				l.depth++
			case ")", "]", "}":
				if l.depth > 0 {
					l.depth--
				}
			}
			l.advance(len(op))
			l.emit(tOp, op, line, col, false)
		}
	}
	if field {
		return fmt.Errorf("line %d: f-string replacement field not closed", l.line)
	}
	return nil
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isNamePart(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

func isPrefix(w string) bool {
	switch strings.ToLower(w) {
	case "r", "u", "b", "f", "t", "br", "rb", "fr", "rf", "tr", "rt":
		return true
	}
	return false
}

// str lexes a string literal at l.pos (its prefix already read).
func (l *lexer) str(prefix string, line, col int) error {
	q := l.src[l.pos]
	quote := string(q)
	if l.peek(1) == q && l.peek(2) == q {
		quote = strings.Repeat(string(q), 3)
	}
	l.advance(len(quote))
	raw := strings.Contains(prefix, "r")
	format := strings.ContainsAny(prefix, "ft")
	var lit strings.Builder
	k := tString
	if format {
		k = tFString
	}
	emitted := false
	at := len(l.out)
	for {
		if l.pos >= len(l.src) {
			return fmt.Errorf("line %d: string not closed", line)
		}
		if strings.HasPrefix(l.src[l.pos:], quote) {
			l.advance(len(quote))
			break
		}
		c := l.src[l.pos]
		if c == '\n' && len(quote) == 1 {
			return fmt.Errorf("line %d: string not closed", line)
		}
		if c == '\\' && l.pos+1 < len(l.src) {
			if raw {
				lit.WriteByte(c)
				lit.WriteByte(l.src[l.pos+1])
			} else {
				lit.WriteString(unescape(l.src[l.pos+1]))
			}
			l.advance(2)
			continue
		}
		if format && c == '{' && l.peek(1) == '{' {
			if !emitted {
				lit.WriteByte('{')
			}
			l.advance(2)
			continue
		}
		if format && c == '}' && l.peek(1) == '}' {
			if !emitted {
				lit.WriteByte('}')
			}
			l.advance(2)
			continue
		}
		if format && c == '{' {
			if !emitted {
				// The f-string token comes before its fields' tokens.
				l.out = append(l.out[:at], append([]token{{kind: tFString, text: lit.String(), line: line, col: col, first: l.lineAt, raw: raw}}, l.out[at:]...)...)
				l.lineAt = false
				emitted = true
			}
			l.advance(1)
			l.depth++
			if err := l.field(); err != nil {
				return err
			}
			continue
		}
		if !emitted {
			lit.WriteByte(c)
		}
		l.advance(1)
	}
	if !emitted {
		l.emit(k, lit.String(), line, col, raw)
	} else {
		l.emit(tFEnd, "", l.line, l.col, false)
	}
	return nil
}

// maxFields caps f-string nesting, as CPython does, so a crafted file
// cannot overflow the stack.
const maxFields = 200

// field lexes one replacement field after its '{': the expression as code,
// then an optional !conversion and :format spec (which may hold fields), up
// to the closing '}'.
func (l *lexer) field() error {
	if l.fields >= maxFields {
		return fmt.Errorf("line %d: f-strings nested more than %d deep", l.line, maxFields)
	}
	l.fields++
	defer func() { l.fields-- }()
	if err := l.run(true); err != nil {
		return err
	}
	if l.peek(0) == '!' {
		l.advance(2)
	}
	if l.peek(0) == ':' {
		l.advance(1)
		for l.pos < len(l.src) && l.src[l.pos] != '}' {
			if l.src[l.pos] == '{' {
				l.advance(1)
				l.depth++
				if err := l.field(); err != nil {
					return err
				}
				continue
			}
			l.advance(1)
		}
	}
	if l.peek(0) != '}' {
		return fmt.Errorf("line %d: f-string replacement field not closed", l.line)
	}
	l.advance(1)
	l.depth--
	return nil
}

// unescape decodes the escapes that matter for names and URLs; the rest
// (\x, \u, octal) keep their letters, which no host or variable name holds.
func unescape(c byte) string {
	switch c {
	case 'n':
		return "\n"
	case 't':
		return "\t"
	case '\n':
		return ""
	}
	return string(c)
}
