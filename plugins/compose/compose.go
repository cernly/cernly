// Package compose extracts services and data stores from Docker Compose files.
package compose

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/composefile"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/internal/yamlnode"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func New() plugin.Plugin { return Plugin{} }

func (Plugin) Name() string    { return "compose" }
func (Plugin) Version() string { return "0.1.2" }

func (Plugin) Watches(p string) bool { return defaults.IsCompose(p) }

type serviceSpec struct {
	Image     string    `yaml:"image"`
	Build     yaml.Node `yaml:"build"`
	DependsOn yaml.Node `yaml:"depends_on"`
	Volumes   yaml.Node `yaml:"volumes"`
}

// Consumes makes compose a phase-2 tool adapter: it gets the stacks the
// invocation readers found and merges each one.
func (Plugin) Consumes() []string { return []string{fact.KindStack} }

// Run without input: every stack is a convention stack.
func (p Plugin) Run(r *repo.Repo) plugin.Result { return p.RunWith(r, nil) }

// stackDef is an ordered set of compose files started together.
type stackDef struct {
	name     string
	files    []string
	evidence []fact.Evidence // of the stack fact: invocation lines, or nothing for a convention stack
}

// RunWith reports services and data stores once per stack, merged as
// Compose merges -f files: a later file's image or build replaces an
// earlier one, depends_on is the union, and relative paths resolve against
// the first file's folder. Compose files no invocation covers form
// convention stacks, which this plugin reports as stack facts itself.
func (p Plugin) RunWith(r *repo.Repo, in []fact.Fact) plugin.Result {
	var res plugin.Result
	docs := map[string]*yaml.Node{}
	keyLine := map[string]int{} // line of the services: key, for convention stack evidence
	services := func(file string) *yaml.Node {
		if n, ok := docs[file]; ok {
			return n
		}
		docs[file] = nil
		data, err := r.Read(file)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			return nil
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: "invalid YAML: " + err.Error()})
			return nil
		}
		if len(doc.Content) == 0 {
			return nil
		}
		root := doc.Content[0]
		if root.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i].Value == "services" {
					keyLine[file] = root.Content[i].Line
					docs[file] = root.Content[i+1]
				}
			}
		}
		return docs[file]
	}

	stacks, used, covered := invoked(r, in)
	for _, st := range stacks {
		for _, f := range st.files {
			covered[f] = true
		}
	}
	for _, st := range convention(r, covered, used) {
		var ev []fact.Evidence
		for _, f := range st.files {
			services(f)
			line := keyLine[f]
			if line == 0 {
				line = 1 // no services: key; the file itself is the evidence
			}
			ev = append(ev, fact.Evidence{Path: f, Line: line})
		}
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + st.name, Plugin: "compose", Evidence: ev,
			Attrs: map[string]any{"name": st.name, "tool": "compose", "source": "convention", "files": fact.Strings(st.files),
				"default_files": st.name == "default", "starts": false, "ci_invoked": false}})
		stacks = append(stacks, st)
	}
	for _, st := range stacks {
		res.Facts = append(res.Facts, mergeStack(st, services, r.Has, &res)...)
	}
	return res
}

// invoked returns the compose stacks of the phase-1 facts, keeping only the
// files that are tracked (the rest are listed as unresolved on the stack).
// Conditional overlays (conditional_files) are not merged, but an
// invocation names them, so they form no convention stack either.
func invoked(r *repo.Repo, in []fact.Fact) ([]stackDef, map[string]bool, map[string]bool) {
	used, covered := map[string]bool{}, map[string]bool{}
	var out []stackDef
	for _, f := range in {
		if f.Kind != fact.KindStack || f.Attrs["tool"] != "compose" {
			continue
		}
		name, _ := f.Attrs["name"].(string)
		used[name] = true
		cond, _ := f.Attrs["conditional_files"].([]any)
		for _, c := range cond {
			if m, ok := c.(map[string]any); ok {
				if p, ok := m["file"].(string); ok {
					covered[p] = true
				}
			}
		}
		st := stackDef{name: name, evidence: f.Evidence}
		files, _ := f.Attrs["files"].([]any)
		for _, x := range files {
			if p, ok := x.(string); ok && r.Has(p) {
				st.files = append(st.files, p)
			}
		}
		if len(st.files) > 0 {
			out = append(out, st)
		}
	}
	return out, used, covered
}

