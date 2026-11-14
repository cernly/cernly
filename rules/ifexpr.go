package rules

import (
	"errors"
	"strings"
)

// An if: expression is read as a question about one run: does it run when
// the job is triggered by event for a change on branch? The answer is
// three-valued: the expression admits the branch, excludes it, or holds a
// term the rules cannot read.

type kleene int

const (
	kF kleene = iota
	kU
	kT
)

func (a kleene) and(b kleene) kleene { return min(a, b) }
func (a kleene) or(b kleene) kleene  { return max(a, b) }
func (a kleene) not() kleene         { return kT - a }

// world is the run being asked about. branch "" is some branch other than
// def, the default branch.
type world struct {
	event, branch, def string
}

type exprNode struct {
	op   string // "||", "&&", "!", "==", "!=", other comparisons, or "" for a lone operand
	kids []*exprNode
	val  *operand // op "": the operand
	l, r *operand // comparisons
}

type operand struct {
	kind string // "str", "num", "ident", "call"
	s    string
	text string
}

// parseIf parses an if: expression, ${{ }} optional.
func parseIf(expr string) (*exprNode, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "${{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(expr[3 : len(expr)-2])
	}
	toks, err := lex(expr)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.i != len(p.toks) {
		return nil, errors.New("trailing input")
	}
	return n, nil
}

type token struct {
	kind string // "op", "str", "word"
	s    string
}

var opToks = []string{"&&", "||", "==", "!=", "<=", ">=", "(", ")", ",", "!", "<", ">"}

func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case c == '\'' || c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(s); j++ {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						b.WriteByte(c)
						j++
						continue
					}
					break
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, errors.New("unterminated string")
			}
			out = append(out, token{"str", b.String()})
			i = j + 1
			continue
		}
		matched := false
		for _, o := range opToks {
			if strings.HasPrefix(s[i:], o) {
				out = append(out, token{"op", o})
				i += len(o)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		j := i
		for j < len(s) {
			c := s[j]
			if c == '[' { // index: github.event.inputs['x']
				k := strings.IndexByte(s[j:], ']')
				if k < 0 {
					return nil, errors.New("unterminated index")
				}
				j += k + 1
				continue
			}
			if c == '_' || c == '.' || c == '-' || c == '*' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
				j++
				continue
			}
			break
		}
		if j == i {
			return nil, errors.New("unexpected character")
		}
		out = append(out, token{"word", s[i:j]})
		i = j
	}
	return out, nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek(s string) bool {
	return p.i < len(p.toks) && p.toks[p.i].kind == "op" && p.toks[p.i].s == s
}

func (p *parser) or() (*exprNode, error) { return p.binary("||", p.and) }

func (p *parser) and() (*exprNode, error) { return p.binary("&&", p.unary) }

func (p *parser) binary(op string, next func() (*exprNode, error)) (*exprNode, error) {
	n, err := next()
	if err != nil {
		return nil, err
	}
	for p.peek(op) {
		p.i++
		m, err := next()
		if err != nil {
			return nil, err
		}
		n = &exprNode{op: op, kids: []*exprNode{n, m}}
	}
	return n, nil
}

func (p *parser) unary() (*exprNode, error) {
	if p.peek("!") {
		p.i++
		if !p.peek("(") && !p.peek("!") {
			// ! binds tighter than ==: !a == 'b' compares !a, which is not readable.
			l, err := p.operand()
			if err != nil {
				return nil, err
			}
			n, err := p.cmp(&exprNode{val: l})
			if err != nil {
				return nil, err
			}
			if n.op != "" {
				n.l = &operand{kind: "call", text: "!" + n.l.text}
				return n, nil
			}
			return &exprNode{op: "!", kids: []*exprNode{n}}, nil
		}
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &exprNode{op: "!", kids: []*exprNode{n}}, nil
	}
	if p.peek("(") {
		p.i++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.peek(")") {
			return nil, errors.New("missing )")
		}
		p.i++
		return p.cmp(n)
	}
	l, err := p.operand()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.peek(op) {
			p.i++
			r, err := p.operand()
			if err != nil {
				return nil, err
			}
			return &exprNode{op: op, l: l, r: r}, nil
		}
	}
	return &exprNode{val: l}, nil
}

// cmp handles a comparison on a parenthesised expression; its value is not
// readable, but the parse goes on.
func (p *parser) cmp(n *exprNode) (*exprNode, error) {
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.peek(op) {
			p.i++
			r, err := p.operand()
			if err != nil {
				return nil, err
			}
			t := n.text()
			if n.op != "" {
				t = "(" + t + ")"
			}
			return &exprNode{op: op, l: &operand{kind: "call", text: t}, r: r}, nil
		}
	}
	return n, nil
}

