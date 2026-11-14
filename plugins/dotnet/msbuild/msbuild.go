// Package msbuild reads .NET project files the way MSBuild evaluates the parts
// Cernly needs: project discovery, SDK-style detection, the
// Directory.Build.props and Directory.Packages.props chain, properties,
// PackageReference, ProjectReference and packages.config. It never runs
// MSBuild: what it cannot determine stays as written and is marked unresolved.
package msbuild

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
)

type Project struct {
	Path, Dir   string // repo paths, "/" separators
	Lang        string // csharp | fsharp | vb, from the extension
	SDKStyle    bool
	Name        string            // file name without extension
	Props       map[string]string // lower-cased name -> value: the Directory.Build.props chain, then Directory.Packages.props, then the project (project wins); conditional or unresolvable properties are absent
	Packages    []PackageRef
	ProjectRefs []ProjectRef
	CPM         bool // ManagePackageVersionsCentrally resolved true
	IsTest      bool // IsTestProject true, or references Microsoft.NET.Test.Sdk unconditionally
}

type PackageRef struct {
	ID, Version string // Version as resolved; raw when unresolved; "" when several Candidates remain
	Raw         string // as written
	Unresolved  bool
	Candidates  []Candidate     // the version sources that may apply, in source order, when no single one is decided
	Condition   string          // the Conditions of the item and its enclosing groups and Whens, joined with " and "; empty when decided true (condition.go)
	PrivateAll  bool            // PrivateAssets="all" (attribute or child)
	Global      bool            // from GlobalPackageReference
	Ev          []fact.Evidence // the reference line, plus the PackageVersion line when CPM supplied it
}

type ProjectRef struct {
	Include, Path string
	Line          int
	Condition     string // as PackageRef.Condition
} // Include as written, Path resolved repo path ("" when it leaves the repo)

// Candidate is one version source (a Version, VersionOverride, PackageVersion
// or Update) that may apply to a PackageRef: its raw version, the condition it
// is under ("" when unconditional or decided true), and its line.
type Candidate struct {
	Raw, Condition string
	Ev             fact.Evidence
}

// AnyCondition is the condition under which one of several items with these
// conditions applies: "" when any is unconditional, else the distinct ones
// joined with " or ", each in parentheses when there are several.
func AnyCondition(conds []string) string {
	var out []string
	seen := map[string]bool{}
	for _, c := range conds {
		if c == "" {
			return ""
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	for i, c := range out {
		out[i] = "(" + c + ")"
	}
	return strings.Join(out, " or ")
}

type PackagesConfigEntry struct {
	ID, Version string
	Dev         bool
	Line        int
}

var langs = map[string]string{".csproj": "csharp", ".fsproj": "fsharp", ".vbproj": "vb"}

const (
	buildProps    = "directory.build.props"
	packagesProps = "directory.packages.props"
)

// Projects reads every tracked .csproj, .fsproj and .vbproj, sorted by path.
// A file that does not parse is left out and reported; a props file that does
// not parse is reported once and contributes nothing.
//
// Evaluation follows MSBuild's order: the nearest Directory.Build.props (and
// the parents it imports with GetPathOfFileAbove), the nearest
// Directory.Packages.props likewise, then the project; all properties before
// any item. A property set under a Condition is unknown from then on, until
// an unconditional one sets it again. A Choose takes its first true When, or
// Otherwise, decided where it stands (choose). An item condition that can be
// decided exactly against those properties (condition.go) drops the item or
// becomes empty; any other stays as written. Several conditional version
// sources leave the version empty with every Candidate listed. Props files
// outside the repo are never seen.
func Projects(r *repo.Repo) ([]*Project, []fact.LocationError) {
	e := &reader{r: r, docs: map[string]*doc{}, byLower: map[string]string{}, setInRepo: map[string]bool{}}
	for _, f := range r.Files {
		if b := strings.ToLower(path.Base(f)); b == buildProps || b == packagesProps {
			e.byLower[strings.ToLower(f)] = f
		}
	}
	files := r.Match(func(b string) bool { return langs[strings.ToLower(path.Ext(b))] != "" })
	for _, file := range files {
		e.collectSet(e.load(file))
	}
	for _, f := range e.byLower {
		// Quietly: a props file outside every chain reports no parse error.
		if data, err := r.Read(f); err == nil {
			if root, err := parse(data); err == nil {
				e.collectSet(root)
			}
		}
	}
	var out []*Project
	for _, file := range files {
		if p := e.project(file); p != nil {
			out = append(out, p)
		}
	}
	fold := foldMatch(files)
	for _, p := range out {
		for i := range p.ProjectRefs {
			if ref := &p.ProjectRefs[i]; ref.Path != "" && !r.Has(ref.Path) {
				ref.Path = fold(ref.Path)
			}
		}
	}
	return out, e.errs
}

// foldMatch resolves a path the way Windows does, without regard to case: to
// the one tracked path that matches it so, or unchanged when none or several do.
func foldMatch(tracked []string) func(string) string {
	byFold := map[string][]string{}
	for _, f := range tracked {
		k := strings.ToLower(f)
		byFold[k] = append(byFold[k], f)
	}
	return func(p string) string {
		if m := byFold[strings.ToLower(p)]; len(m) == 1 {
			return m[0]
		}
		return p
	}
}

// PackagesConfig reads a packages.config: one entry per <package>.
func PackagesConfig(r *repo.Repo, file string) ([]PackagesConfigEntry, error) {
	data, err := r.Read(file)
	if err != nil {
		return nil, err
	}
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	var out []PackagesConfigEntry
	for _, n := range root.kids {
		if n.name != "package" {
			continue
		}
		out = append(out, PackagesConfigEntry{
			ID:      n.attr("id"),
			Version: n.attr("version"),
			Dev:     strings.EqualFold(strings.TrimSpace(n.attr("developmentdependency")), "true"),
			Line:    n.line,
		})
	}
	return out, nil
}

// node is one XML element. name is the lower-cased local name, since MSBuild
// treats element names case-insensitively; raw keeps it as written.
type node struct {
	name, raw string
	attrs     []xml.Attr
	text      strings.Builder
	line      int
	kids      []*node
}

func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}

func (n *node) has(name string) bool {
	for _, a := range n.attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return true
		}
	}
	return false
}