// convention groups compose files no invocation covers, per folder: the
// default stack (what a bare `docker compose up` starts: base file plus the
// default override) when the base file is uncovered, and the base file plus
// each other uncovered file. A folder without a base file gives one stack
// per file.
func convention(r *repo.Repo, covered, used map[string]bool) []stackDef {
	byDir := map[string][]string{}
	var dirs []string
	for _, f := range r.Match(defaults.IsCompose) {
		d := path.Dir(f)
		if byDir[d] == nil {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	sort.Strings(dirs)
	var out []stackDef
	add := func(files ...string) {
		name := composefile.StackName(files)
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", composefile.StackName(files), i)
		}
		used[name] = true
		out = append(out, stackDef{name: name, files: files})
	}
	for _, d := range dirs {
		files := byDir[d]
		base := first(r, d, composefile.Default)
		done := map[string]bool{}
		if base != "" {
			done[base] = true
			if !covered[base] {
				def := []string{base}
				if o := first(r, d, composefile.Override); o != "" && !covered[o] {
					def = append(def, o)
					done[o] = true
				}
				add(def...)
			}
		}
		for _, f := range files {
			if done[f] || covered[f] {
				continue
			}
			if base != "" {
				add(base, f)
			} else {
				add(f)
			}
		}
	}
	return out
}

func first(r *repo.Repo, dir string, names []string) string {
	for _, n := range names {
		if p := path.Join(dir, n); r.Has(p) {
			return p
		}
	}
	return ""
}

type merged struct {
	image                    string
	hasBuild                 bool
	context, dockerfile, tgt string // build keys, merged one by one as Compose does
	deps                     map[string]bool
	mounts                   map[string]string // container target -> tracked repo file, "" when not one
	ev                       []fact.Evidence
}

func mergeStack(st stackDef, services func(string) *yaml.Node, has func(string) bool, res *plugin.Result) []fact.Fact {
	project := repo.Dir(st.files[0])
	byName := map[string]*merged{}
	var names []string
	for _, file := range st.files {
		svcs := services(file)
		if svcs == nil || svcs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			keyNode, valNode := svcs.Content[i], svcs.Content[i+1]
			name := keyNode.Value
			var spec serviceSpec
			if err := valNode.Decode(&spec); err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: file, Line: keyNode.Line, Message: "service " + name + ": " + err.Error()})
				continue
			}
			m := byName[name]
			if m == nil {
				m = &merged{deps: map[string]bool{}, mounts: map[string]string{}}
				byName[name] = m
				names = append(names, name)
			}
			ev := fact.Evidence{Path: file, Line: keyNode.Line, EndLine: yamlnode.LastLine(valNode), Unit: name}
			if ev.EndLine <= ev.Line {
				ev.EndLine = 0
			}
			m.ev = append(m.ev, ev)
			if img := strings.TrimSpace(spec.Image); img != "" {
				m.image = img
			}
			if spec.Build.Kind != 0 {
				m.hasBuild = true
				b := buildKeys(spec.Build)
				if v, ok := b["context"]; ok {
					m.context = v
				}
				if v, ok := b["dockerfile"]; ok {
					m.dockerfile = v
				}
				if v, ok := b["target"]; ok {
					m.tgt = v
				}
			}
			for _, d := range dependsOn(spec.DependsOn) {
				m.deps[d.(string)] = true
			}
			// Bind mounts merge by target, a later file replacing an earlier
			// one, as Compose does. Sources resolve against the project folder.
			for target, src := range bindMounts(spec.Volumes) {
				m.mounts[target] = ""
				if p, ok := repo.Join(project, src); ok && has(p) {
					m.mounts[target] = p
				}
			}
		}
	}
	var out []fact.Fact
	for _, name := range names {
		m := byName[name]
		f := build(st.name, name, project, m)
		f.Evidence = append(append([]fact.Evidence(nil), m.ev...), st.evidence...)
		out = append(out, f)
	}
	return out
}