func (p *parser) operand() (*operand, error) {
	if p.i >= len(p.toks) {
		return nil, errors.New("missing operand")
	}
	t := p.toks[p.i]
	p.i++
	switch t.kind {
	case "str":
		return &operand{kind: "str", s: t.s, text: "'" + strings.ReplaceAll(t.s, "'", "''") + "'"}, nil
	case "word":
		if p.peek("(") {
			p.i++
			var args []string
			for !p.peek(")") {
				a, err := p.or()
				if err != nil {
					return nil, err
				}
				args = append(args, a.text())
				if p.peek(",") {
					p.i++
				} else if !p.peek(")") {
					return nil, errors.New("bad call")
				}
			}
			p.i++
			return &operand{kind: "call", s: t.s, text: t.s + "(" + strings.Join(args, ", ") + ")"}, nil
		}
		if c := t.s[0]; c == '-' || c >= '0' && c <= '9' {
			return &operand{kind: "num", s: t.s, text: t.s}, nil
		}
		return &operand{kind: "ident", s: t.s, text: t.s}, nil
	}
	return nil, errors.New("unexpected " + t.s)
}

// text is the expression normalised: single quotes, single spaces.
func (n *exprNode) text() string {
	// Parentheses where a child binds looser than its parent.
	sub := func(k *exprNode) string {
		if k.op == "||" && n.op != "||" || k.op == "&&" && n.op == "!" ||
			n.op == "!" && k.op != "" && k.op != "!" {
			return "(" + k.text() + ")"
		}
		return k.text()
	}
	switch n.op {
	case "||", "&&":
		return sub(n.kids[0]) + " " + n.op + " " + sub(n.kids[1])
	case "!":
		return "!" + sub(n.kids[0])
	case "":
		return n.val.text
	}
	return n.l.text + " " + n.op + " " + n.r.text
}

// eval answers the question for w. Every term is evaluated, also where the
// answer is already known, so that unread lists each term it cannot read.
func (n *exprNode) eval(w world, unread func(string)) kleene {
	switch n.op {
	case "||":
		return n.kids[0].eval(w, unread).or(n.kids[1].eval(w, unread))
	case "&&":
		return n.kids[0].eval(w, unread).and(n.kids[1].eval(w, unread))
	case "!":
		return n.kids[0].eval(w, unread).not()
	case "":
		if n.val.kind == "ident" {
			switch strings.ToLower(n.val.s) {
			case "true":
				return kT
			case "false", "null":
				return kF
			}
		}
	case "==", "!=":
		k := opEqual(n.l, n.r, w)
		if k == kU {
			k = opEqual(n.r, n.l, w)
		}
		if k != kU {
			if n.op == "!=" {
				k = k.not()
			}
			return k
		}
	}
	if unread != nil {
		unread(n.text())
	}
	return kU
}

// opEqual compares a context value a with a literal b; kU when either side is
// not readable. GitHub compares strings ignoring case.
func opEqual(a, b *operand, w world) kleene {
	if b.kind != "str" || b.s == "" {
		return kU
	}
	v := lookup(a, w)
	switch v.kind {
	case vNull:
		return kF
	case vKnown:
		if strings.EqualFold(v.s, b.s) {
			return kT
		}
		return kF
	case vOther:
		if !strings.HasPrefix(strings.ToLower(b.s), strings.ToLower(v.s)) || strings.EqualFold(b.s, v.s+w.def) {
			return kF
		}
	}
	return kU
}

const (
	vUnread = iota
	vKnown
	vNull
	vOther // a branch other than the default, as s + an unknown name
)

type value struct {
	kind int
	s    string
}

// lookup reads a context value in w.
func lookup(a *operand, w world) value {
	if a.kind == "str" {
		return value{vKnown, a.s}
	}
	if a.kind != "ident" {
		return value{}
	}
	name := strings.ToLower(a.s)
	branch := func(prefix, b string) value {
		if b == "" {
			return value{vOther, prefix}
		}
		return value{vKnown, prefix + b}
	}
	// The run's branch for push; the default branch for workflow_run and
	// schedule, which run on it whatever branch started them.
	ref := func(prefix string) value {
		switch w.event {
		case "push":
			return branch(prefix, w.branch)
		case "workflow_run", "schedule":
			return branch(prefix, w.def)
		}
		return value{}
	}
	// Fields of the run a workflow_run follows: null for other events.
	run := func(v value) value {
		switch {
		case w.event == "workflow_run":
			return v
		case strings.HasPrefix(name, "github.event.workflow_run."):
			return value{kind: vNull}
		}
		return value{}
	}
	switch {
	case name == "github.event_name" || name == "event_name":
		return value{vKnown, w.event}
	case name == "github.ref":
		return ref("refs/heads/")
	case name == "github.ref_name":
		return ref("")
	case name == "head_branch" || strings.HasSuffix(name, ".head_branch"):
		return run(branch("", w.branch))
	case name == "github.event.workflow_run.event" || name == "event":
		// The run judged follows a push to the branch.
		return run(value{vKnown, "push"})
	case name == "conclusion" || strings.HasSuffix(name, ".conclusion"):
		// The run judged follows a successful one.
		return run(value{vKnown, "success"})
	}
	return value{}
}
