package command

import (
	"bytes"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/yamlnode"
	"github.com/cernly/cernly/repo"
)

// Source kinds. Each adapter turns one kind of file into shell snippets; the
// rest of the package only sees snippets.
const (
	srcWorkflow = "workflow"
	srcScript   = "script"
	srcTask     = "taskfile"
	srcMake     = "makefile"
	srcNpm      = "npm"
)

// workflowInfo says how a CI job is triggered. Branches is ["*"] for a push
// trigger without a branch or tag filter. Filters keep their written order,
// since a later pattern overrides an earlier one. A nil filter was absent.
type workflowInfo struct {
	Path        string
	Name        string // the workflow's name:, which workflow_run refers to
	Job         string
	Environment string
	Events      []string
	Branches    []string // push branch filters
	BranchesIgn []string // push branches-ignore
	Tags        []string // push tags
	TagsIgn     []string // push tags-ignore
	After       []string // workflow_run: names of the workflows this one follows
	AfterBranch []string // workflow_run branch filters
	AfterIgn    []string // workflow_run branches-ignore
	Manual      bool
	Conditions  []jobCond // if: of this job and of every job it needs, raw
	JobName     string    // the job's name:, as written
	JobLine     int       // line of the job's key
	Steps       []stepInfo
	Start       int    // index in Steps of the step that reaches the command; -1 when unknown
	StepIf      string // if: of the step, also in Conditions under the job
	Step        string // the step with StepIf, see stepName
}

// jobCond is one if:; Step is set when it is a step's if:, not the job's.
type jobCond struct{ Job, Step, If string }

// stepInfo is one step of a job, as written: name:, uses: and run:.
type stepInfo struct {
	Name, Uses, Run string
	Line            int
}

func (st stepInfo) ref() map[string]any {
	m := map[string]any{"line": st.Line}
	if st.Name != "" {
		m["name"] = st.Name
	}
	if st.Uses != "" {
		m["uses"] = st.Uses
	}
	if st.Run != "" {
		m["run"] = st.Run
	}
	return m
}

func (w workflowInfo) id() string {
	if w.StepIf != "" {
		return w.Path + "#" + w.Job + "#" + w.Step + "#" + w.StepIf
	}
	return w.Path + "#" + w.Job
}

// forStep is w for step i of the job: the step's if: joins the conditions,
// since the step runs only when the job and the step both do.
func (w *workflowInfo) forStep(step *yaml.Node, i int) *workflowInfo {
	c := yamlnode.Get(step, "if")
	if c == nil || c.Kind != yaml.ScalarNode || c.Value == "" {
		return w
	}
	sw := *w
	sw.StepIf, sw.Step = c.Value, stepName(step, i)
	sw.Conditions = append(append([]jobCond(nil), w.Conditions...), jobCond{Job: w.Job, Step: sw.Step, If: c.Value})
	sort.SliceStable(sw.Conditions, func(a, b int) bool { return sw.Conditions[a].Job < sw.Conditions[b].Job })
	return &sw
}

// stepName names step i of a job as GitHub shows it: its name:, else its
// id:, else #n counting from 1. A name survives steps added above it; the
// evidence line says where the step is.
func stepName(step *yaml.Node, i int) string {
	for _, k := range []string{"name", "id"} {
		if v := yamlnode.Get(step, k); v != nil && v.Kind == yaml.ScalarNode && v.Value != "" {
			return v.Value
		}
	}
	return "#" + strconv.Itoa(i+1)
}

