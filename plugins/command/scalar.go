package command

import (
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// seg says where a stretch of a YAML scalar's value comes from: from value
// line vl, column col (1-based bytes) on, the text is on file line file.
// Folding joins and splits lines, so a value line can hold several.
type seg struct{ vl, col, file int }

// yamlSnippet is a shell snippet read from a YAML scalar, with its value
// mapped back to file lines. Literal (|) and one-line scalars map line for
// line. Folded (>) and multi-line flow scalars are folded again from the raw
// lines; when that does not give the parsed value exactly, every finding in
// the scalar gets its first line.
func yamlSnippet(kind, p, unit string, raw []string, n *yaml.Node, wf *workflowInfo) *snippet {
	sn := &snippet{kind: kind, path: p, unit: unit, line: scalarStart(n), text: []byte(n.Value), wf: wf}
	if n.Style&yaml.LiteralStyle != 0 {
		return sn
	}
	var segs []seg
	var ok bool
	if n.Style&yaml.FoldedStyle != 0 {
		segs, ok = foldedSegs(raw, n)
	} else {
		segs, ok = flowSegs(raw, n)
	}
	switch {
	case !ok:
		sn.flat = true
	case len(segs) > 1:
		sn.segs = segs
	}
	return sn
}

// fileLine is the file line of a position in the snippet's text.
func (sn *snippet) fileLine(l, c int) int {
	if sn.flat {
		return sn.line
	}
	if sn.segs == nil {
		return sn.line + l - 1
	}
	file := sn.segs[0].file
	for _, s := range sn.segs {
		if s.vl > l || s.vl == l && s.col > c {
			break
		}
		file = s.file
	}
	return file
}

// folder builds a value from raw line pieces and records each piece's seg.
type folder struct {
	b      strings.Builder
	vl, cl int // current value line, and its length so far
	segs   []seg
}

func (f *folder) text(s string, file int) {
	if s == "" {
		return
	}
	f.segs = append(f.segs, seg{vl: f.vl, col: f.cl + 1, file: file})
	f.b.WriteString(s)
	f.cl += len(s)
}

func (f *folder) str(s string) {
	for _, c := range []byte(s) {
		f.b.WriteByte(c)
		if c == '\n' {
			f.vl++
			f.cl = 0
		} else {
			f.cl++
		}
	}
}

func newFolder() *folder { return &folder{vl: 1} }

// rawLine is file line l (1-based) without its line break.
func rawLine(raw []string, l int) (string, bool) {
	if l < 1 || l > len(raw) {
		return "", false
	}
	return strings.TrimSuffix(raw[l-1], "\r"), true
}

// fromColumn cuts a line at a yaml.v3 column (1-based, in characters).
func fromColumn(line string, col int) (string, bool) {
	i := 0
	for k := 1; k < col; k++ {
		if i >= len(line) {
			return "", false
		}
		_, w := utf8.DecodeRuneInString(line[i:])
		i += w
	}
	return line[i:], true
}

const maxScalarLines = 10_000

// flowSegs maps a plain, single- or double-quoted scalar. Lines are added
// until folding them (spaces trimmed, a line break between two lines read as
// a space, n empty lines as n line breaks) gives the parsed value. Escapes
// are not undone: a quoted scalar with one maps only when on one line.
func flowSegs(raw []string, n *yaml.Node) ([]seg, bool) {
	first, ok := rawLine(raw, n.Line)
	if !ok {
		return nil, false
	}
	rest, ok := fromColumn(first, n.Column)
	if !ok {
		return nil, false
	}
	quote := byte(0)
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		quote = '"'
	case n.Style&yaml.SingleQuotedStyle != 0:
		quote = '\''
	}
	if quote != 0 {
		if !strings.HasPrefix(rest, string(quote)) {
			return nil, false
		}
		rest = rest[1:]
	}
	type piece struct {
		text string
		file int
	}
	var pieces []piece
	for l := n.Line; l < n.Line+maxScalarLines; l++ {
		line := rest
		if l > n.Line {
			if line, ok = rawLine(raw, l); !ok {
				return nil, false
			}
			line = strings.TrimLeft(line, " \t")
		}
		closed := false
		if quote != 0 {
			i, esc := closingQuote(line, quote)
			if esc {
				return nil, l == n.Line && i >= 0
			}
			if i >= 0 {
				line, closed = line[:i], true
			}
		}
		if !closed {
			line = strings.TrimRight(line, " \t")
		}
		pieces = append(pieces, piece{line, l})
		if quote != 0 && !closed {
			continue
		}
		f := newFolder()
		empty := 0
		for i, pc := range pieces {
			switch {
			case pc.text == "" && i > 0 && i < len(pieces)-1:
				empty++
				continue
			case i > 0 && empty == 0:
				f.str(" ")
			case i > 0:
				f.str(strings.Repeat("\n", empty))
			}
			empty = 0
			f.text(pc.text, pc.file)
		}
		got := f.b.String()
		switch {
		case got == n.Value && len(pieces) == 1:
			return nil, true
		case got == n.Value:
			return f.segs, true
		case quote != 0 || len(got) > len(n.Value):
			return nil, false
		}
	}
	return nil, false
}

// closingQuote is the index of the quote that ends a quoted scalar on this
// line, -1 when it goes on, and whether an escape (a backslash, or a doubled single quote) comes
// before it.
func closingQuote(s string, q byte) (int, bool) {
	esc := false
	for i := 0; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			esc = true
			i++
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			esc = true
			i++
		case s[i] == q:
			return i, esc
		}
	}
	return -1, esc
}

// foldedSegs maps a folded block scalar (>, >-, >+). Content lines are the
// lines indented at least as far as the first non-empty one. Two adjacent
// lines join with a space, n empty lines between them give n line breaks,
// and the break after a more-indented line is kept.
func foldedSegs(raw []string, n *yaml.Node) ([]seg, bool) {
	header, ok := rawLine(raw, n.Line)
	if !ok {
		return nil, false
	}
	h, ok := fromColumn(header, n.Column)
	if !ok || !strings.HasPrefix(h, ">") || strings.ContainsAny(strings.Fields(h)[0], "123456789") {
		return nil, false // an indentation indicator is not mapped
	}
	type line struct {
		text string
		file int
	}
	var lines []line
	indent := -1
	for l := n.Line + 1; ; l++ {
		s, ok := rawLine(raw, l)
		if !ok {
			break
		}
		if strings.TrimLeft(s, " ") == "" {
			lines = append(lines, line{"", l})
			continue
		}
		in := len(s) - len(strings.TrimLeft(s, " "))
		if indent < 0 {
			indent = in
		}
		if in < indent {
			break
		}
		lines = append(lines, line{s[indent:], l})
	}
	// Trailing empty lines are chomping's: kept as written in the value's
	// tail, never mapped.
	for len(lines) > 0 && lines[len(lines)-1].text == "" {
		lines = lines[:len(lines)-1]
	}
	f := newFolder()
	empty, prevMore := 0, false
	for i, ln := range lines {
		if ln.text == "" {
			empty++
			continue
		}
		more := ln.text[0] == ' ' || ln.text[0] == '\t'
		switch {
		case i == 0:
		case empty == 0 && !more && !prevMore:
			f.str(" ")
		case more || prevMore:
			f.str("\n" + strings.Repeat("\n", empty))
		default:
			f.str(strings.Repeat("\n", empty))
		}
		empty, prevMore = 0, more
		f.text(ln.text, ln.file)
	}
	got := f.b.String()
	if strings.TrimRight(n.Value, "\n") != got {
		return nil, false
	}
	return f.segs, true
}
