// Package sqlddl reads SQL migrations without running them: a lexer splits
// statements, and a replay applies the DDL that shapes tables and columns
// to an in-memory schema. PostgreSQL first; MySQL quoting is tolerated.
// Statements it does not know are skipped, never guessed. See
// https://cernly.dev/how/plugins/.
package sqlddl

import (
	"fmt"
	"strings"
)

type kind byte

const (
	kWord   kind = 'w' // an unquoted word: keyword or identifier
	kQuoted kind = 'q' // a quoted identifier ("x" or `x`), text as written inside the quotes
	kString kind = 's' // a string literal, any form
	kNumber kind = 'n'
	kPunct  kind = 'p' // ( ) , . [ ]
	kOther  kind = 'o' // operators, parameters
)

type tok struct {
	kind kind
	text string
	line int
	asIs bool // an unquoted word whose case is kept as an identifier (SQL Server)
}

// is reports whether the token is the unquoted keyword w (case-insensitive).
func (t tok) is(w string) bool { return t.kind == kWord && strings.EqualFold(t.text, w) }

// Stmt is one statement: its tokens, without comments, and its first and
// last line.
type Stmt struct {
	toks       []tok
	Line, End  int
	backticked bool
	mysql      bool // lexed with MySQL's string escapes and # comments
	hashLine   bool // a line starting with #: a MySQL comment
	dollarLine int  // line of the first $tag$ … $tag$ string: PostgreSQL only
}

// SyntaxError is a lexing failure at a line.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// downMarker reports whether a line comment starts the down half of a
// single-file migration (goose, dbmate): nothing after it is applied.
func downMarker(comment string) bool {
	c := strings.ToLower(strings.TrimSpace(comment))
	return strings.HasPrefix(c, "+goose down") || strings.HasPrefix(c, "migrate:down")
}

// Split lexes a file into statements. It stops at a down marker. Strings
// follow PostgreSQL: a backslash is literal except in E'…'.
func Split(src []byte) ([]Stmt, error) { return lex(src, false, false) }

// SplitSet lexes a migration set's files. A set that is MySQL-shaped
// (backticks, an ENGINE= option, a line starting with #, or a file only
// MySQL's lexing reads) is lexed again as MySQL: backslash escapes in quotes
// and # line comments.
func SplitSet(srcs [][]byte) ([][]Stmt, []error) {
	stmts := make([][]Stmt, len(srcs))
	errs := make([]error, len(srcs))
	mysql := false
	for i, src := range srcs {
		stmts[i], errs[i] = lex(src, false, false)
		if errs[i] != nil {
			if _, err := lex(src, true, false); err == nil {
				mysql = true
			}
		}
		for _, st := range stmts[i] {
			if st.backticked || st.hashLine || engineOption(st.toks) {
				mysql = true
			}
		}
	}
	if mysql {
		for i, src := range srcs {
			stmts[i], errs[i] = lex(src, true, false)
		}
	}
	return stmts, errs
}

