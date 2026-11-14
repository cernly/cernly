package command

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/cernly/cernly/repo"
)

// This file reads a script's statements in order, without running anything,
// to know what a command line expands to where it is written: the arrays a
// script (or a file it sources, or a function it calls) has set up, the path
// variables it computed from its own location, and the functions it defined.
// Only literal values and copies of known arrays are followed; anything else
// makes the array unknown, so it stays unresolved rather than guessed.

// condRef is a condition an array element was added under, as written: the
// element exists only when the condition holds, which a scan cannot know.
type condRef struct {
	path   string
	line   int
	text   string
	parent *condRef // the condition this one is nested in, nil at the top
}

func (c *condRef) same(o *condRef) bool {
	return c.path == o.path && c.line == o.line && c.text == o.text
}

// governs reports whether c is one of the conditions of chain (itself or one
// it is nested in).
func governs(chain, c *condRef) bool {
	for x := chain; x != nil; x = x.parent {
		if x.same(c) {
			return true
		}
	}
	return false
}

// notGoverned is the innermost condition of elem that does not also hold for
// a call under chain; nil when every condition of elem holds there too.
func notGoverned(elem, chain *condRef) *condRef {
	for x := elem; x != nil; x = x.parent {
		if !governs(chain, x) {
			return x
		}
	}
	return nil
}

// joinCond is inner nested in outer: an element added under inner, copied
// into another array under outer.
func joinCond(inner, outer *condRef) *condRef {
	if inner == nil {
		return outer
	}
	if outer == nil {
		return inner
	}
	c := *inner
	c.parent = joinCond(inner.parent, outer)
	return &c
}

// funcDef is a function definition and the file it is written in.
type funcDef struct {
	decl *syntax.FuncDecl
	fr   frame
}

// frame is the file a statement is written in.
type frame struct {
	path   string
	render func(*syntax.Word) string
	lineOf func(syntax.Pos) int
	src    []byte // the parsed text, for a condition's text as written
}

func (fr frame) text(n syntax.Node) string {
	s, e := int(n.Pos().Offset()), int(n.End().Offset())
	if s < 0 || e > len(fr.src) || s >= e {
		return ""
	}
	return strings.TrimSuffix(strings.Join(strings.Fields(string(fr.src[s:e])), " "), ";")
}

// shEnv is what is known at one point of a script.
type shEnv struct {
	vars   map[string]string // path variables, repo-relative ("." is the root)
	arrays map[string][]arg
	bad    map[string]bool // arrays that cannot be read
	funcs  map[string]funcDef
	self   string   // BASH_SOURCE: the file being read
	script string   // $0: the script that was started
	cond   *condRef // in a snapshot: the condition the call itself runs under
	top    *condRef // in a snapshot: the condition of the script's top-level flow at the call
}

func newEnv(self string) *shEnv {
	return &shEnv{vars: map[string]string{}, arrays: map[string][]arg{}, bad: map[string]bool{}, funcs: map[string]funcDef{}, self: self, script: self}
}

// clone copies the maps; slices are never changed in place, so they are shared.
func (e *shEnv) clone() *shEnv {
	c := &shEnv{vars: make(map[string]string, len(e.vars)), arrays: make(map[string][]arg, len(e.arrays)),
		bad: make(map[string]bool, len(e.bad)), funcs: make(map[string]funcDef, len(e.funcs)), self: e.self, script: e.script}
	for k, v := range e.vars {
		c.vars[k] = v
	}
	for k, v := range e.arrays {
		c.arrays[k] = v
	}
	for k, v := range e.bad {
		c.bad[k] = v
	}
	for k, v := range e.funcs {
		c.funcs[k] = v
	}
	return c
}

// loader parses the repo files that scripts source, once per scan.
type loader struct {
	r     *repo.Repo
	files map[string]*parsedFile
}

type parsedFile struct {
	f   *syntax.File
	src []byte
}

func newLoader(r *repo.Repo) *loader { return &loader{r: r, files: map[string]*parsedFile{}} }

func (ld *loader) parse(p string) *parsedFile {
	if ld == nil || ld.r == nil {
		return nil
	}
	if pf, ok := ld.files[p]; ok {
		return pf
	}
	ld.files[p] = nil
	data, err := ld.r.Read(p)
	if err != nil || len(data) > maxScript {
		return nil
	}
	f, err := parseShell(data, p)
	if err != nil {
		return nil
	}
	ld.files[p] = &parsedFile{f: f, src: data}
	return ld.files[p]
}