// ref is the workflow as recorded in facts. Conditions are kept as written;
// deciding what they mean (which branch deploys) is for the rules layer.
// withSteps adds the step that reaches the command and the steps after it, as
// written (stacks: the rules decide whether the job only runs tests).
func (w workflowInfo) ref(withSteps bool) map[string]any {
	m := map[string]any{"path": w.Path, "job": w.Job, "manual": w.Manual,
		"branches": fact.Strings(w.Branches), "events": fact.Strings(w.Events)}
	if w.JobName != "" {
		m["job_name"] = w.JobName
	}
	if w.JobLine > 0 {
		m["job_line"] = w.JobLine
	}
	if withSteps && w.Start >= 0 && w.Start < len(w.Steps) {
		m["step"] = w.Steps[w.Start].ref()
		after := []any{}
		for _, st := range w.Steps[w.Start+1:] {
			after = append(after, st.ref())
		}
		m["steps_after"] = after
	}
	if w.Name != "" {
		m["workflow"] = w.Name
	}
	if w.Environment != "" {
		m["environment"] = w.Environment
	}
	if len(w.After) > 0 {
		m["after"] = fact.Strings(w.After)
	}
	if len(w.AfterBranch) > 0 {
		m["after_branches"] = fact.Strings(w.AfterBranch)
	}
	for k, v := range map[string][]string{"branches_ignore": w.BranchesIgn, "tags": w.Tags,
		"tags_ignore": w.TagsIgn, "after_branches_ignore": w.AfterIgn} {
		if v != nil {
			m[k] = fact.Strings(v)
		}
	}
	if len(w.Conditions) > 0 {
		var cs []any
		for _, c := range w.Conditions {
			cm := map[string]any{"job": c.Job, "if": c.If}
			if c.Step != "" {
				cm["step"] = c.Step
			}
			cs = append(cs, cm)
		}
		m["conditions"] = cs
	}
	return m
}

// snippet is a piece of shell with its place in the repo.
type snippet struct {
	kind string
	path string
	unit string // job, task, make target or npm script; "" for a whole script file
	line int    // file line of the snippet's first line
	text []byte // shell source as written; template syntax is masked when parsed
	wf   *workflowInfo
	step int   // workflow snippets: index of the step in wf.Steps
	segs []seg // value lines to file lines for a folded YAML scalar, else nil
	flat bool  // a YAML scalar not mapped: every finding is on line
}

// buildRec is an image build declared as data rather than shell
// (docker/build-push-action, docker-bake.json).
type buildRec struct {
	path, tool                  string
	line, endLine               int
	dockerfile, context, target string
	tags                        []string
	wf                          *workflowInfo
	step                        int    // index of the build step in wf.Steps
	push                        string // "true" or "false" when the record says; "" unknown
}

// taskRef is a Taskfile entry that calls another task by name.
type taskRef struct {
	path, task string
	callee     string
	line       int
}

type sources struct {
	snippets []*snippet
	builds   []buildRec
	taskRefs []taskRef
	errs     []fact.LocationError
}

func (s *sources) fail(p string, line int, msg string) {
	s.errs = append(s.errs, fact.LocationError{Path: p, Line: line, Message: msg})
}

func isWorkflow(p string) bool {
	return strings.HasPrefix(p, ".github/workflows/") && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml"))
}

func isTaskfile(base string) bool {
	switch base {
	case "Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml":
		return true
	}
	return false
}

func isMakefile(base string) bool {
	return base == "Makefile" || base == "makefile" || base == "GNUmakefile" || strings.HasSuffix(base, ".mk")
}

func isScriptName(base string) bool {
	return strings.HasSuffix(base, ".sh") || strings.HasSuffix(base, ".bash")
}

func isBake(base string) bool {
	return base == "docker-bake.json" || base == "docker-bake.override.json"
}

const maxScript = 1 << 20

var shells = []string{"sh", "bash", "dash", "zsh", "ksh"}

// hasShellShebang reports whether an extension-less file starts with a shell
// shebang (#!/bin/sh, #!/usr/bin/env bash, …).
func hasShellShebang(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("#!")) {
		return false
	}
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	for _, f := range fields {
		b := path.Base(f)
		for _, s := range shells {
			if b == s {
				return true
			}
		}
	}
	return false
}

func collect(r *repo.Repo) *sources {
	s := &sources{}
	for _, f := range r.Files {
		base := path.Base(f)
		switch {
		case isWorkflow(f):
			s.workflow(r, f)
		case isTaskfile(base):
			s.taskfile(r, f)
		case isMakefile(base):
			s.makefile(r, f)
		case base == "package.json":
			s.npm(r, f)
		case isBake(base):
			s.bake(r, f)
		case isScriptName(base):
			s.script(r, f, false)
		case !strings.Contains(base, "."):
			s.script(r, f, true)
		}
	}
	sort.SliceStable(s.snippets, func(i, j int) bool {
		a, b := s.snippets[i], s.snippets[j]
		if a.path != b.path {
			return a.path < b.path
		}
		return a.line < b.line
	})
	return s
}

