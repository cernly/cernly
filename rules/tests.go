package rules

import (
	"sort"

	"github.com/cernly/cernly/fact"
)

// TestPresence says whether a service's code has tests and whether a
// command runs them: test_suite facts inside the service's link sources,
// and the test_run facts that cover each suite. No coverage: nothing is
// run. Whether the job ran on a change is the checks reader's.
type TestPresence struct {
	Service   string   `json:"service,omitempty"` // service key; absent for a suite on no service
	Suite     string   `json:"suite"`
	Language  string   `json:"language"`
	Framework string   `json:"framework"`
	Files     int      `json:"files"`
	Runs      []string `json:"runs"`           // test_run keys that cover the suite
	Jobs      []JobRef `json:"jobs,omitempty"` // CI jobs that reach one of those runs
	From      []string `json:"from"`
}

// JobRef is one CI job.
type JobRef struct {
	Workflow string `json:"workflow"`
	Job      string `json:"job"`
}

// runFamily maps a test_run tool to the suite language it runs.
var runFamily = map[string]string{
	"go": "go", "npm": "javascript", "pnpm": "javascript", "yarn": "javascript", "bun": "javascript",
	"jest": "javascript", "vitest": "javascript", "mocha": "javascript", "ava": "javascript",
	"playwright": "javascript", "cypress": "javascript",
	"pytest": "python", "unittest": "python", "maven": "java", "gradle": "java",
	"dotnet": "dotnet",
}

// dotnetLanguages are the suite languages a dotnet run covers.
var dotnetLanguages = map[string]bool{"csharp": true, "fsharp": true, "vb": true}

// recursive tools run the suites of every module under their folder (a
// multi-module build, pytest's discovery); the others run their own folder.
var recursive = map[string]bool{"maven": true, "gradle": true, "dotnet": true, "pytest": true, "unittest": true}

func covers(run, suite fact.Fact) bool {
	tool := run.Str("tool")
	lang := suite.Str("language")
	if runFamily[tool] == "dotnet" {
		if !dotnetLanguages[lang] {
			return false
		}
	} else if runFamily[tool] != lang {
		return false
	}
	dir, path := run.Str("dir"), suite.Str("path")
	if recursive[tool] {
		return inside(path, dir)
	}
	return dir == path
}

func tests(m *Map, facts []fact.Fact) []TestPresence {
	var suites, runs []fact.Fact
	for _, f := range facts {
		switch f.Kind {
		case fact.KindTestSuite:
			suites = append(suites, f)
		case fact.KindTestRun:
			runs = append(runs, f)
		}
	}
	if len(suites) == 0 {
		return nil
	}
	var out []TestPresence
	for _, s := range suites {
		base := TestPresence{Suite: s.Key, Language: s.Str("language"), Framework: s.Str("framework"), Runs: []string{}}
		switch n := s.Attrs["files"].(type) {
		case int:
			base.Files = n
		case float64:
			base.Files = int(n)
		}
		jobs := map[JobRef]bool{}
		for _, r := range runs {
			if !covers(r, s) {
				continue
			}
			base.Runs = append(base.Runs, r.Key)
			if wfs, ok := r.Attrs["workflows"].([]any); ok {
				for _, w := range wfs {
					wm, _ := w.(map[string]any)
					jobs[JobRef{Workflow: anyStr(wm["path"]), Job: anyStr(wm["job"])}] = true
				}
			}
		}
		for j := range jobs {
			base.Jobs = append(base.Jobs, j)
		}
		sort.Slice(base.Jobs, func(i, j int) bool {
			if base.Jobs[i].Workflow != base.Jobs[j].Workflow {
				return base.Jobs[i].Workflow < base.Jobs[j].Workflow
			}
			return base.Jobs[i].Job < base.Jobs[j].Job
		})
		dirs := s.Strs("dirs")
		onService := false
		seen := map[string]bool{}
		for _, l := range m.Links {
			if seen[l.Service] || !anyInside(dirs, l.Sources) {
				continue
			}
			seen[l.Service] = true
			onService = true
			tp := base
			tp.Service = l.Service
			tp.From = uniq(append(append([]string{s.Key, l.Service}, l.From...), base.Runs...))
			out = append(out, tp)
		}
		if !onService {
			base.From = uniq(append([]string{s.Key}, base.Runs...))
			out = append(out, base)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Suite < out[j].Suite
	})
	return out
}

func anyInside(dirs, sources []string) bool {
	for _, d := range dirs {
		for _, s := range sources {
			if inside(d, s) {
				return true
			}
		}
	}
	return false
}
