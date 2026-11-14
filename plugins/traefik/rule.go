package traefik

import (
	"fmt"
	"strings"
)

// A router rule as Traefik documents it: matchers such as Host(`a`),
// PathPrefix(`/x`), Method(`GET`), joined with &&, || and !, grouped with
// parentheses; arguments in backticks or quotes, several allowed (v2). The
// parser keeps every matcher with whether it sits under a negation.

type matcher struct {
	Name    string
	Args    []string
	Negated bool
}

type ruleLexer struct {
	s   string
	pos int
}

func parseRule(s string) ([]matcher, error) {
	l := &ruleLexer{s: s}
	var out []matcher
	if err := l.expr(false, &out); err != nil {
		return nil, err
	}
	l.space()
	if l.pos != len(l.s) {
		return nil, fmt.Errorf("rule %q: unexpected %q", s, l.s[l.pos:])
	}
	return out, nil
}

func (l *ruleLexer) space() {
	for l.pos < len(l.s) && strings.ContainsRune(" \t\n", rune(l.s[l.pos])) {
		l.pos++
	}
}

func (l *ruleLexer) eat(tok string) bool {
	l.space()
	if strings.HasPrefix(l.s[l.pos:], tok) {
		l.pos += len(tok)
		return true
	}
	return false
}

func (l *ruleLexer) expr(neg bool, out *[]matcher) error {
	if err := l.and(neg, out); err != nil {
		return err
	}
	for l.eat("||") {
		if err := l.and(neg, out); err != nil {
			return err
		}
	}
	return nil
}

func (l *ruleLexer) and(neg bool, out *[]matcher) error {
	if err := l.unary(neg, out); err != nil {
		return err
	}
	for l.eat("&&") {
		if err := l.unary(neg, out); err != nil {
			return err
		}
	}
	return nil
}

func (l *ruleLexer) unary(neg bool, out *[]matcher) error {
	if l.eat("!") {
		return l.unary(!neg, out)
	}
	if l.eat("(") {
		if err := l.expr(neg, out); err != nil {
			return err
		}
		if !l.eat(")") {
			return fmt.Errorf("rule %q: ) missing", l.s)
		}
		return nil
	}
	l.space()
	start := l.pos
	for l.pos < len(l.s) && (l.s[l.pos] >= 'A' && l.s[l.pos] <= 'Z' || l.s[l.pos] >= 'a' && l.s[l.pos] <= 'z' || l.s[l.pos] >= '0' && l.s[l.pos] <= '9') {
		l.pos++
	}
	m := matcher{Name: l.s[start:l.pos], Negated: neg}
	if m.Name == "" || !l.eat("(") {
		return fmt.Errorf("rule %q: a matcher expected at %d", l.s, start)
	}
	for {
		l.space()
		if l.eat(")") {
			break
		}
		if l.pos >= len(l.s) {
			return fmt.Errorf("rule %q: ) missing", l.s)
		}
		q := l.s[l.pos]
		if q != '`' && q != '"' && q != '\'' {
			return fmt.Errorf("rule %q: a quoted argument expected at %d", l.s, l.pos)
		}
		end := strings.IndexByte(l.s[l.pos+1:], q)
		if end < 0 {
			return fmt.Errorf("rule %q: unterminated argument", l.s)
		}
		m.Args = append(m.Args, l.s[l.pos+1:l.pos+1+end])
		l.pos += end + 2
		l.eat(",")
	}
	*out = append(*out, m)
	return nil
}
