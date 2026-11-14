// Package kubernetes is the Kubernetes tool adapter: plain manifests,
// Kustomize overlays and, partly, Helm charts become stacks; workloads
// (Deployment, StatefulSet, DaemonSet, Job, CronJob, Pod) become services
// or data stores, with the Services that select them as evidence; image
// references go through the normalisation.
//
// Nothing is rendered or run. Kustomize is followed as far as its files
// say: resources, bases and components in order, the images transformer,
// namePrefix, nameSuffix and namespace; patches and generators are not
// applied. Helm templates are not rendered (their values are unknown): a
// chart gives its Chart.yaml dependencies, the image references in
// values.yaml and the -f values files of its invocation, and the manifests
// in templates/ that hold no template action. See
// https://cernly.dev/how/plugins/.
package kubernetes

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/internal/yamlnode"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "kubernetes" }
func (Plugin) Version() string { return "0.1.2" }

func (Plugin) Watches(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
}

// Consumes makes it a phase-2 tool adapter: it gets the kubernetes and helm
// stacks the command reader found (kubectl, kustomize, helm calls).
func (Plugin) Consumes() []string { return []string{fact.KindStack} }

func (p Plugin) Run(r *repo.Repo) plugin.Result { return p.RunWith(r, nil) }

var workloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true,
	"Job": true, "CronJob": true, "Pod": true}

// object is one Kubernetes manifest document.
type object struct {
	kind, name, namespace string
	file                  string
	line                  int
	containers            []container
	labels                map[string]string // pod template labels
	selector              map[string]string // a Service's selector
	ports                 []any
	via                   []fact.Evidence // kustomization lines that brought it in
}

type container struct {
	name, image string
	line        int
	file        string
	via         []fact.Evidence // images transformer lines that changed it
}

type state struct {
	r         *repo.Repo
	res       *plugin.Result
	docs      map[string][]*yaml.Node // file -> documents
	read      map[string]bool
	templated map[string]bool
	left      int // objects one top-level kustomization may still add
}

// maxKustomizeObjects bounds the objects one top-level kustomization
// expands to, so a resource listed many times cannot multiply the output.
var maxKustomizeObjects = 100000 // a variable so a test can lower it

func (s *state) load(file string) []*yaml.Node {
	if s.read[file] {
		return s.docs[file]
	}
	s.read[file] = true
	data, err := s.r.Read(file)
	if err != nil {
		s.res.Errors = append(s.res.Errors, fact.LocationError{Path: file, Message: err.Error()})
		return nil
	}
	if strings.Contains(string(data), "{{") {
		s.templated[file] = true // a template (Helm, or another tool's); never rendered
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var out []*yaml.Node
	for {
		var d yaml.Node
		if err := dec.Decode(&d); err != nil {
			if err.Error() != "EOF" {
				out = nil // not YAML a tool would load; another plugin's concern
			}
			break
		}
		if len(d.Content) > 0 {
			out = append(out, d.Content[0])
		}
	}
	s.docs[file] = out
	return out
}

func str(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func strMap(n *yaml.Node) map[string]string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	m := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		m[n.Content[i].Value] = n.Content[i+1].Value
	}
	return m
}