func (s *sources) read(r *repo.Repo, p string) ([]byte, bool) {
	data, err := r.Read(p)
	if err != nil {
		s.fail(p, 0, err.Error())
		return nil, false
	}
	return data, true
}

// yamlDoc returns a YAML file's top mapping and its raw lines.
func (s *sources) yamlDoc(r *repo.Repo, p string) (*yaml.Node, []string) {
	data, ok := s.read(r, p)
	if !ok {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		s.fail(p, 0, "invalid YAML: "+err.Error())
		return nil, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	return doc.Content[0], strings.Split(string(data), "\n")
}

func (s *sources) script(r *repo.Repo, p string, needShebang bool) {
	data, ok := s.read(r, p)
	if !ok || len(data) > maxScript {
		return
	}
	if needShebang && !hasShellShebang(data) {
		return
	}
	s.snippets = append(s.snippets, &snippet{kind: srcScript, path: p, line: 1, text: data})
}

// scalarStart is the file line where a scalar's text begins: block scalars
// (| and >) start on the line after their indicator.
func scalarStart(n *yaml.Node) int {
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return n.Line + 1
	}
	return n.Line
}

func scalars(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode {
				out = append(out, c.Value)
			}
		}
		return out
	}
	return nil
}

type trigger struct {
	events, branches, after, afterBranches []string
	branchesIgn, tags, tagsIgn, afterIgn   []string // nil when absent
	manual                                 bool
}

// triggers reads a workflow's on: block.
func triggers(on *yaml.Node) (t trigger) {
	if on == nil {
		return t
	}
	events := map[string]*yaml.Node{}
	switch on.Kind {
	case yaml.ScalarNode, yaml.SequenceNode:
		for _, e := range scalars(on) {
			events[e] = nil
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			events[on.Content[i].Value] = on.Content[i+1]
		}
	}
	for e := range events {
		t.events = append(t.events, e)
	}
	sort.Strings(t.events)
	if _, ok := events["workflow_dispatch"]; ok {
		t.manual = true
	}
	if push, ok := events["push"]; ok {
		t.branchesIgn = filter(push, "branches-ignore")
		t.tags = filter(push, "tags")
		t.tagsIgn = filter(push, "tags-ignore")
		if b := scalars(yamlnode.Get(push, "branches")); len(b) > 0 {
			t.branches = b
		} else if t.tags == nil && t.tagsIgn == nil && t.branchesIgn == nil {
			t.branches = []string{"*"}
		}
	}
	if wr, ok := events["workflow_run"]; ok {
		t.after = scalars(yamlnode.Get(wr, "workflows"))
		t.afterBranches = scalars(yamlnode.Get(wr, "branches"))
		t.afterIgn = filter(wr, "branches-ignore")
	}
	sort.Strings(t.after)
	return t
}

// filter returns the patterns under key, in written order; nil when the key
// is absent, empty when it is present without patterns.
func filter(n *yaml.Node, key string) []string {
	v := yamlnode.Get(n, key)
	if v == nil {
		return nil
	}
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return []string{}
	}
	return append([]string{}, scalars(v)...)
}

// jobConditions returns the if: of a job and of every job it needs,
// transitively, sorted by job name.
func jobConditions(jobs *yaml.Node, start string) []jobCond {
	ifs, needs := map[string]string{}, map[string][]string{}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		name, job := jobs.Content[i].Value, jobs.Content[i+1]
		if c := yamlnode.Get(job, "if"); c != nil && c.Kind == yaml.ScalarNode {
			ifs[name] = c.Value
		}
		needs[name] = scalars(yamlnode.Get(job, "needs"))
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(j string) {
		if seen[j] {
			return
		}
		seen[j] = true
		for _, n := range needs[j] {
			visit(n)
		}
	}
	visit(start)
	var out []jobCond
	for j := range seen {
		if ifs[j] != "" {
			out = append(out, jobCond{Job: j, If: ifs[j]})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Job < out[b].Job })
	return out
}