// meta returns item metadata given as an attribute or as a child element.
func (n *node) meta(name string) (string, bool) {
	if n.has(name) {
		return n.attr(name), true
	}
	lower := strings.ToLower(name)
	for _, k := range n.kids {
		if k.name == lower {
			return strings.TrimSpace(k.text.String()), true
		}
	}
	return "", false
}

// parse builds the element tree, each element with the physical line of its
// '<'. Lines count '\n' over the raw bytes, so CRLF is one line.
func parse(data []byte) (*node, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	var nl []int
	for i, b := range data {
		if b == '\n' {
			nl = append(nl, i)
		}
	}
	lineAt := func(off int64) int { return sort.SearchInts(nl, int(off)) + 1 }
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root *node
	var stack []*node
	for {
		off := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: strings.ToLower(t.Name.Local), raw: t.Name.Local, attrs: t.Attr, line: lineAt(off)}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no root element")
	}
	return root, nil
}

type doc struct{ root *node }

type reader struct {
	r       *repo.Repo
	docs    map[string]*doc
	byLower map[string]string // lower-cased path -> tracked props file
	errs    []fact.LocationError
	// setInRepo holds the lower-cased names of every property some project or
	// props file of the repo sets, under a condition or not. Only these read
	// as "" when unset; any other may come from the environment or the SDK.
	setInRepo map[string]bool
}

// fail records an error at file and line once, though both passes walk it.
func (e *reader) fail(file string, line int, msg string) {
	for _, le := range e.errs {
		if le.Path == file && le.Line == line && le.Message == msg {
			return
		}
	}
	e.errs = append(e.errs, fact.LocationError{Path: file, Line: line, Message: msg})
}

func (e *reader) collectSet(n *node) {
	if n == nil {
		return
	}
	for _, k := range n.kids {
		if n.name == "propertygroup" {
			e.setInRepo[k.name] = true
			continue
		}
		e.collectSet(k)
	}
}

func (e *reader) load(file string) *node {
	if d, ok := e.docs[file]; ok {
		return d.root
	}
	d := &doc{}
	e.docs[file] = d
	data, err := e.r.Read(file)
	if err == nil {
		d.root, err = parse(data)
	}
	if err != nil {
		le := fact.LocationError{Path: file, Message: err.Error()}
		if se, ok := err.(*xml.SyntaxError); ok {
			le.Line = se.Line
		}
		e.errs = append(e.errs, le)
	}
	return d.root
}