// objects returns the Kubernetes objects of a file: documents with
// apiVersion and kind; a List's items are read too.
func (s *state) objects(file string) []object {
	var out []object
	var add func(d *yaml.Node)
	add = func(d *yaml.Node) {
		if str(yamlnode.Get(d, "apiVersion")) == "" || str(yamlnode.Get(d, "kind")) == "" {
			return
		}
		kind := str(yamlnode.Get(d, "kind"))
		if kind == "List" || strings.HasSuffix(kind, "List") {
			if items := yamlnode.Get(d, "items"); items != nil {
				for _, it := range items.Content {
					add(it)
				}
			}
			return
		}
		o := object{kind: kind, name: str(yamlnode.Get(d, "metadata", "name")), namespace: str(yamlnode.Get(d, "metadata", "namespace")),
			file: file, line: yamlnode.Get(d, "kind").Line}
		var pod *yaml.Node
		switch kind {
		case "Pod":
			pod = yamlnode.Get(d, "spec")
			o.labels = strMap(yamlnode.Get(d, "metadata", "labels"))
		case "CronJob":
			pod = yamlnode.Get(d, "spec", "jobTemplate", "spec", "template", "spec")
			o.labels = strMap(yamlnode.Get(d, "spec", "jobTemplate", "spec", "template", "metadata", "labels"))
		case "Service":
			o.selector = strMap(yamlnode.Get(d, "spec", "selector"))
			if ports := yamlnode.Get(d, "spec", "ports"); ports != nil {
				for _, pt := range ports.Content {
					if v := str(yamlnode.Get(pt, "port")); v != "" {
						o.ports = append(o.ports, v)
					}
				}
			}
		default:
			if workloadKinds[kind] {
				pod = yamlnode.Get(d, "spec", "template", "spec")
				o.labels = strMap(yamlnode.Get(d, "spec", "template", "metadata", "labels"))
			}
		}
		if cs := yamlnode.Get(pod, "containers"); cs != nil {
			for _, c := range cs.Content {
				img := yamlnode.Get(c, "image")
				line := c.Line
				if img != nil {
					line = img.Line
				}
				o.containers = append(o.containers, container{name: str(yamlnode.Get(c, "name")), image: str(img), line: line, file: file})
			}
		}
		if o.name != "" {
			out = append(out, o)
		}
	}
	docs := s.load(file)
	if s.templated[file] {
		return nil
	}
	for _, d := range docs {
		add(d)
	}
	return out
}

func isKustomization(f string) bool {
	switch path.Base(f) {
	case "kustomization.yaml", "kustomization.yml", "Kustomization":
		return true
	}
	return false
}

func (s *state) kustomizationIn(dir string) string {
	for _, n := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		p := path.Join(dir, n)
		if s.r.Has(p) {
			return p
		}
	}
	return ""
}

// filesIn lists the YAML files directly in dir, as kubectl -f dir reads.
func (s *state) filesIn(dir string) []string {
	var out []string
	for _, f := range s.r.Files {
		if path.Dir(f) == dir && (strings.HasSuffix(f, ".yaml") || strings.HasSuffix(f, ".yml") || strings.HasSuffix(f, ".json")) && !isKustomization(f) {
			out = append(out, f)
		}
	}
	return out
}

// built is the output of one kustomization, its own transformers applied.
type built struct {
	objects []object
	files   []string // every file read, for coverage
	remote  []string // resources outside the repo
}