func (s *sources) workflow(r *repo.Repo, p string) {
	root, raw := s.yamlDoc(r, p)
	if root == nil {
		return
	}
	tr := triggers(yamlnode.Get(root, "on"))
	wfName := ""
	if n := yamlnode.Get(root, "name"); n != nil {
		wfName = n.Value
	}
	jobs := yamlnode.Get(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		name, job := jobs.Content[i].Value, jobs.Content[i+1]
		wf := &workflowInfo{Path: p, Name: wfName, Job: name, Events: tr.events, Branches: tr.branches,
			BranchesIgn: tr.branchesIgn, Tags: tr.tags, TagsIgn: tr.tagsIgn, After: tr.after,
			AfterBranch: tr.afterBranches, AfterIgn: tr.afterIgn, Manual: tr.manual, Conditions: jobConditions(jobs, name),
			JobLine: jobs.Content[i].Line, Start: -1}
		if n := yamlnode.Get(job, "name"); n != nil && n.Kind == yaml.ScalarNode {
			wf.JobName = n.Value
		}
		if env := yamlnode.Get(job, "environment"); env != nil {
			if env.Kind == yaml.ScalarNode {
				wf.Environment = env.Value
			} else if n := yamlnode.Get(env, "name"); n != nil {
				wf.Environment = n.Value
			}
		}
		steps := yamlnode.Get(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			si := stepInfo{Line: step.Line}
			for k, dst := range map[string]*string{"name": &si.Name, "uses": &si.Uses, "run": &si.Run} {
				if v := yamlnode.Get(step, k); v != nil && v.Kind == yaml.ScalarNode {
					*dst = v.Value
				}
			}
			wf.Steps = append(wf.Steps, si)
		}
		// Every step is read first: a step with its own if: gets a copy of
		// the job's info, which must hold all the steps.
		for idx, step := range steps.Content {
			swf := wf.forStep(step, idx)
			if run := yamlnode.Get(step, "run"); run != nil && run.Kind == yaml.ScalarNode {
				sn := yamlSnippet(srcWorkflow, p, name, raw, run, swf)
				sn.step = idx
				s.snippets = append(s.snippets, sn)
			}
			if uses := yamlnode.Get(step, "uses"); uses != nil && strings.HasPrefix(uses.Value, "docker/build-push-action@") {
				with := yamlnode.Get(step, "with")
				// push: defaults to false in the action; anything but a literal is unknown.
				b := buildRec{path: p, tool: "build-push-action", line: uses.Line, wf: swf, step: idx, context: ".", push: "false"}
				if v := yamlnode.Get(with, "push"); v != nil {
					switch strings.ToLower(v.Value) {
					case "true", "false":
						b.push = strings.ToLower(v.Value)
					default:
						b.push = ""
					}
				}
				if v := yamlnode.Get(with, "context"); v != nil {
					b.context = v.Value
				}
				if v := yamlnode.Get(with, "file"); v != nil {
					b.dockerfile = v.Value
				}
				if v := yamlnode.Get(with, "target"); v != nil {
					b.target = v.Value
				}
				if v := yamlnode.Get(with, "tags"); v != nil {
					for _, t := range strings.FieldsFunc(v.Value, func(r rune) bool { return r == ',' || r == '\n' }) {
						if t = strings.TrimSpace(t); t != "" {
							b.tags = append(b.tags, t)
						}
					}
				}
				b.endLine = yamlnode.LastLine(step)
				s.builds = append(s.builds, b)
			}
		}
	}
}