func build(stack, name, project string, m *merged) fact.Fact {
	if engine, v, ok := imageref.Datastore(m.image); ok && !m.hasBuild {
		attrs := map[string]any{"name": name, "stack": stack, "engine": engine, "image_ref": imageref.Parse(m.image).Attrs()}
		if v.Raw != "" {
			attrs["version_info"] = v.Attrs()
		}
		if v.Normalised != "" {
			attrs["version"] = v.Normalised
		}
		return fact.Fact{Kind: fact.KindDatastore, Key: "datastore:compose:" + stack + ":" + name, Attrs: attrs, Plugin: "compose"}
	}
	attrs := map[string]any{"name": name, "stack": stack}
	if m.image != "" {
		attrs["image"] = m.image
		attrs["image_ref"] = imageref.Parse(m.image).Attrs()
	}
	if m.hasBuild {
		ctx := m.context
		if ctx == "" {
			ctx = "."
		}
		// Relative paths resolve against the project folder (the first
		// file's), the Dockerfile against the context. Remote contexts and
		// paths outside the repo are left out.
		if p, ok := repo.Join(project, ctx); ok && !strings.Contains(ctx, "://") {
			attrs["build_context"] = p
			df := m.dockerfile
			if df == "" {
				df = "Dockerfile"
			}
			if d, ok := repo.Join(p, df); ok && !path.IsAbs(df) {
				attrs["dockerfile"] = d
			}
		}
		if m.tgt != "" {
			attrs["target"] = m.tgt
		}
	}
	if mounts := mountAttrs(m.mounts); len(mounts) > 0 {
		attrs["mounts"] = mounts
	}
	if len(m.deps) > 0 {
		deps := make([]string, 0, len(m.deps))
		for d := range m.deps {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		attrs["depends_on"] = fact.Strings(deps)
	}
	return fact.Fact{Kind: fact.KindService, Key: "service:compose:" + stack + ":" + name, Attrs: attrs, Plugin: "compose"}
}

// buildKeys reads a build: value, short form (the context) or long form.
func buildKeys(n yaml.Node) map[string]string {
	out := map[string]string{}
	switch n.Kind {
	case yaml.ScalarNode:
		out["context"] = n.Value
	case yaml.MappingNode:
		for _, k := range []string{"context", "dockerfile", "target"} {
			if v := yamlnode.Get(&n, k); v != nil && v.Kind == yaml.ScalarNode {
				out[k] = v.Value
			}
		}
	}
	return out
}

func dependsOn(n yaml.Node) []any {
	var names []string
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode {
				names = append(names, c.Value)
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			names = append(names, n.Content[i].Value)
		}
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for i, s := range names {
		if i > 0 && s == names[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// bindMounts reads a service's volumes: as container target to host source,
// for bind mounts with a relative source (short form ./x:/y[:ro], long form
// type: bind). Named volumes, absolute and home paths and interpolated
// sources are left out: they are not files in the repository.
func bindMounts(n yaml.Node) map[string]string {
	out := map[string]string{}
	if n.Kind != yaml.SequenceNode {
		return out
	}
	for _, c := range n.Content {
		var src, target string
		switch c.Kind {
		case yaml.ScalarNode:
			parts := strings.Split(c.Value, ":")
			if len(parts) < 2 {
				continue
			}
			src, target = parts[0], parts[1]
		case yaml.MappingNode:
			if t := yamlnode.Get(c, "type"); t == nil || t.Value != "bind" {
				continue
			}
			if v := yamlnode.Get(c, "source"); v != nil {
				src = v.Value
			}
			if v := yamlnode.Get(c, "target"); v != nil {
				target = v.Value
			}
		}
		if target == "" || !strings.HasPrefix(src, ".") || strings.Contains(src, "$") {
			continue
		}
		out[target] = src
	}
	return out
}

func mountAttrs(mounts map[string]string) []any {
	targets := make([]string, 0, len(mounts))
	for t, src := range mounts {
		if src != "" {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)
	out := make([]any, 0, len(targets))
	for _, t := range targets {
		out = append(out, map[string]any{"source": mounts[t], "target": t})
	}
	return out
}