// kustomize follows one kustomization folder.
func (s *state) kustomize(dir string, depth int, seen map[string]bool) built {
	var b built
	kf := s.kustomizationIn(dir)
	if kf == "" || depth > 20 || seen[dir] {
		return b
	}
	if depth == 0 {
		s.left = maxKustomizeObjects
	}
	seen[dir] = true
	listed := map[string]bool{} // a resource listed twice is expanded once
	b.files = append(b.files, kf)
	docs := s.load(kf)
	if len(docs) == 0 {
		return b
	}
	k := docs[0]
	for _, field := range []string{"resources", "bases", "components"} {
		list := yamlnode.Get(k, field)
		if list == nil {
			continue
		}
		for _, item := range list.Content {
			ref := str(item)
			via := fact.Evidence{Path: kf, Line: item.Line}
			if ref == "" {
				continue
			}
			if strings.Contains(ref, "://") || strings.HasPrefix(ref, "github.com/") || strings.Contains(ref, "?ref=") {
				b.remote = append(b.remote, redact.Userinfo(ref))
				continue
			}
			p, ok := repo.Join(dir, ref)
			if !ok {
				b.remote = append(b.remote, ref)
				continue
			}
			if listed[p] {
				continue
			}
			listed[p] = true
			if s.left < 0 {
				continue
			}
			var sub built
			if s.r.Has(p) {
				// Only files spend the budget: a nested kustomization spent
				// its own objects already.
				sub = built{objects: s.objects(p), files: []string{p}}
				if len(sub.objects) > s.left {
					sub.objects = sub.objects[:s.left]
					s.left = -1 // recorded once, rest skipped
					s.res.Errors = append(s.res.Errors, fact.LocationError{Path: kf, Line: item.Line, Message: "kustomize: more than " + strconv.Itoa(maxKustomizeObjects) + " objects from " + dir + "; rest skipped"})
				} else {
					s.left -= len(sub.objects)
				}
			} else if s.kustomizationIn(p) != "" {
				sub = s.kustomize(p, depth+1, seen)
			} else {
				s.res.Errors = append(s.res.Errors, fact.LocationError{Path: kf, Line: item.Line, Message: "resource " + ref + ": not a tracked file or kustomization"})
				continue
			}
			for i := range sub.objects {
				sub.objects[i].via = append(sub.objects[i].via, via)
			}
			b.objects = append(b.objects, sub.objects...)
			b.files = append(b.files, sub.files...)
			b.remote = append(b.remote, sub.remote...)
		}
	}
	// Transformers of this kustomization, after its resources' own.
	prefix, suffix, ns := str(yamlnode.Get(k, "namePrefix")), str(yamlnode.Get(k, "nameSuffix")), str(yamlnode.Get(k, "namespace"))
	for i := range b.objects {
		b.objects[i].name = prefix + b.objects[i].name + suffix
		if ns != "" {
			b.objects[i].namespace = ns
		}
	}
	if imgs := yamlnode.Get(k, "images"); imgs != nil {
		for _, rule := range imgs.Content {
			name := str(yamlnode.Get(rule, "name"))
			if name == "" {
				continue
			}
			for i := range b.objects {
				for j := range b.objects[i].containers {
					c := &b.objects[i].containers[j]
					if imageName(c.image) != name {
						continue
					}
					c.image = rewrite(c.image, str(yamlnode.Get(rule, "newName")), str(yamlnode.Get(rule, "newTag")), str(yamlnode.Get(rule, "digest")))
					c.via = append(c.via, fact.Evidence{Path: kf, Line: rule.Line})
				}
			}
		}
	}
	return b
}