// nearest finds the props file named base (case-insensitively) in dir or the
// closest folder above it, stopping at the repo root.
func (e *reader) nearest(dir, base string) string {
	base = strings.ToLower(base)
	for {
		if f, ok := e.byLower[strings.ToLower(path.Join(dir, base))]; ok {
			return f
		}
		if dir == "." || dir == "" {
			return ""
		}
		dir = path.Dir(dir)
	}
}

// item is a raw item collected during the property pass, resolved after it.
// choose is the part of its condition that comes from the Choose branches
// around it: decided where the Choose stands, never again after the pass.
// cond is the rest (its ItemGroup's and its own), decided after the pass.
type item struct {
	n            *node
	file         string
	props        bool // from a props file, not the project
	choose, cond string
}

type eval struct {
	e       *reader
	props   map[string]string // lower-cased name -> value
	unknown map[string]bool
	items   []item
	seen    map[string]bool
}

func (v *eval) file(file string, props bool) {
	if v.seen[file] {
		return
	}
	v.seen[file] = true
	root := v.e.load(file)
	if root == nil {
		return
	}
	v.walk(root.kids, file, props, "")
}

func joinCond(outer, inner string) string {
	switch {
	case outer == "":
		return inner
	case inner == "":
		return outer
	}
	return outer + " and " + inner
}

// walk reads the elements of a file in order. choose is the condition the
// enclosing Choose branches put on them.
func (v *eval) walk(kids []*node, file string, props bool, choose string) {
	for _, n := range kids {
		c := n.attr("condition")
		switch n.name {
		case "propertygroup":
			for _, p := range n.kids {
				v.setProp(p, joinCond(joinCond(choose, c), p.attr("condition")))
			}
		case "itemgroup":
			for _, it := range n.kids {
				v.items = append(v.items, item{n: it, file: file, props: props, choose: choose, cond: joinCond(c, it.attr("condition"))})
			}
		case "choose":
			v.choose(n, file, props, choose)
		case "import":
			if props {
				v.importAbove(n.attr("project"), file)
			}
		}
	}
}

const (
	maxChooseBranches = 64
	maxChooseCond     = 4 << 10
)

// choose walks the branch MSBuild takes: the first When whose condition is
// true, else Otherwise. Each condition is decided here, with the properties
// set so far, as MSBuild decides it in the property pass. A false When is
// skipped. An undecided one is walked under its condition, and every branch
// after it depends on it being false: those carry its negation, a group
// decide never settles. A true When ends the Choose.
func (v *eval) choose(n *node, file string, props bool, outer string) {
	after := outer // the condition of reaching the next branch
	undecided := 0
	for _, w := range n.kids {
		switch w.name {
		case "when":
			wc := w.attr("condition")
			switch v.decide(wc) {
			case isFalse:
				continue
			case isTrue:
				v.walk(w.kids, file, props, after)
				return
			}
			// Each undecided branch lengthens every later condition, so a
			// hostile Choose would grow them quadratically. Past the caps
			// the rest of the Choose is skipped, with an error at it.
			if undecided++; undecided > maxChooseBranches || len(after) > maxChooseCond {
				v.e.fail(file, w.line, "Choose: more than "+strconv.Itoa(maxChooseBranches)+" undecided When branches or a condition over "+strconv.Itoa(maxChooseCond)+" bytes; rest skipped")
				return
			}
			v.walk(w.kids, file, props, joinCond(after, wc))
			after = joinCond(after, "!("+wc+")")
		case "otherwise":
			v.walk(w.kids, file, props, after)
			return
		}
	}
}

