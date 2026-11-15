package dockerfile

import (
	"path"
	"strconv"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/dotnetsln"
	"github.com/cernly/cernly/internal/imageref"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "dockerfile" }
func (Plugin) Version() string { return "0.2.0" }

func isDockerfile(base string) bool { return defaults.MatchBase(defaults.Get().Files.Dockerfile, base) }

// Watches Dockerfiles, and the solutions and project files a dotnet
// entrypoint is resolved against.
func (Plugin) Watches(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".sln", ".slnx", ".csproj", ".fsproj", ".vbproj":
		return true
	}
	return isDockerfile(path.Base(p))
}

// Run reports one image_recipe per named stage and per final stage: the
// external base images, the build-context paths that end up in it (directly or
// through COPY --from), remote files fetched at build time, and the go / npm
// builds whose output it contains.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	for _, file := range r.Match(isDockerfile) {
		data, err := r.Read(file)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		parsed, err := parse(data)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: file, Message: err.Error()})
			continue
		}
		stages := readStages(parsed)
		for _, st := range stages {
			if st.name == "" && st.index != len(stages)-1 {
				continue
			}
			res.Facts = append(res.Facts, recipe(r, file, stages, st))
		}
	}
	return res
}

// stageID is the stage's --target name. An unnamed final stage, built when
// no --target is given, is "_final"; other unnamed stages are "#<index>".
func stageID(stages []*stage, st *stage) string {
	switch {
	case st.name != "":
		return st.name
	case st.index == len(stages)-1:
		return "_final"
	}
	return "#" + strconv.Itoa(st.index)
}

// isScratch reports Docker's reserved empty base, which is not an image.
func isScratch(ref string) bool { return strings.EqualFold(ref, "scratch") }

// lookup resolves a stage reference (name or index) seen at stage `before`.
func lookup(stages []*stage, ref string, before int) *stage {
	if i, err := strconv.Atoi(ref); err == nil && i >= 0 && i < before {
		return stages[i]
	}
	for _, s := range stages[:before] {
		if s.name == ref {
			return s
		}
	}
	return nil
}

type collector struct {
	repo     *repo.Repo
	others   map[string]bool
	stages   []*stage
	bases    map[string]map[string]any
	sources  map[string]bool
	remote   map[string]bool
	entries  map[string]map[string]any
	evidence []fact.Evidence
	seen     map[string]bool
}

func (c *collector) ev(line, end int, file string) {
	if end <= line {
		end = 0
	}
	c.evidence = append(c.evidence, fact.Evidence{Path: file, Line: line, EndLine: end})
}