// imageName is an image reference without tag or digest, as Kustomize's
// images transformer matches it.
func imageName(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

func rewrite(ref, newName, newTag, digest string) string {
	name := imageName(ref)
	tag := strings.TrimPrefix(strings.TrimPrefix(ref, name), ":")
	if i := strings.Index(tag, "@"); i >= 0 {
		tag = tag[:i]
	}
	if newName != "" {
		name = newName
	}
	if newTag != "" {
		tag = newTag
	}
	out := name
	if tag != "" {
		out += ":" + tag
	}
	if digest != "" {
		out += "@" + digest
	}
	return out
}

// stackDef is one stack this plugin reports on.
type stackDef struct {
	tool, name string
	paths      []string
	evidence   []fact.Evidence
	unresolved []string // resources outside the repo (convention stacks)
}

func (p Plugin) RunWith(r *repo.Repo, in []fact.Fact) plugin.Result {
	var res plugin.Result
	s := &state{r: r, res: &res, docs: map[string][]*yaml.Node{}, read: map[string]bool{}, templated: map[string]bool{}}
	var stacks []stackDef
	names := map[string]bool{}
	for _, f := range in {
		tool, _ := f.Attrs["tool"].(string)
		if f.Kind != fact.KindStack || (tool != "kubernetes" && tool != "helm") {
			continue
		}
		var paths []string
		switch fs := f.Attrs["files"].(type) {
		case []any:
			for _, x := range fs {
				paths = append(paths, x.(string))
			}
		case []string:
			paths = fs
		}
		name, _ := f.Attrs["name"].(string)
		stacks = append(stacks, stackDef{tool: tool, name: name, paths: paths, evidence: f.Evidence})
		names[tool+"\x00"+name] = true
	}
	covered := map[string]bool{}
	charts := s.charts()
	type out struct {
		def   stackDef
		objs  []object
		vals  []valueImage
		chart string
	}
	var outs []out
	run := func(def stackDef) {
		o := out{def: def}
		switch def.tool {
		case "kubernetes":
			for _, pth := range def.paths {
				switch {
				case r.Has(pth):
					o.objs = append(o.objs, s.objects(pth)...)
					covered[pth] = true
				case s.kustomizationIn(pth) != "":
					b := s.kustomize(pth, 0, map[string]bool{})
					o.objs = append(o.objs, b.objects...)
					for _, f := range b.files {
						covered[f] = true
					}
				default:
					for _, f := range s.filesIn(pth) {
						o.objs = append(o.objs, s.objects(f)...)
						covered[f] = true
					}
				}
			}
		case "helm":
			if len(def.paths) == 0 {
				break
			}
			ch, ok := charts[def.paths[0]]
			if !ok {
				break // a chart from a repository: nothing in the repo to read
			}
			for _, f := range ch.files {
				covered[f] = true
			}
			o.objs = append(o.objs, ch.plain(s)...)
			o.vals = ch.images(s, def.paths[1:])
			o.chart = ch.name
			for _, v := range def.paths[1:] {
				covered[v] = true
			}
		}
		outs = append(outs, o)
	}
	for _, def := range stacks {
		run(def)
	}
	// Convention stacks: what no invocation covers, named by the tool's own
	// layout.
	unique := func(tool, name string) string {
		n := name
		for i := 2; names[tool+"\x00"+n]; i++ {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		names[tool+"\x00"+n] = true
		return n
	}
	var conv []stackDef
	// Charts not invoked.
	chartDirs := make([]string, 0, len(charts))
	for d := range charts {
		chartDirs = append(chartDirs, d)
	}
	sort.Strings(chartDirs)
	for _, d := range chartDirs {
		if !covered[charts[d].chartFile] {
			conv = append(conv, stackDef{tool: "helm", name: unique("helm", charts[d].name), paths: []string{d},
				evidence: []fact.Evidence{{Path: charts[d].chartFile, Line: charts[d].nameLine}}})
			for _, f := range charts[d].files {
				covered[f] = true
			}
		}
	}
	// Top-level kustomizations: not a resource of another one.
	referenced := map[string]bool{}
	var kdirs []string
	for _, f := range r.Files {
		if isKustomization(f) {
			kdirs = append(kdirs, path.Dir(f))
			b := s.kustomize(path.Dir(f), 0, map[string]bool{})
			for _, x := range b.files {
				if isKustomization(x) && path.Dir(x) != path.Dir(f) {
					referenced[path.Dir(x)] = true
				}
			}
		}
	}
	for _, d := range kdirs {
		kf := s.kustomizationIn(d)
		if referenced[d] || covered[kf] {
			continue
		}
		b := s.kustomize(d, 0, map[string]bool{})
		conv = append(conv, stackDef{tool: "kubernetes", name: unique("kubernetes", base(d)), paths: []string{d},
			evidence: []fact.Evidence{{Path: kf, Line: 1}}, unresolved: b.remote})
		for _, x := range b.files {
			covered[x] = true
		}
	}
	// Plain manifests left: one stack per folder.
	byDir := map[string][]string{}
	for _, f := range r.Files {
		if !p.Watches(f) || covered[f] || isKustomization(f) || inChart(f, charts) {
			continue
		}
		// Only files with a workload or a Service: other documents with
		// apiVersion and kind (catalog-info.yaml, CI tools) are not a stack.
		for _, o := range s.objects(f) {
			if workloadKinds[o.kind] || o.kind == "Service" {
				byDir[path.Dir(f)] = append(byDir[path.Dir(f)], f)
				break
			}
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		var ev []fact.Evidence
		for _, f := range byDir[d] {
			ev = append(ev, fact.Evidence{Path: f, Line: s.objects(f)[0].line})
		}
		conv = append(conv, stackDef{tool: "kubernetes", name: unique("kubernetes", base(d)), paths: byDir[d], evidence: ev})
	}
	for _, def := range conv {
		attrs := map[string]any{"name": def.name, "tool": def.tool, "source": "convention",
			"files": fact.Strings(def.paths), "default_files": false, "starts": false, "ci_invoked": false}
		if len(def.unresolved) > 0 {
			attrs["unresolved"] = fact.Strings(def.unresolved)
		}
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindStack, Key: "stack:" + def.tool + ":" + def.name, Plugin: "kubernetes",
			Evidence: def.evidence, Attrs: attrs})
		run(def)
	}
	for _, o := range outs {
		res.Facts = append(res.Facts, workloads(o.def, o.objs)...)
		res.Facts = append(res.Facts, valueServices(o.def, o.chart, o.vals)...)
	}
	for _, d := range chartDirs {
		res.Facts = append(res.Facts, charts[d].deps()...)
	}
	return res
}

func base(dir string) string {
	if dir == "." {
		return "default"
	}
	return path.Base(dir)
}

func inChart(f string, charts map[string]*chart) bool {
	for d := range charts {
		if d == "." || strings.HasPrefix(f, d+"/") {
			return true
		}
	}
	return false
}

// workloads turns a stack's objects into service and data store facts.
func workloads(def stackDef, objs []object) []fact.Fact {
	var out []fact.Fact
	for _, o := range objs {
		if !workloadKinds[o.kind] || len(o.containers) == 0 {
			continue
		}
		ev := []fact.Evidence{{Path: o.file, Line: o.line}}
		ev = append(ev, o.via...)
		main := o.containers[0]
		ev = append(ev, fact.Evidence{Path: main.file, Line: main.line})
		ev = append(ev, main.via...)
		ext := map[string]any{"kind": o.kind}
		if o.namespace != "" {
			ext["namespace"] = o.namespace
		}
		if len(o.containers) > 1 {
			var cs []any
			for _, c := range o.containers {
				cs = append(cs, map[string]any{"name": c.name, "image": c.image})
				if c.line != main.line || c.file != main.file {
					ev = append(ev, fact.Evidence{Path: c.file, Line: c.line})
					ev = append(ev, c.via...)
				}
			}
			ext["containers"] = cs
		}
		// Services that select this workload's pods.
		var ports []any
		for _, svc := range objs {
			if svc.kind != "Service" || len(svc.selector) == 0 || svc.namespace != o.namespace || !subset(svc.selector, o.labels) {
				continue
			}
			ev = append(ev, fact.Evidence{Path: svc.file, Line: svc.line})
			ports = append(ports, svc.ports...)
		}
		if len(ports) > 0 {
			ext["service_ports"] = ports
		}
		ev = append(ev, def.evidence...)
		key := def.tool + ":" + def.name + ":" + o.name
		if engine, v, ok := imageref.Datastore(main.image); ok {
			attrs := map[string]any{"name": o.name, "stack": def.name, "engine": engine, "image_ref": imageref.Parse(main.image).Attrs(), "ext": ext}
			if v.Raw != "" {
				attrs["version_info"] = v.Attrs()
			}
			if v.Normalised != "" {
				attrs["version"] = v.Normalised
			}
			out = append(out, fact.Fact{Kind: fact.KindDatastore, Key: "datastore:" + key, Plugin: "kubernetes", Attrs: attrs, Evidence: ev})
			continue
		}
		attrs := map[string]any{"name": o.name, "stack": def.name, "ext": ext}
		if main.image != "" {
			attrs["image"] = main.image
			attrs["image_ref"] = imageref.Parse(main.image).Attrs()
		}
		out = append(out, fact.Fact{Kind: fact.KindService, Key: "service:" + key, Plugin: "kubernetes", Attrs: attrs, Evidence: ev})
	}
	return out
}

func subset(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}
