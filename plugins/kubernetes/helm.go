package kubernetes

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/internal/redact"
	"github.com/cernly/cernly/internal/yamlnode"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/internal/version"
)

// chart is a Helm chart folder (Chart.yaml).
type chart struct {
	dir, chartFile, name string
	nameLine             int
	files                []string // every tracked file under the chart
	depsNode             *yaml.Node
}

func (s *state) charts() map[string]*chart {
	out := map[string]*chart{}
	for _, f := range s.r.Files {
		if path.Base(f) != "Chart.yaml" {
			continue
		}
		docs := s.load(f)
		if len(docs) == 0 {
			continue
		}
		d := docs[0]
		c := &chart{dir: path.Dir(f), chartFile: f, name: str(yamlnode.Get(d, "name")), depsNode: yamlnode.Get(d, "dependencies"), nameLine: 1}
		if n := yamlnode.Get(d, "name"); n != nil {
			c.nameLine = n.Line
		}
		if c.name == "" {
			c.name = base(c.dir)
		}
		out[c.dir] = c
	}
	for _, f := range s.r.Files {
		for d, c := range out {
			if d == "." || strings.HasPrefix(f, d+"/") {
				c.files = append(c.files, f)
			}
		}
	}
	return out
}

// plain reads the chart's templates that hold no template action: plain
// manifests Helm installs as written.
func (c *chart) plain(s *state) []object {
	var out []object
	for _, f := range c.files {
		if strings.HasPrefix(f, path.Join(c.dir, "templates")+"/") && (strings.HasSuffix(f, ".yaml") || strings.HasSuffix(f, ".yml")) {
			out = append(out, s.objects(f)...) // objects skips files with {{
		}
	}
	return out
}

// deps are Chart.yaml's dependencies, as written.
func (c *chart) deps() []fact.Fact {
	if c.depsNode == nil {
		return nil
	}
	var out []fact.Fact
	for _, d := range c.depsNode.Content {
		name, ver := str(yamlnode.Get(d, "name")), str(yamlnode.Get(d, "version"))
		if name == "" {
			continue
		}
		attrs := map[string]any{"ecosystem": "helm", "name": name, "version": ver, "direct": true, "scope": "prod",
			"version_info": version.Parse(ver).Attrs()}
		ext := map[string]any{}
		// A repository URL loses its credentials (user:token@).
		if repoURL := redact.Userinfo(str(yamlnode.Get(d, "repository"))); repoURL != "" {
			ext["repository"] = repoURL
			if strings.HasPrefix(repoURL, "file://") {
				if p, ok := joinRel(c.dir, strings.TrimPrefix(repoURL, "file://")); ok {
					attrs["internal"] = map[string]any{"name": name, "path": p, "spec": repoURL}
				}
			}
		}
		if cond := str(yamlnode.Get(d, "condition")); cond != "" {
			ext["condition"] = cond
		}
		if len(ext) > 0 {
			attrs["ext"] = ext
		}
		out = append(out, fact.Fact{Kind: fact.KindDependency, Key: "dependency:helm:" + c.dir + ":" + name, Plugin: "kubernetes",
			Attrs: attrs, Evidence: []fact.Evidence{{Path: c.chartFile, Line: d.Line, Unit: c.dir}}})
	}
	return out
}

func joinRel(dir, rel string) (string, bool) {
	p := path.Clean(path.Join(dir, rel))
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// valueImage is an image reference in the chart's values.
type valueImage struct {
	component string // the values path above image:, "" at the top
	ref       string
	ev        []fact.Evidence
	complete  bool // a tag or digest is given
}

// images reads image references from values.yaml and the invocation's -f
// files, later files winning per key as Helm merges them: image: "ref", or
// image: {registry, repository, tag, digest}.
func (c *chart) images(s *state, valueFiles []string) []valueImage {
	files := []string{}
	if v := path.Join(c.dir, "values.yaml"); s.r.Has(v) {
		files = append(files, v)
	}
	files = append(files, valueFiles...)
	type part struct {
		val  string
		line int
		file string
	}
	found := map[string]map[string]part{} // component -> field -> value
	for _, f := range files {
		docs := s.load(f)
		if len(docs) == 0 {
			continue
		}
		var walk func(n *yaml.Node, at []string)
		walk = func(n *yaml.Node, at []string) {
			if n.Kind != yaml.MappingNode {
				return
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if k.Value == "image" {
					comp := strings.Join(at, ".")
					if found[comp] == nil {
						found[comp] = map[string]part{}
					}
					switch v.Kind {
					case yaml.ScalarNode:
						found[comp]["ref"] = part{v.Value, v.Line, f}
					case yaml.MappingNode:
						for _, fld := range []string{"registry", "repository", "name", "tag", "digest"} {
							if x := yamlnode.Get(v, fld); x != nil && x.Kind == yaml.ScalarNode {
								found[comp][fld] = part{x.Value, x.Line, f}
							}
						}
					}
					continue
				}
				walk(v, append(append([]string(nil), at...), k.Value))
			}
		}
		walk(docs[0], nil)
	}
	comps := make([]string, 0, len(found))
	for k := range found {
		comps = append(comps, k)
	}
	sort.Strings(comps)
	var out []valueImage
	for _, comp := range comps {
		parts := found[comp]
		vi := valueImage{component: comp}
		var fields []string
		for f := range parts {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			vi.ev = append(vi.ev, fact.Evidence{Path: parts[f].file, Line: parts[f].line})
		}
		if r, ok := parts["ref"]; ok {
			vi.ref = r.val
			ir := imageref.Parse(r.val)
			vi.complete = ir.Tag != "" || ir.Digest != ""
		} else {
			repoName := parts["repository"].val
			if repoName == "" {
				repoName = parts["name"].val
			}
			if repoName == "" {
				continue
			}
			if reg := parts["registry"].val; reg != "" {
				repoName = reg + "/" + repoName
			}
			vi.ref = repoName
			if t := parts["tag"].val; t != "" {
				vi.ref += ":" + t
				vi.complete = true
			}
			if d := parts["digest"].val; d != "" {
				vi.ref += "@" + d
				vi.complete = true
			}
		}
		out = append(out, vi)
	}
	return out
}

// valueServices are the components a chart's values give an image. The
// templates decide how they are used; they are not rendered, so the fact
// says where the reference is written and nothing more.
func valueServices(def stackDef, chartName string, vals []valueImage) []fact.Fact {
	var out []fact.Fact
	for _, v := range vals {
		if strings.Contains(v.ref, "{{") {
			continue
		}
		name := v.component
		if name == "" {
			name = chartName
		}
		ext := map[string]any{"values_path": strings.TrimPrefix(v.component+".image", "."), "rendered": false}
		attrs := map[string]any{"name": name, "stack": def.name, "image": v.ref, "ext": ext}
		if v.complete {
			attrs["image_ref"] = imageref.Parse(v.ref).Attrs()
		} else {
			ext["tag"] = "not in the values; the chart's templates decide (often appVersion)"
		}
		ev := append(append([]fact.Evidence(nil), v.ev...), def.evidence...)
		out = append(out, fact.Fact{Kind: fact.KindService, Key: "service:helm:" + def.name + ":" + name, Plugin: "kubernetes", Attrs: attrs, Evidence: ev})
	}
	return out
}
