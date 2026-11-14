// Package dotnet reads .NET source and project structure: test
// suites, the project graph and EF Core migrations, from the projects the
// msbuild reader finds.
// It emits the existing fact shapes itself; the tests and outbound plugins do
// not read C#.
package dotnet

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugins/dotnet/msbuild"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "dotnet" }
func (Plugin) Version() string { return "0.1.0" }

func (Plugin) Watches(p string) bool {
	b := path.Base(p)
	lb := strings.ToLower(b)
	switch path.Ext(lb) {
	case ".csproj", ".fsproj", ".vbproj", ".cs", ".fs", ".vb":
		return true
	}
	return (strings.HasPrefix(lb, "directory.") && strings.HasSuffix(lb, ".props")) ||
		(strings.HasPrefix(lb, "appsettings") && strings.HasSuffix(lb, ".json"))
}

// state is what the producers share: the projects, read once, and the source
// files each project owns.
type state struct {
	r        *repo.Repo
	projects []*msbuild.Project
	byPath   map[string]*msbuild.Project
	dirs     map[string]bool     // folders that hold a project
	sources  map[string][]string // project dir -> its source files, sorted
	lexed    map[string][]token  // tokens, by path
}

// tokens lexes a tracked file once; later calls get the same tokens.
func (s *state) tokens(p string) ([]token, error) {
	if t, ok := s.lexed[p]; ok {
		return t, nil
	}
	data, err := s.r.Read(p)
	if err != nil {
		return nil, err
	}
	t := lex(data)
	s.lexed[p] = t
	return t, nil
}

// producers each add one kind of fact from the shared state.
var producers = []func(*state, *plugin.Result){testSuites, imports, efTables, envOutbound}

// Run reads the projects once and runs every producer over them. Project files
// that do not parse are reported by deps-nuget, which reads the same projects,
// so they are not reported twice here.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	projects, _ := msbuild.Projects(r)
	s := &state{r: r, projects: projects, byPath: map[string]*msbuild.Project{}, dirs: map[string]bool{}, sources: sources(r, projects), lexed: map[string][]token{}}
	for _, p := range projects {
		s.byPath[p.Path] = p
		s.dirs[p.Dir] = true
	}
	for _, produce := range producers {
		produce(s, &res)
	}
	return res
}

var sourceExt = map[string]string{".cs": "csharp", ".fs": "fsharp", ".vb": "vb"}

// sources gives each project folder the source files below it, leaving out
// nested folders that hold another project: a file belongs to the nearest
// project folder above it. A folder whose projects are all non-SDK-style
// (legacy .NET Framework) gets no sources: such a project gets its dependency
// list and SBOM only, and its files stay its own.
func sources(r *repo.Repo, projects []*msbuild.Project) map[string][]string {
	dirs := map[string]bool{} // project folder -> it holds an SDK-style project
	for _, p := range projects {
		dirs[p.Dir] = dirs[p.Dir] || p.SDKStyle
	}
	out := map[string][]string{}
	if len(dirs) == 0 {
		return out
	}
	for _, f := range r.Files {
		if sourceExt[strings.ToLower(path.Ext(f))] == "" {
			continue
		}
		for d := path.Dir(f); ; d = path.Dir(d) {
			if sdk, ok := dirs[d]; ok {
				if sdk {
					out[d] = append(out[d], f)
				}
				break
			}
			if d == "." || d == "/" {
				break
			}
		}
	}
	for _, fs := range out {
		sort.Strings(fs)
	}
	return out
}

const maxEvidence = 50

// frameworks map a test project's package ids (lower case) to its framework,
// in order of preference when it references several.
var frameworks = []struct{ id, name string }{
	{"xunit", "xunit"}, {"xunit.v3", "xunit"}, {"nunit", "nunit"},
	{"mstest", "mstest"}, {"mstest.testframework", "mstest"}, {"tunit", "tunit"},
}

// testSuites reports one test_suite per test project (IsTestProject, or a
// Microsoft.NET.Test.Sdk reference): its source files in the project's
// language, the framework from its packages, and the declaring PackageReference
// line as evidence. A test project without source files gives none.
func testSuites(s *state, res *plugin.Result) {
	seen := map[string]bool{}
	for _, p := range s.projects {
		if !p.IsTest {
			continue
		}
		key := "test_suite:" + p.Lang + ":" + p.Dir
		var files []string
		for _, f := range s.sources[p.Dir] {
			if sourceExt[strings.ToLower(path.Ext(f))] == p.Lang {
				files = append(files, f)
			}
		}
		if len(files) == 0 || seen[key] {
			continue
		}
		seen[key] = true
		var ev []fact.Evidence
		dirSet := map[string]bool{}
		for i, f := range files {
			if i < maxEvidence {
				ev = append(ev, fact.Evidence{Path: f, Line: 1, Unit: p.Dir})
			}
			dirSet[path.Dir(f)] = true
		}
		fw := "unknown"
	match:
		for _, c := range frameworks {
			for _, pr := range p.Packages {
				if strings.ToLower(pr.ID) == c.id {
					fw = c.name
					for _, e := range pr.Ev {
						e.Unit = p.Dir
						ev = append(ev, e)
					}
					break match
				}
			}
		}
		dirs := make([]string, 0, len(dirSet))
		for d := range dirSet {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		dirsAny := make([]any, len(dirs))
		for i, d := range dirs {
			dirsAny[i] = d
		}
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindTestSuite, Key: key, Plugin: "dotnet",
			Attrs:    map[string]any{"path": p.Dir, "language": p.Lang, "framework": fw, "files": len(files), "dirs": dirsAny},
			Evidence: ev})
	}
}

// imports reports one import per ProjectReference between two projects of the
// repo, keyed by project file paths; a reference that leaves the repo or names
// a project it does not hold gives none. A project referenced twice (per
// target framework) is one fact with both lines. A reference under a condition
// that stays undecided keeps it in ext.condition: with several, the edge holds
// when any does (msbuild.AnyCondition).
func imports(s *state, res *plugin.Result) {
	for _, p := range s.projects {
		idx := map[string]int{}
		var order []string
		conds := map[string][]string{}
		for _, ref := range p.ProjectRefs {
			to := s.byPath[ref.Path]
			if ref.Path == "" || to == nil || to == p {
				continue
			}
			conds[to.Path] = append(conds[to.Path], ref.Condition)
			ev := fact.Evidence{Path: p.Path, Line: ref.Line, Unit: p.Dir}
			if i, ok := idx[to.Path]; ok {
				res.Facts[i].Evidence = append(res.Facts[i].Evidence, ev)
				continue
			}
			idx[to.Path] = len(res.Facts)
			order = append(order, to.Path)
			res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindImport, Key: "import:dotnet:" + p.Path + ":" + to.Path, Plugin: "dotnet",
				Attrs:    map[string]any{"language": "dotnet", "from": p.Path, "to": to.Path, "from_dir": p.Dir, "to_dir": to.Dir},
				Evidence: []fact.Evidence{ev}})
		}
		for _, to := range order {
			if c := msbuild.AnyCondition(conds[to]); c != "" {
				res.Facts[idx[to]].Attrs["ext"] = map[string]any{"condition": c}
			}
		}
	}
}