// importAbove follows the one import form MSBuild documents for chaining
// props files: $([MSBuild]::GetPathOfFileAbove('<name>', ...)), searched from
// the folder above the importing file. Other imports are ignored.
func (v *eval) importAbove(project, from string) {
	const fn = "GetPathOfFileAbove("
	i := strings.Index(project, fn)
	if i < 0 {
		return
	}
	arg := project[i+len(fn):]
	if j := strings.IndexAny(arg, ",)"); j >= 0 {
		arg = arg[:j]
	}
	name := strings.Trim(strings.TrimSpace(arg), `'"`)
	if name == "" || strings.ContainsAny(name, `$/\`) {
		return
	}
	dir := path.Dir(from)
	if dir == "." {
		return // the repo root: anything above is outside the repo
	}
	if f := v.e.nearest(path.Dir(dir), name); f != "" {
		v.file(f, true)
	}
}

func (v *eval) setProp(p *node, cond string) {
	key := p.name
	if cond != "" {
		delete(v.props, key)
		v.unknown[key] = true
		return
	}
	val, ok := v.expand(strings.TrimSpace(p.text.String()))
	if !ok {
		delete(v.props, key)
		v.unknown[key] = true
		return
	}
	delete(v.unknown, key)
	v.props[key] = val
}

// expand replaces $(Name) with known properties. Anything else MSBuild would
// compute (property functions, item lists, metadata, unknown names) makes the
// whole value unresolved.
func (v *eval) expand(s string) (string, bool) {
	if strings.Contains(s, "@(") || strings.Contains(s, "%(") {
		return s, false
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "$(")
		if i < 0 {
			b.WriteString(s)
			return b.String(), true
		}
		j := strings.IndexByte(s[i:], ')')
		if j < 0 {
			return "", false
		}
		name := s[i+2 : i+j]
		if !isName(name) {
			return "", false
		}
		p, ok := v.props[strings.ToLower(name)]
		if !ok || v.unknown[strings.ToLower(name)] {
			return "", false
		}
		b.WriteString(s[:i])
		b.WriteString(p)
		s = s[i+j+1:]
	}
}

func isName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (v *eval) prop(name string) string {
	return v.props[strings.ToLower(name)]
}

// addSource records a version source in order. An unconditional one
// overrides every source before it, as a later item or Update does in MSBuild.
func addSource(m map[string][]Candidate, id string, c Candidate) {
	id = strings.ToLower(id)
	if c.Condition == "" {
		m[id] = nil
	}
	m[id] = append(m[id], c)
}

func (e *reader) project(file string) *Project {
	root := e.load(file)
	if root == nil {
		return nil
	}
	name := path.Base(file)
	ext := path.Ext(name)
	p := &Project{
		Path: file,
		Dir:  repo.Dir(file),
		Lang: langs[strings.ToLower(ext)],
		Name: strings.TrimSuffix(name, ext),
	}
	p.SDKStyle = root.has("sdk")
	for _, k := range root.kids {
		if k.name == "sdk" || k.name == "import" && k.has("sdk") {
			p.SDKStyle = true
		}
	}
	if !p.SDKStyle {
		return p // only a sibling packages.config is read for these
	}

	v := &eval{e: e, props: map[string]string{}, unknown: map[string]bool{}, seen: map[string]bool{}}
	if f := e.nearest(p.Dir, "Directory.Build.props"); f != "" {
		v.file(f, true)
	}
	pkgProps := e.nearest(p.Dir, "Directory.Packages.props")
	if pkgProps != "" {
		v.file(pkgProps, true)
	}
	v.file(file, false)

	p.Props = v.props
	p.CPM = strings.EqualFold(v.prop("ManagePackageVersionsCentrally"), "true")

	central := map[string][]Candidate{} // lower-cased id -> PackageVersion
	updates := map[string][]Candidate{} // lower-cased id -> PackageReference Update in a props file
	var globals []PackageRef
	byID := map[string][]int{}   // lower-cased id -> indexes into p.Packages
	own := map[int][]Candidate{} // package index -> its own Version or VersionOverride
	upd := map[int][]Candidate{} // package index -> the project's Updates after it, in order
	for _, it := range v.items {
		switch v.decide(it.cond) {
		case isFalse:
			continue
		case isTrue:
			it.cond = ""
		}
		it.cond = joinCond(it.choose, it.cond)
		ev := fact.Evidence{Path: it.file, Line: it.n.line}
		switch it.n.name {
		case "packageversion":
			if id := it.n.attr("include"); id != "" {
				if raw, ok := it.n.meta("Version"); ok {
					addSource(central, id, Candidate{Raw: raw, Condition: it.cond, Ev: ev})
				}
			}
		case "globalpackagereference":
			id := it.n.attr("include")
			if id == "" {
				continue
			}
			raw, _ := it.n.meta("Version")
			g := PackageRef{ID: id, Condition: it.cond, Global: true, Ev: []fact.Evidence{ev}}
			v.resolve(&g, raw)
			g.PrivateAll = true // NuGet adds every GlobalPackageReference with PrivateAssets="All"
			globals = append(globals, g)
		case "packagereference":
			if id := it.n.attr("include"); id != "" {
				pr := PackageRef{ID: id, Condition: it.cond, PrivateAll: privateAll(it.n), Ev: []fact.Evidence{ev}}
				i := len(p.Packages)
				if raw, ok := it.n.meta("VersionOverride"); ok {
					own[i] = []Candidate{{Raw: raw, Ev: ev}}
				} else if raw, ok := it.n.meta("Version"); ok {
					own[i] = []Candidate{{Raw: raw, Ev: ev}}
				}
				byID[strings.ToLower(id)] = append(byID[strings.ToLower(id)], i)
				p.Packages = append(p.Packages, pr)
			} else if id := it.n.attr("update"); id != "" {
				raw, hasVer := it.n.meta("Version")
				if it.props {
					if hasVer {
						addSource(updates, id, Candidate{Raw: raw, Condition: it.cond, Ev: ev})
					}
					continue
				}
				// In the project, Update changes the items defined before it.
				for _, i := range byID[strings.ToLower(id)] {
					if hasVer {
						upd[i] = append(upd[i], Candidate{Raw: raw, Condition: it.cond, Ev: ev})
					}
					if pa, ok := it.n.meta("PrivateAssets"); ok {
						p.Packages[i].PrivateAll = strings.EqualFold(strings.TrimSpace(pa), "all")
					}
				}
			}
		case "projectreference":
			inc := it.n.attr("include")
			if inc == "" || it.props {
				continue
			}
			ref := ProjectRef{Include: inc, Line: it.n.line, Condition: it.cond}
			if s, ok := v.expand(strings.ReplaceAll(inc, `\`, "/")); ok {
				if rp, ok := repo.Join(p.Dir, s); ok {
					ref.Path = rp
				}
			}
			p.ProjectRefs = append(p.ProjectRefs, ref)
		}
	}
	// Sources in order: the reference's own version, else its PackageVersion
	// (NuGet matches it after evaluation, so a reference in a props file read
	// before Directory.Packages.props gets it too) and the props Updates;
	// then the project's Updates. An unconditional one overrides the ones
	// before it.
	over := func(list []Candidate, c Candidate) []Candidate {
		if c.Condition == "" {
			list = nil
		}
		return append(list, c)
	}
	for i := range p.Packages {
		pr := &p.Packages[i]
		id := strings.ToLower(pr.ID)
		cands := own[i]
		if len(cands) == 0 {
			if p.CPM {
				cands = append(cands, central[id]...)
			}
			for _, c := range updates[id] {
				cands = over(cands, c)
			}
		}
		for _, c := range upd[i] {
			cands = over(cands, c)
		}
		for _, c := range cands {
			if c.Ev != pr.Ev[0] {
				pr.Ev = append(pr.Ev, c.Ev)
			}
		}
		switch {
		case len(cands) == 0:
			pr.Unresolved = true // no version anywhere: Version and Raw stay empty
		case len(cands) == 1 && cands[0].Condition == "":
			v.resolve(pr, cands[0].Raw)
		default:
			// Never guess which condition holds: no version, every candidate.
			pr.Unresolved, pr.Candidates = true, cands
		}
	}
	if p.CPM {
		p.Packages = append(p.Packages, globals...)
	}
	p.IsTest = strings.EqualFold(v.prop("IsTestProject"), "true")
	for _, pr := range p.Packages {
		if strings.EqualFold(pr.ID, "Microsoft.NET.Test.Sdk") && pr.Condition == "" {
			p.IsTest = true
		}
	}
	return p
}

func (v *eval) resolve(pr *PackageRef, raw string) {
	pr.Raw = raw
	if val, ok := v.expand(raw); ok {
		pr.Version, pr.Unresolved = val, false
	} else {
		pr.Version, pr.Unresolved = raw, true
	}
}

func privateAll(n *node) bool {
	pa, _ := n.meta("PrivateAssets")
	return strings.EqualFold(strings.TrimSpace(pa), "all")
}