// lex splits src into statements. mysql: backslash escapes and # comments.
// mssql (T-SQL): [name] is a quoted identifier, ]] inside it a ], and an
// unquoted name keeps its case.
func lex(src []byte, mysql, mssql bool) ([]Stmt, error) {
	s := string(src)
	var out []Stmt
	cur := Stmt{mysql: mysql}
	line := 1
	flush := func() {
		if len(cur.toks) > 0 {
			cur.Line, cur.End = cur.toks[0].line, cur.toks[len(cur.toks)-1].line
			out = append(out, cur)
		}
		cur = Stmt{mysql: mysql}
	}
	add := func(k kind, text string, l int) {
		cur.toks = append(cur.toks, tok{kind: k, text: text, line: l, asIs: mssql && k == kWord})
	}
	lineStart := true
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
			lineStart = true
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
			continue
		case c == '\\' && lineStart:
			// a psql meta-command (\connect, \set): the rest of the line
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		first := lineStart
		lineStart = false
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			if downMarker(s[i+2 : i+j]) {
				flush()
				return out, nil
			}
			i += j
		case c == '#' && mysql:
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			i += j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			start, depth := line, 0
			for i < len(s) {
				switch {
				case strings.HasPrefix(s[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(s[i:], "*/"):
					depth--
					i += 2
				default:
					if s[i] == '\n' {
						line++
					}
					i++
				}
				if depth == 0 {
					break
				}
			}
			if depth != 0 {
				return nil, &SyntaxError{start, "unterminated comment"}
			}
		case c == ';':
			flush()
			i++
		case c == '\'':
			text, n, nl, err := quoted(s[i:], '\'', mysql)
			if err != nil {
				return nil, &SyntaxError{line, err.Error()}
			}
			add(kString, text, line)
			line += nl
			i += n
		case c == '[' && mssql:
			text, n, nl, err := quoted(s[i:], ']', false)
			if err != nil {
				return nil, &SyntaxError{line, err.Error()}
			}
			add(kQuoted, text, line)
			line += nl
			i += n
		case c == '"' || c == '`':
			text, n, nl, err := quoted(s[i:], c, mysql && c == '"')
			if err != nil {
				return nil, &SyntaxError{line, err.Error()}
			}
			add(kQuoted, text, line)
			if c == '`' {
				cur.backticked = true
			}
			line += nl
			i += n
		case c == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			add(kOther, s[i:j], line)
			i = j
		case c == '$':
			// a dollar-quoted string: $tag$ … $tag$
			j := i + 1
			for j < len(s) && isWordByte(s[j]) && s[j] != '$' {
				j++
			}
			if j >= len(s) || s[j] != '$' {
				add(kOther, "$", line)
				i++
				continue
			}
			tag := s[i : j+1]
			end := strings.Index(s[j+1:], tag)
			if end < 0 {
				return nil, &SyntaxError{line, "unterminated " + tag + " string"}
			}
			body := s[j+1 : j+1+end]
			if cur.dollarLine == 0 {
				cur.dollarLine = line
			}
			add(kString, body, line)
			line += strings.Count(body, "\n")
			i = j + 1 + end + len(tag)
		case isWordStart(c):
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			w := s[i:j]
			if j < len(s) && s[j] == '\'' && len(w) == 1 && strings.ContainsAny(w, "eEbBxXnNuU") {
				// E'…' (backslash escapes), B'…', X'…', N'…', U&'…'
				text, n, nl, err := quoted(s[j:], '\'', mysql || w == "e" || w == "E")
				if err != nil {
					return nil, &SyntaxError{line, err.Error()}
				}
				add(kString, text, line)
				line += nl
				i = j + n
				continue
			}
			add(kWord, w, line)
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.' || s[j] == '_' ||
				(s[j] == 'e' || s[j] == 'E') && j+1 < len(s) && (s[j+1] >= '0' && s[j+1] <= '9' || s[j+1] == '-' || s[j+1] == '+') ||
				(s[j] == '-' || s[j] == '+') && (s[j-1] == 'e' || s[j-1] == 'E')) {
				j++
			}
			add(kNumber, s[i:j], line)
			i = j
		case strings.IndexByte("(),.[]", c) >= 0:
			add(kPunct, string(c), line)
			i++
		default:
			if c == '#' && first {
				cur.hashLine = true // no PostgreSQL line starts with #
			}
			add(kOther, string(c), line)
			i++
		}
	}
	flush()
	return out, nil
}

// quoted reads a quoted run starting at s[0] == q; a doubled quote is one
// quote, and with backslash escapes a backslash takes the next byte. It
// returns the text, the bytes read and the newlines crossed.
func quoted(s string, q byte, backslash bool) (string, int, int, error) {
	var b strings.Builder
	nl := 0
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			nl++
		}
		if backslash && c == '\\' && i+1 < len(s) {
			if s[i+1] == '\n' {
				nl++
			}
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if c == q {
			if i+1 < len(s) && s[i+1] == q {
				b.WriteByte(q)
				i++
				continue
			}
			return b.String(), i + 1, nl, nil
		}
		b.WriteByte(c)
	}
	return "", 0, 0, fmt.Errorf("unterminated %c", q)
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isWordByte(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' || c == '$' }
