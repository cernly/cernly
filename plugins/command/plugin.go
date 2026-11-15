package command

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/composefile"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

// Plugin reports image builds and the stacks found in commands: compose,
// kubectl and kustomize (tool kubernetes), helm.
type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "commands" }
func (Plugin) Version() string { return "0.3.3" }

// Watches is true for every path: commands name arbitrary files (Dockerfiles,
// build contexts, compose files), so any added or removed file can change how
// a command resolves.
func (Plugin) Watches(string) bool { return true }

// hop is one link in the chain from a workflow to a command: the file line
// that made the call.
type hop struct {
	path string
	line int
	note string // an assumption the link rests on, in words; "" for none
}

type reach struct {
	workflows map[string]workflowInfo
	chain     []hop // first chain found, workflow line first
}

type state struct {
	r        *repo.Repo
	src      *sources
	found    []found
	edges    map[*snippet][]edge
	reachd   map[*snippet]*reach
	testErrs []fact.LocationError
}

type edge struct {
	to  *snippet
	hop hop
}

func (Plugin) Run(r *repo.Repo) plugin.Result {
	st := &state{r: r, src: collect(r), edges: map[*snippet][]edge{}, reachd: map[*snippet]*reach{}}
	ld := newLoader(r)
	var res plugin.Result
	res.Errors = append(res.Errors, st.src.errs...)
	for _, sn := range st.src.snippets {
		fs, err := scanIn(sn, ld)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: sn.path, Line: sn.line, Message: "shell parse: " + err.Error()})
		}
		st.found = append(st.found, fs...)
	}
	st.found = dedupeFound(st.found)
	st.link()
	st.walk()
	res.Facts = append(res.Facts, st.buildFacts()...)
	res.Facts = append(res.Facts, st.stackFacts()...)
	res.Facts = append(res.Facts, st.deployFacts()...)
	res.Facts = append(res.Facts, st.testFacts()...)
	res.Errors = append(res.Errors, st.testErrs...)
	return res
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func dedupeFound(fs []found) []found {
	seen := map[string]bool{}
	var out []found
	for _, f := range fs {
		k := fmt.Sprintf("%s:%d:%s:%+v:%+v:%+v:%+v:%+v", f.snip.path, f.line, f.cwd, deref(f.build), deref(f.compose), deref(f.deploy), deref(f.test), deref(f.invoke))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// resolveAt is resolve (or resolveDir) for a command that a literal `cd`
// moved to cwd: relative to cwd first, as the shell reads it.
func (st *state) resolveAt(cwd, raw, fromDir string, dir bool) (string, bool) {
	if cwd != "" && raw != "" {
		if p, ok := repo.Join(cwd, raw); ok && (st.r.Has(p) || dir && st.hasDir(p)) {
			return p, true
		}
	}
	if dir {
		return st.resolveDir(raw, fromDir)
	}
	return st.resolve(raw, fromDir)
}

func (st *state) hasDir(p string) bool {
	if p == "." {
		return true
	}
	for _, f := range st.r.Files {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// resolve finds a tracked file for a path written in a command: relative to
// the repo root first, then to the folder of the file that holds the command.
func (st *state) resolve(raw, fromDir string) (string, bool) {
	if raw == "" {
		return "", false
	}
	for _, base := range []string{".", fromDir} {
		if p, ok := repo.Join(base, raw); ok && st.r.Has(p) {
			return p, true
		}
	}
	return raw, false
}

func (st *state) resolveDir(raw, fromDir string) (string, bool) {
	for _, base := range []string{".", fromDir} {
		p, ok := repo.Join(base, raw)
		if !ok {
			continue
		}
		if p == "." {
			return p, true
		}
		for _, f := range st.r.Files {
			if strings.HasPrefix(f, p+"/") {
				return p, true
			}
		}
	}
	return raw, false
}

// link turns invocations into edges between snippets.
func (st *state) link() {
	byScript := map[string][]*snippet{}
	byUnit := map[string][]*snippet{} // kind|file|unit
	for _, sn := range st.src.snippets {
		switch sn.kind {
		case srcScript:
			byScript[sn.path] = append(byScript[sn.path], sn)
		default:
			k := sn.kind + "|" + sn.path + "|" + sn.unit
			byUnit[k] = append(byUnit[k], sn)
		}
	}
	unitFile := func(dir string, names ...string) string {
		for _, n := range names {
			if p, ok := repo.Join(dir, n); ok && st.r.Has(p) {
				return p
			}
		}
		return ""
	}
	taskfiles := []string{"Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"}
	makefiles := []string{"GNUmakefile", "makefile", "Makefile"}
	for _, f := range st.found {
		inv := f.invoke
		if inv == nil || !inv.literal {
			continue
		}
		from := f.snip
		dir := path.Dir(from.path)
		h := hop{path: from.path, line: f.line}
		var targets []*snippet
		switch inv.kind {
		case srcScript:
			if p, ok := st.resolve(inv.names[0], dir); ok {
				targets = byScript[p]
				if f.remoteCd != nil {
					h.note = remoteNote(f.remoteCd, p+" is read")
				}
			}
		case srcTask, srcMake:
			base := dir
			if from.kind == srcWorkflow {
				base = "."
			}
			if inv.dir != "" {
				if p, ok := repo.Join(base, inv.dir); ok {
					base = p
				}
			}
			candidates := taskfiles
			if inv.kind == srcMake {
				candidates = makefiles
			}
			file := unitFile(base, candidates...)
			for _, n := range inv.names {
				targets = append(targets, byUnit[inv.kind+"|"+file+"|"+n]...)
			}
		case srcNpm:
			base := dir
			if from.kind == srcWorkflow {
				base = "."
			}
			if p, ok := repo.Join(base, "package.json"); ok {
				targets = byUnit[srcNpm+"|"+p+"|"+inv.names[0]]
			}
		}
		for _, t := range targets {
			if t != from {
				st.edges[from] = append(st.edges[from], edge{to: t, hop: h})
			}
		}
	}
	for _, tr := range st.src.taskRefs {
		callers := byUnit[srcTask+"|"+tr.path+"|"+tr.task]
		callees := byUnit[srcTask+"|"+tr.path+"|"+tr.callee]
		for _, c := range callers {
			for _, t := range callees {
				st.edges[c] = append(st.edges[c], edge{to: t, hop: hop{path: tr.path, line: tr.line}})
			}
		}
	}
}

// walk records, for every snippet a workflow reaches, which workflows reach
// it and the first chain of calls that got there.
func (st *state) walk() {
	for _, sn := range st.src.snippets {
		if sn.wf == nil {
			continue
		}
		type item struct {
			s     *snippet
			chain []hop
		}
		queue := []item{{sn, []hop{{path: sn.path, line: sn.line}}}}
		visited := map[*snippet]bool{}
		for len(queue) > 0 {
			it := queue[0]
			queue = queue[1:]
			if visited[it.s] {
				continue
			}
			visited[it.s] = true
			rc := st.reachd[it.s]
			if rc == nil {
				rc = &reach{workflows: map[string]workflowInfo{}, chain: it.chain}
				st.reachd[it.s] = rc
			}
			if _, ok := rc.workflows[sn.wf.id()]; !ok { // the first step that reaches it
				w := *sn.wf
				w.Start = sn.step
				rc.workflows[sn.wf.id()] = w
			}
			for _, e := range st.edges[it.s] {
				chain := append(append([]hop(nil), it.chain...), e.hop)
				queue = append(queue, item{e.to, chain})
			}
		}
	}
}

// remoteNote says what a link through a remote `cd` assumes: the folder on
// the host is a checkout of this repo, so a path the remote command names
// is read from the repo root.
func remoteNote(cd *hop, subject string) string {
	return fmt.Sprintf("remote command after %s: %s from the repo root, assuming the remote folder is a checkout of this repo", cd.note, subject)
}

func (st *state) evidence(f found, extra ...hop) []fact.Evidence {
	ev := []fact.Evidence{{Path: f.snip.path, Line: f.line, EndLine: f.endLine}}
	for _, h := range f.via {
		ev = append(ev, fact.Evidence{Path: h.path, Line: h.line})
	}
	for _, h := range extra {
		ev = append(ev, fact.Evidence{Path: h.path, Line: h.line})
	}
	return ev
}

func (st *state) reachOf(sn *snippet, withSteps bool) ([]hop, []any) {
	rc := st.reachd[sn]
	if rc == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(rc.workflows))
	for id := range rc.workflows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var wfs []any
	for _, id := range ids {
		wfs = append(wfs, rc.workflows[id].ref(withSteps))
	}
	return rc.chain, wfs
}

// imageRepo is the last literal path segment of an image reference, without
// tag or digest: "${REGISTRY}/ns/acme-api:${tag}" -> "acme-api".
func imageRepo(ref string) string { return imageref.Repo(ref) }

type buildAgg struct {
	attrs    map[string]any
	tags     map[string]bool
	contexts map[string]bool
	wfs      map[string]any
	ev       []fact.Evidence
	ci       bool
	push     map[string]bool // "true", "false", "" (unknown) per build command
}

func (st *state) buildFacts() []fact.Fact {
	aggs := map[string]*buildAgg{}
	var order []string
	add := func(tool, from, cwd, rawFile, rawCtx, target string, tags []string, literal bool, push string, ev []fact.Evidence, wfs []any) {
		dir := path.Dir(from)
		ctx, ctxOK := rawCtx, false
		if literal {
			ctx, ctxOK = st.resolveAt(cwd, rawCtx, dir, true)
		}
		file := rawFile
		fileOK := false
		if file == "" && ctxOK {
			file, fileOK = st.resolve(path.Join(ctx, "Dockerfile"), ".")
		} else if literal {
			file, fileOK = st.resolveAt(cwd, rawFile, dir, false)
			if !fileOK && ctxOK {
				file, fileOK = st.resolve(path.Join(ctx, rawFile), ".")
			}
		}
		if file == "" {
			file = "Dockerfile"
		}
		repos := map[string][]string{}
		for _, t := range tags {
			if r := imageRepo(t); r != "" {
				repos[r] = append(repos[r], t)
			}
		}
		for name, refs := range repos {
			tgt := target
			if tgt == "" {
				tgt = "_"
			}
			key := "image_build:" + name + ":" + file + ":" + tgt
			a := aggs[key]
			if a == nil {
				a = &buildAgg{attrs: map[string]any{"image_repo": name, "dockerfile": file, "dockerfile_resolved": fileOK, "tool": tool},
					tags: map[string]bool{}, contexts: map[string]bool{}, wfs: map[string]any{}, push: map[string]bool{}}
				if target != "" {
					a.attrs["target"] = target
				}
				aggs[key] = a
				order = append(order, key)
			}
			for _, r := range refs {
				a.tags[r] = true
			}
			a.contexts[redact.Userinfo(ctx)] = true // a remote context without its credentials
			a.push[push] = true
			a.ev = append(a.ev, ev...)
			for _, w := range wfs {
				m := w.(map[string]any)
				a.wfs[wfKey(m)] = w
				a.ci = true
			}
		}
	}
	for _, f := range st.found {
		if f.build == nil {
			continue
		}
		chain, wfs := st.reachOf(f.snip, true)
		b := f.build
		push := "false"
		if b.push {
			push = "true"
		}
		add("docker build", f.snip.path, f.cwd, b.dockerfile, b.context, b.target, b.tags, b.literalFile && b.literalContext, push, st.evidence(f, chain...), wfs)
	}
	for _, b := range st.src.builds {
		var wfs []any
		if b.wf != nil {
			w := *b.wf
			w.Start = b.step
			wfs = []any{w.ref(true)}
		}
		ev := []fact.Evidence{{Path: b.path, Line: b.line, EndLine: b.endLine}}
		if ev[0].EndLine <= ev[0].Line {
			ev[0].EndLine = 0
		}
		add(b.tool, b.path, "", b.dockerfile, b.context, b.target, b.tags, true, b.push, ev, wfs)
	}
	var out []fact.Fact
	for _, key := range order {
		a := aggs[key]
		a.attrs["tags"] = fact.SortedSet(a.tags)
		a.attrs["contexts"] = fact.SortedSet(a.contexts)
		a.attrs["ci_invoked"] = a.ci
		// push: the build command itself pushes (true when any does), or none
		// does (false when every one is known); a later docker push is for
		// the rules, from the steps after the build.
		switch {
		case a.push["true"]:
			a.attrs["push"] = true
		case !a.push[""]:
			a.attrs["push"] = false
		}
		if len(a.wfs) > 0 {
			a.attrs["workflows"] = fact.SortedValues(a.wfs)
		}
		out = append(out, fact.Fact{Kind: fact.KindImageBuild, Key: key, Attrs: a.attrs, Plugin: "commands", Evidence: a.ev})
	}
	return out
}

type stackAgg struct {
	files, unresolved []string
	envFiles          map[string]bool
	defaults, starts  bool
	wfs               map[string]any
	wfStarts          map[string]bool // wfs entry comes from a command that starts the stack
	ev                []fact.Evidence
	envNames          map[string]bool
	conditional       map[string]map[string]any // file|line -> conditional_files entry
	assumptions       map[string]map[string]any // path|line|text -> assumptions entry
}

func (st *state) stackFacts() []fact.Fact {
	aggs := map[string]*stackAgg{}
	for _, f := range st.found {
		c := f.compose
		if c == nil {
			continue
		}
		dir := path.Dir(f.snip.path)
		var files, unresolved []string
		defaults := false
		unresolved = append(unresolved, c.unknown...)
		if len(c.files) == 0 && (len(c.unknown) > 0 || len(c.condFiles) > 0) {
			// Files come from an expansion we cannot read, or only under a
			// condition: never assume compose's default files.
			continue
		}
		if len(c.files) == 0 {
			defaults = true
			for _, group := range [][]string{composefile.Default, composefile.Override} {
				for _, n := range group {
					if p, ok := repo.Join(f.cwd, n); ok && st.r.Has(p) {
						files = append(files, p)
						break
					}
				}
			}
		}
		for _, raw := range c.files {
			p, ok := st.resolveAt(f.cwd, raw, dir, false)
			if !ok || !c.literalFiles {
				unresolved = append(unresolved, p)
			}
			files = append(files, p)
		}
		if len(files) == 0 {
			continue
		}
		id := strings.Join(files, "\x00")
		a := aggs[id]
		if a == nil {
			a = &stackAgg{files: files, unresolved: unresolved, envFiles: map[string]bool{}, defaults: defaults, wfs: map[string]any{}, wfStarts: map[string]bool{}, envNames: map[string]bool{},
				conditional: map[string]map[string]any{}, assumptions: map[string]map[string]any{}}
			aggs[id] = a
		}
		for _, cf := range c.condFiles {
			p, ok := st.resolveAt(f.cwd, cf.raw, dir, false)
			e := map[string]any{"file": p, "resolved": ok, "condition": cf.cond.text, "condition_path": cf.cond.path, "condition_line": cf.cond.line}
			if cf.at.path != "" {
				e["path"], e["line"] = cf.at.path, cf.at.line
			}
			a.conditional[fmt.Sprintf("%s|%s|%d", p, cf.at.path, cf.at.line)] = e
		}
		if f.remoteCd != nil && len(c.files) > 0 {
			addAssumption(a.assumptions, hop{path: f.snip.path, line: f.line, note: remoteNote(f.remoteCd, "its compose files are read")})
		}
		for _, e := range c.envFiles {
			a.envFiles[path.Base(e)] = true
		}
		starts := false
		switch c.sub {
		case "up", "create", "start", "restart", "run":
			a.starts, starts = true, true
		}
		chain, wfs := st.reachOf(f.snip, true)
		a.ev = append(a.ev, st.evidence(f, chain...)...)
		for _, h := range chain {
			if h.note != "" {
				addAssumption(a.assumptions, h)
			}
		}
		for _, w := range wfs {
			m := w.(map[string]any)
			id := wfKey(m)
			// A job's entry names the first step that starts the stack, else
			// the first step that reaches it (snippets come in file order).
			if _, seen := a.wfs[id]; !seen || starts && !a.wfStarts[id] {
				a.wfs[id] = w
				a.wfStarts[id] = starts
			}
			if e, ok := m["environment"].(string); ok {
				a.envNames[e] = true
			}
		}
	}
	ids := make([]string, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	used := map[string]int{}
	var out []fact.Fact
	for _, id := range ids {
		a := aggs[id]
		name := composefile.StackName(a.files)
		if envs := fact.SortedSet(a.envNames); len(envs) == 1 {
			name = envs[0].(string)
		}
		used[name]++
		if used[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, used[name])
		}
		attrs := map[string]any{
			"name": name, "tool": "compose", "source": "invocation", "files": fact.Strings(a.files), "default_files": a.defaults,
			"starts": a.starts, "ci_invoked": len(a.wfs) > 0,
		}
		if len(a.unresolved) > 0 {
			attrs["unresolved"] = fact.Strings(a.unresolved)
		}
		if len(a.envFiles) > 0 {
			attrs["env_files"] = fact.SortedSet(a.envFiles)
		}
		if len(a.wfs) > 0 {
			attrs["workflows"] = fact.SortedValues(withStarts(a.wfs, a.wfStarts))
		}
		if len(a.conditional) > 0 {
			attrs["conditional_files"] = fact.SortedValues(a.conditional)
		}
		if len(a.assumptions) > 0 {
			attrs["assumptions"] = fact.SortedValues(a.assumptions)
		}
		out = append(out, fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + name, Attrs: attrs, Plugin: "commands", Evidence: a.ev})
	}
	return out
}

// withStarts copies each workflow entry with "starts": whether a command of
// that job starts the stack, so the rules can tell a deploy from a job that
// only renders or validates it.
func withStarts(wfs map[string]any, starts map[string]bool) map[string]any {
	out := make(map[string]any, len(wfs))
	for id, w := range wfs {
		m := map[string]any{}
		for k, v := range w.(map[string]any) {
			m[k] = v
		}
		m["starts"] = starts[id]
		out[id] = m
	}
	return out
}

func addAssumption(m map[string]map[string]any, h hop) {
	m[fmt.Sprintf("%s|%09d|%s", h.path, h.line, h.note)] = map[string]any{"path": h.path, "line": h.line, "text": h.note}
}

// wfKey tells workflow refs apart: one job reached through steps with
// different if: is recorded once per set of conditions.
func wfKey(m map[string]any) string {
	k := m["path"].(string) + "#" + m["job"].(string)
	if c, ok := m["conditions"]; ok {
		k += "#" + fmt.Sprint(c)
	}
	return k
}