func (ld *loader) has(p string) bool { return ld != nil && ld.r != nil && ld.r.Has(p) }

// interp reads statements in order. It records, for every call it passes,
// what was known just before it.
type interp struct {
	ld     *loader
	calls  map[*syntax.CallExpr]*shEnv
	active map[string]bool // functions and files being read, against recursion
	steps  int
	top    *condRef // condition of the top-level flow at the statement being read
}

const maxSteps = 20000 // statements read per snippet; beyond it the rest stays unknown

// result of one statement: stop when it ends the list it is in (return,
// exit), cond when the statements after it run only under a condition
// (`check || return 0`).
type result struct {
	stop bool
	cond *condRef
}

func (in *interp) run(stmts []*syntax.Stmt, e *shEnv, fr frame, cond *condRef) bool {
	for _, s := range stmts {
		r := in.stmt(s, e, fr, cond)
		if r.stop {
			return true
		}
		if r.cond != nil {
			cond = r.cond // nested in the previous one
		}
	}
	return false
}

func (in *interp) condOf(fr frame, n syntax.Node, outer *condRef) *condRef {
	return &condRef{path: fr.path, line: fr.lineOf(n.Pos()), text: fr.text(n), parent: outer}
}

func (in *interp) stmt(s *syntax.Stmt, e *shEnv, fr frame, cond *condRef) result {
	in.steps++
	if in.steps > maxSteps || s == nil || s.Background {
		return result{}
	}
	switch x := s.Cmd.(type) {
	case *syntax.CallExpr:
		return in.call(x, e, fr, cond)
	case *syntax.DeclClause:
		if x.Variant != nil && x.Variant.Value != "local" {
			for _, a := range x.Args {
				in.assign(a, e, fr, cond)
			}
		}
	case *syntax.FuncDecl:
		if x.Name != nil {
			e.funcs[x.Name.Value] = funcDef{decl: x, fr: fr}
		}
	case *syntax.BinaryCmd:
		if x.Op != syntax.AndStmt && x.Op != syntax.OrStmt {
			return result{}
		}
		if r := in.stmt(x.X, e, fr, cond); r.stop {
			return r
		}
		c := in.condOf(fr, x, cond)
		if isReturn(x.Y) {
			return result{cond: c} // `check || return 0`: the rest runs only when check passes
		}
		r := in.stmt(x.Y, e, fr, c)
		if r.stop || r.cond != nil {
			return result{cond: c}
		}
	case *syntax.IfClause:
		c := cond
		if len(x.Cond) > 0 {
			c = &condRef{path: fr.path, line: fr.lineOf(x.Pos()), text: "if " + fr.text(x.Cond[len(x.Cond)-1]), parent: cond}
		}
		ended := false
		for b := x; b != nil; b = b.Else {
			if in.run(b.Then, e, fr, c) {
				ended = true
			}
		}
		if ended {
			return result{cond: c}
		}
	case *syntax.Block:
		if in.run(x.Stmts, e, fr, cond) {
			return result{stop: true}
		}
	case *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
		c := in.condOf(fr, x, cond)
		syntax.Walk(x, func(n syntax.Node) bool {
			switch y := n.(type) {
			case *syntax.Stmt:
				if y != s {
					in.stmt(y, e, fr, c)
					return false
				}
			}
			return true
		})
	}
	return result{}
}

func isReturn(s *syntax.Stmt) bool {
	c, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(c.Args) == 0 {
		return false
	}
	switch wordText(c.Args[0]) {
	case "return", "exit":
		return true
	}
	return false
}