// visit adds a stage's content. outputs limits which builds count: nil means
// the whole stage is inherited (FROM), otherwise only builds whose output is
// one of the copied container paths.
func (c *collector) visit(file string, st *stage, outputs []string) {
	key := stageID(c.stages, st) + "|" + strings.Join(outputs, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.ev(st.line, 0, file)
	if parent := lookup(c.stages, st.base, st.index); parent != nil {
		c.visit(file, parent, nil)
	} else if !isScratch(st.base) {
		art := imageref.Parse(st.base)
		art.Raw, art.Templated = st.rawBase, imageref.Parse(st.rawBase).Templated
		ref := map[string]any{"ref": st.base, "role": "from", "stage": stageID(c.stages, st), "artifact": art.Attrs()}
		if st.rawBase != st.base {
			ref["raw"] = st.rawBase
		}
		c.bases[st.base+"|from|"+stageID(c.stages, st)] = ref
	}
	for _, cp := range st.copies {
		if cp.fromStage == "" {
			for _, s := range cp.srcs {
				c.sources[cleanSrc(s)] = true
			}
			c.ev(cp.line, cp.endLine, file)
			continue
		}
		if from := lookup(c.stages, cp.fromStage, st.index); from != nil {
			c.ev(cp.line, cp.endLine, file)
			var paths []string
			for _, s := range cp.srcs {
				paths = append(paths, absIn(from.workdir, s))
			}
			c.visit(file, from, paths)
			continue
		}
		if !isScratch(cp.fromStage) {
			c.bases[cp.fromStage+"|copy|"+stageID(c.stages, st)] = map[string]any{"ref": cp.fromStage, "role": "copy", "stage": stageID(c.stages, st),
				"artifact": imageref.Parse(cp.fromStage).Attrs()}
		}
		c.ev(cp.line, cp.endLine, file)
	}
	for _, rs := range st.remote {
		c.remote[rs.url] = true
		c.ev(rs.line, 0, file)
	}
	for _, o := range st.others {
		c.others[o.word] = true
		c.ev(o.line, o.endLine, file)
	}
	for _, b := range st.builds {
		if outputs != nil && !covered(b.output, outputs) {
			continue
		}
		e := map[string]any{"kind": b.kind, "raw": b.raw}
		switch b.kind {
		case "go":
			if p, ok := st.contextPath(b.pkg); ok && !b.unresolved {
				e["path"], e["resolved"] = p, true
			} else {
				e["path"], e["resolved"] = b.pkg, false
			}
		case "npm":
			e["workspace"] = b.pkg
		case "dotnet":
			c.dotnet(e, st, b.pkg, b.unresolved)
		}
		p, _ := e["path"].(string)
		c.entries[b.kind+"|"+p+"|"+b.raw] = e
		c.ev(b.line, b.endLine, file)
	}
}

// dotnet fills a dotnet entrypoint: the repo path of what is built and, when
// it can be read, the project folders. A path that is not tracked, a variable,
// a folder with several projects, a cd before the build or several named
// projects stays unresolved.
func (c *collector) dotnet(e map[string]any, st *stage, pkg string, unresolved bool) {
	e["path"], e["resolved"] = pkg, false
	rel, ok := st.contextPath(pkg)
	if unresolved || !ok || strings.ContainsAny(pkg, "$`") {
		return
	}
	var projects []string
	switch strings.ToLower(path.Ext(rel)) {
	case ".csproj", ".fsproj", ".vbproj":
		if c.repo.Has(rel) {
			projects = []string{path.Dir(rel)}
		}
	case ".sln", ".slnx":
		if c.repo.Has(rel) {
			if data, err := c.repo.Read(rel); err == nil {
				projects, _ = dotnetsln.Projects(data, rel)
			}
		}
	default:
		projects = c.folderProjects(rel)
	}
	if len(projects) == 0 {
		return
	}
	e["path"], e["resolved"] = rel, true
	list := make([]any, len(projects))
	for i, p := range projects {
		list[i] = p
	}
	e["projects"] = list
}

// folderProjects reads the project or solution files tracked directly in a
// folder: its one project, else its one solution.
func (c *collector) folderProjects(dir string) []string {
	in := func(exts ...string) []string {
		return c.repo.Match(func(base string) bool {
			e := strings.ToLower(path.Ext(base))
			for _, x := range exts {
				if e == x {
					return true
				}
			}
			return false
		})
	}
	direct := func(files []string) []string {
		var out []string
		for _, f := range files {
			if path.Dir(f) == dir {
				out = append(out, f)
			}
		}
		return out
	}
	if p := direct(in(".csproj", ".fsproj", ".vbproj")); len(p) == 1 {
		return []string{dir}
	} else if len(p) > 1 {
		return nil
	}
	if s := direct(in(".sln", ".slnx")); len(s) == 1 {
		if data, err := c.repo.Read(s[0]); err == nil {
			p, _ := dotnetsln.Projects(data, s[0])
			return p
		}
	}
	return nil
}

// covered reports whether a build output is one of the copied paths or lies
// inside one. An unknown output counts, so nothing is silently dropped.
func covered(output string, copied []string) bool {
	if output == "" {
		return true
	}
	for _, p := range copied {
		if output == p || strings.HasPrefix(output, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func cleanSrc(s string) string {
	c := path.Clean(s)
	if c == "" {
		return "."
	}
	return c
}

func recipe(r *repo.Repo, file string, stages []*stage, st *stage) fact.Fact {
	c := &collector{repo: r, others: map[string]bool{}, stages: stages, bases: map[string]map[string]any{}, sources: map[string]bool{},
		remote: map[string]bool{}, entries: map[string]map[string]any{}, seen: map[string]bool{}}
	c.visit(file, st, nil)
	attrs := map[string]any{
		"dockerfile":  file,
		"target":      stageID(stages, st),
		"final":       st.index == len(stages)-1,
		"base_images": fact.SortedValues(c.bases),
		"sources":     fact.SortedSet(c.sources),
		"entrypoints": fact.SortedValues(c.entries),
	}
	if len(c.remote) > 0 {
		attrs["remote_sources"] = fact.SortedSet(c.remote)
	}
	if len(c.others) > 0 {
		attrs["other_commands"] = fact.SortedSet(c.others)
	}
	return fact.Fact{
		Kind:     fact.KindImageRecipe,
		Key:      "image_recipe:" + file + ":" + stageID(stages, st),
		Attrs:    attrs,
		Plugin:   "dockerfile",
		Evidence: c.evidence,
	}
}