func (s *sources) taskfile(r *repo.Repo, p string) {
	root, raw := s.yamlDoc(r, p)
	if root == nil {
		return
	}
	tasks := yamlnode.Get(root, "tasks")
	if tasks == nil || tasks.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(tasks.Content); i += 2 {
		name, t := tasks.Content[i].Value, tasks.Content[i+1]
		var cmds, deps *yaml.Node
		switch t.Kind {
		case yaml.ScalarNode, yaml.SequenceNode:
			cmds = t
		case yaml.MappingNode:
			cmds, deps = yamlnode.Get(t, "cmds"), yamlnode.Get(t, "deps")
			if c := yamlnode.Get(t, "cmd"); c != nil && cmds == nil {
				cmds = c
			}
		}
		items := []*yaml.Node{}
		if cmds != nil {
			if cmds.Kind == yaml.SequenceNode {
				items = cmds.Content
			} else {
				items = []*yaml.Node{cmds}
			}
		}
		for _, it := range items {
			switch it.Kind {
			case yaml.ScalarNode:
				s.snippets = append(s.snippets, yamlSnippet(srcTask, p, name, raw, it, nil))
			case yaml.MappingNode:
				if c := yamlnode.Get(it, "cmd"); c != nil && c.Kind == yaml.ScalarNode {
					s.snippets = append(s.snippets, yamlSnippet(srcTask, p, name, raw, c, nil))
				}
				if c := yamlnode.Get(it, "task"); c != nil {
					s.taskRefs = append(s.taskRefs, taskRef{path: p, task: name, callee: c.Value, line: c.Line})
				}
			}
		}
		if deps != nil && deps.Kind == yaml.SequenceNode {
			for _, d := range deps.Content {
				callee := d
				if d.Kind == yaml.MappingNode {
					callee = yamlnode.Get(d, "task")
				}
				if callee != nil && callee.Kind == yaml.ScalarNode {
					s.taskRefs = append(s.taskRefs, taskRef{path: p, task: name, callee: callee.Value, line: callee.Line})
				}
			}
		}
	}
}

// makefile reads rules and their recipe lines. Each recipe line is its own
// shell, as make runs it; backslash continuations are joined.
func (s *sources) makefile(r *repo.Repo, p string) {
	data, ok := s.read(r, p)
	if !ok {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r") // CRLF, or a continuation is missed
	}
	var targets []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "\t") && len(targets) > 0 {
			start := i + 1
			text := strings.TrimPrefix(l, "\t")
			for strings.HasSuffix(text, `\`) && i+1 < len(lines) {
				i++
				text += "\n" + strings.TrimPrefix(lines[i], "\t")
			}
			text = strings.TrimLeft(text, "@-+ ")
			text = strings.ReplaceAll(text, "$(MAKE)", "make   ")
			text = strings.ReplaceAll(text, "${MAKE}", "make   ")
			for _, t := range targets {
				s.snippets = append(s.snippets, &snippet{kind: srcMake, path: p, unit: t, line: start, text: []byte(text)})
			}
			continue
		}
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		targets = ruleTargets(l)
	}
}

// ruleTargets returns the targets of a rule line ("build test: deps"), or nil
// for variable assignments, directives and special targets.
func ruleTargets(l string) []string {
	colon := strings.Index(l, ":")
	if colon <= 0 || strings.HasPrefix(l, "\t") {
		return nil
	}
	if strings.ContainsAny(l[:colon], "=") || strings.HasPrefix(l[colon:], ":=") || strings.HasPrefix(l[colon:], "::=") {
		return nil
	}
	var out []string
	for _, t := range strings.Fields(l[:colon]) {
		if !strings.HasPrefix(t, ".") && !strings.ContainsAny(t, "$%") {
			out = append(out, t)
		}
	}
	return out
}

func (s *sources) npm(r *repo.Repo, p string) {
	root, _ := s.yamlDoc(r, p) // JSON is valid YAML; yaml.v3 keeps line numbers
	scripts := yamlnode.Get(root, "scripts")
	if scripts == nil || scripts.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(scripts.Content); i += 2 {
		k, v := scripts.Content[i], scripts.Content[i+1]
		if v.Kind == yaml.ScalarNode {
			s.snippets = append(s.snippets, &snippet{kind: srcNpm, path: p, unit: k.Value, line: v.Line, text: []byte(v.Value)})
		}
	}
}

func (s *sources) bake(r *repo.Repo, p string) {
	root, _ := s.yamlDoc(r, p)
	targets := yamlnode.Get(root, "target")
	if targets == nil || targets.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(targets.Content); i += 2 {
		k, t := targets.Content[i], targets.Content[i+1]
		b := buildRec{path: p, tool: "bake", line: k.Line, endLine: yamlnode.LastLine(t), context: "."}
		if v := yamlnode.Get(t, "context"); v != nil {
			b.context = v.Value
		}
		if v := yamlnode.Get(t, "dockerfile"); v != nil {
			b.dockerfile = v.Value
		}
		if v := yamlnode.Get(t, "target"); v != nil {
			b.target = v.Value
		}
		b.tags = scalars(yamlnode.Get(t, "tags"))
		s.builds = append(s.builds, b)
	}
}