func (in *interp) call(x *syntax.CallExpr, e *shEnv, fr frame, cond *condRef) result {
	if _, seen := in.calls[x]; !seen {
		snap := e.clone()
		snap.cond, snap.top = cond, in.top
		in.calls[x] = snap
	}
	if len(x.Args) == 0 {
		for _, a := range x.Assigns {
			in.assign(a, e, fr, cond)
		}
		return result{}
	}
	if !isLiteral(x.Args[0]) {
		return result{}
	}
	name := fr.render(x.Args[0])
	switch name {
	case "return", "exit":
		return result{stop: true}
	case "source", ".":
		if len(x.Args) < 2 {
			return result{}
		}
		p, ok := in.sourcePath(x.Args[1], e)
		if !ok || in.active["file:"+p] {
			return result{}
		}
		pf := in.ld.parse(p)
		if pf == nil {
			return result{}
		}
		in.active["file:"+p] = true
		self := e.self
		e.self = p
		in.run(pf.f.Stmts, e, frame{path: p, render: wordText, lineOf: func(q syntax.Pos) int { return int(q.Line()) }, src: pf.src}, cond)
		e.self = self
		delete(in.active, "file:"+p)
		return result{}
	}
	fd, ok := e.funcs[name]
	if !ok || in.active["func:"+name] || len(in.active) > 16 {
		return result{}
	}
	in.active["func:"+name] = true
	in.stmt(fd.decl.Body, e, fd.fr, cond) // a return ends the function only
	delete(in.active, "func:"+name)
	return result{}
}

// sourcePath is the repo file a source statement reads: a literal path from
// the repo root or the sourcing file's folder, or a path under a variable the
// script set from its own location.
func (in *interp) sourcePath(w *syntax.Word, e *shEnv) (string, bool) {
	v, ok := e.evalPath(w)
	if !ok {
		return "", false
	}
	bases := []string{"."}
	if isLiteral(w) {
		bases = append(bases, path.Dir(e.self))
	}
	for _, b := range bases {
		if p, ok := repo.Join(b, v); ok && in.ld.has(p) {
			return p, true
		}
	}
	return "", false
}

func (in *interp) assign(a *syntax.Assign, e *shEnv, fr frame, cond *condRef) {
	if a == nil || a.Name == nil || a.Naked {
		return
	}
	name := a.Name.Value
	if a.Array == nil {
		if a.Index != nil {
			e.bad[name] = true
			delete(e.arrays, name)
			return
		}
		delete(e.vars, name)
		if v, ok := e.evalPath(a.Value); ok && !a.Append && cond == nil {
			e.vars[name] = v
		}
		return
	}
	var elems []arg
	ok := true
	for _, el := range a.Array.Elems {
		if el.Index != nil || el.Value == nil {
			ok = false
			break
		}
		t := fr.render(el.Value)
		if isLiteral(el.Value) {
			elems = append(elems, arg{text: t, literal: true, word: el.Value, at: &hop{path: fr.path, line: fr.lineOf(el.Value.Pos())}, cond: cond})
			continue
		}
		ref, isRef := arrayRef(t)
		src, known := e.arrays[ref]
		if !isRef || !known || e.bad[ref] {
			ok = false
			break
		}
		for _, s := range src {
			s.cond = joinCond(s.cond, cond)
			elems = append(elems, s)
		}
	}
	prev, had := e.arrays[name]
	switch {
	case !ok || e.bad[name]:
		e.bad[name] = true
		delete(e.arrays, name)
	case a.Append:
		e.arrays[name] = append(append([]arg(nil), prev...), elems...)
	case cond != nil && (had && fmtArgs(prev) != fmtArgs(elems)):
		e.bad[name] = true // replaced under a condition: either value may hold
		delete(e.arrays, name)
	default:
		e.arrays[name] = elems
	}
}

// evalPath reads a word that names a path: literals, path variables, the
// script's own location (${BASH_SOURCE[0]}, $0) and the usual ways to turn
// it into a folder ($(dirname …), $(cd … && pwd), ${x%/*}, git rev-parse
// --show-toplevel). The result is repo-relative; false when any part cannot
// be read or the path leaves the repo.
func (e *shEnv) evalPath(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	if !e.pathParts(&b, w.Parts) {
		return "", false
	}
	s := b.String()
	if s == "" || strings.HasPrefix(s, "/") {
		return "", false
	}
	c := path.Clean(s)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func (e *shEnv) pathParts(b *strings.Builder, parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if !e.pathParts(b, p.Parts) {
				return false
			}
		case *syntax.ParamExp:
			v, ok := e.param(p)
			if !ok {
				return false
			}
			b.WriteString(v)
		case *syntax.CmdSubst:
			v, ok := e.subst(p)
			if !ok {
				return false
			}
			b.WriteString(v)
		default:
			return false
		}
	}
	return true
}

