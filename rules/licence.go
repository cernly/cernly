package rules

import (
	"slices"
	"strings"
)

// LicenceDenied reads a declared licence as an SPDX expression and reports
// the ids on the deny list that every way of meeting it needs: OR needs one
// branch, AND needs both, "X WITH exception" counts as X, a trailing + is
// ignored. Ids compare without regard to case. ok is false when the
// expression does not parse; such a licence never matches (no guessing).
func LicenceDenied(expr string, deny []string) (hit []string, ok bool) {
	toks := licenceTokens(expr)
	p := &licParser{toks: toks}
	n, good := p.or()
	if !good || p.i != len(toks) {
		return nil, false
	}
	denied := func(id string) bool {
		return slices.ContainsFunc(deny, func(d string) bool { return strings.EqualFold(d, id) })
	}
	need, blocked := n.eval(denied)
	if !blocked {
		return nil, true
	}
	return need, true
}

func licenceTokens(s string) []string {
	s = strings.NewReplacer("(", " ( ", ")", " ) ").Replace(s)
	return strings.Fields(s)
}

type licNode struct {
	op       string // "", "AND", "OR"
	id       string
	children []*licNode
}

// eval: blocked when the expression cannot be met without a denied id; need
// lists the denied ids on the cheapest blocked path (all of them for AND).
func (n *licNode) eval(denied func(string) bool) (need []string, blocked bool) {
	switch n.op {
	case "":
		if denied(n.id) {
			return []string{n.id}, true
		}
		return nil, false
	case "OR":
		var first []string
		for _, c := range n.children {
			need, b := c.eval(denied)
			if !b {
				return nil, false
			}
			if first == nil {
				first = need
			}
		}
		return first, true
	default: // AND
		var all []string
		for _, c := range n.children {
			if need, b := c.eval(denied); b {
				for _, id := range need {
					if !slices.Contains(all, id) {
						all = append(all, id)
					}
				}
			}
		}
		return all, len(all) > 0
	}
}

type licParser struct {
	toks []string
	i    int
}

func (p *licParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *licParser) or() (*licNode, bool) {
	return p.list("OR", p.and)
}

func (p *licParser) and() (*licNode, bool) {
	return p.list("AND", p.atom)
}

func (p *licParser) list(op string, next func() (*licNode, bool)) (*licNode, bool) {
	n, ok := next()
	if !ok {
		return nil, false
	}
	out := &licNode{op: op, children: []*licNode{n}}
	for strings.EqualFold(p.peek(), op) {
		p.i++
		c, ok := next()
		if !ok {
			return nil, false
		}
		out.children = append(out.children, c)
	}
	if len(out.children) == 1 {
		return n, true
	}
	return out, true
}

func (p *licParser) atom() (*licNode, bool) {
	t := p.peek()
	switch {
	case t == "(":
		p.i++
		n, ok := p.or()
		if !ok || p.peek() != ")" {
			return nil, false
		}
		p.i++
		return n, true
	case t == "" || t == ")" || strings.EqualFold(t, "AND") || strings.EqualFold(t, "OR") || strings.EqualFold(t, "WITH"):
		return nil, false
	}
	p.i++
	if strings.EqualFold(p.peek(), "WITH") {
		p.i++
		if e := p.peek(); e == "" || e == "(" || e == ")" {
			return nil, false
		}
		p.i++
	}
	return &licNode{id: strings.TrimSuffix(t, "+")}, true
}