func (e *shEnv) param(p *syntax.ParamExp) (string, bool) {
	if p.Param == nil || p.Excl || p.Length || p.Width || p.Slice != nil || p.Repl != nil || p.Names != 0 {
		return "", false
	}
	var v string
	switch name := p.Param.Value; {
	case name == "BASH_SOURCE":
		if p.Index != nil {
			if w, ok := p.Index.(*syntax.Word); !ok || wordText(w) != "0" {
				return "", false
			}
		}
		v = e.self
	case p.Index != nil:
		return "", false
	case name == "0":
		v = e.script
	default:
		var ok bool
		if v, ok = e.vars[name]; !ok {
			return "", false
		}
	}
	if v == "" {
		return "", false
	}
	if p.Exp != nil {
		if p.Exp.Op != syntax.RemSmallSuffix || wordText(p.Exp.Word) != "/*" {
			return "", false
		}
		v = path.Dir(v)
	}
	return v, true
}

func (e *shEnv) subst(cs *syntax.CmdSubst) (string, bool) {
	if len(cs.Stmts) != 1 {
		return "", false
	}
	switch x := cs.Stmts[0].Cmd.(type) {
	case *syntax.CallExpr:
		return e.pathCmd(x)
	case *syntax.BinaryCmd:
		// cd <dir> && pwd
		if x.Op != syntax.AndStmt {
			return "", false
		}
		cd, ok1 := x.X.Cmd.(*syntax.CallExpr)
		pwd, ok2 := x.Y.Cmd.(*syntax.CallExpr)
		if !ok1 || !ok2 || len(pwd.Args) == 0 || wordText(pwd.Args[0]) != "pwd" || len(pwd.Args) > 2 {
			return "", false
		}
		args := plainArgs(cd.Args)
		if len(args) != 2 || wordText(args[0]) != "cd" {
			return "", false
		}
		return e.evalPath(args[1])
	}
	return "", false
}

// plainArgs drops "--", -P and -L, which do not change the path read here.
func plainArgs(ws []*syntax.Word) []*syntax.Word {
	var out []*syntax.Word
	for i, w := range ws {
		if i > 0 {
			switch wordText(w) {
			case "--", "-P", "-L", "-e", "-m", "-f":
				continue
			}
		}
		out = append(out, w)
	}
	return out
}

func (e *shEnv) pathCmd(x *syntax.CallExpr) (string, bool) {
	args := plainArgs(x.Args)
	if len(args) == 0 {
		return "", false
	}
	switch wordText(args[0]) {
	case "dirname":
		if len(args) == 2 {
			if v, ok := e.evalPath(args[1]); ok {
				return path.Dir(v), true
			}
		}
	case "realpath", "readlink":
		if len(args) == 2 {
			return e.evalPath(args[1])
		}
	case "git":
		if len(args) == 3 && wordText(args[1]) == "rev-parse" && wordText(args[2]) == "--show-toplevel" {
			return ".", true
		}
	}
	return "", false
}

// readEnv reads a parsed text and returns what was known before each call it
// passed. self is the file's repo path for a script, "" otherwise.
func readEnv(ld *loader, f *syntax.File, fr frame, self string) map[*syntax.CallExpr]*shEnv {
	in := &interp{ld: ld, calls: map[*syntax.CallExpr]*shEnv{}, active: map[string]bool{}}
	e := newEnv(self)
	var cond *condRef
	stopped := false
	fallback := map[*syntax.CallExpr]*shEnv{}
	for _, s := range f.Stmts {
		before := e.clone()
		before.cond, before.top = cond, cond
		in.top = cond
		// Calls the reading never passes (the body of a function nobody
		// calls) see what was known before their top-level statement. A
		// function body the reading runs later, at its call, gets the state
		// of that call instead, so this is applied only at the end.
		syntax.Walk(s, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				fallback[c] = before
			}
			return true
		})
		if !stopped {
			r := in.stmt(s, e, fr, cond)
			stopped = r.stop
			if r.cond != nil {
				cond = r.cond
			}
		}
	}
	for c, env := range fallback {
		if _, seen := in.calls[c]; !seen {
			in.calls[c] = env
		}
	}
	return in.calls
}

// wrapperCalls are the calls in a function body that pass "$@" on.
func wrapperCalls(fd *syntax.FuncDecl) []*syntax.CallExpr {
	var out []*syntax.CallExpr
	syntax.Walk(fd.Body, func(m syntax.Node) bool {
		if c, ok := m.(*syntax.CallExpr); ok {
			for _, w := range c.Args {
				if wordText(w) == "$@" {
					out = append(out, c)
					break
				}
			}
		}
		return true
	})
	return out
}
