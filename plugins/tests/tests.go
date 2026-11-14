// Package tests reports test suites: the test files of each Go module, npm
// package and Python project, found by the language's own naming
// convention, with the framework the module declares. Nothing is run and
// nothing is measured. Java and Kotlin test suites come from the java
// WebAssembly plugin. See https://cernly.dev/how/plugins/.
package tests

import (
	"encoding/json"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/testfile"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "tests" }
func (Plugin) Version() string { return "0.1.0" }

func (Plugin) Watches(p string) bool {
	return language(p) != "" || isManifest(path.Base(p))
}

// language is the shared test-file convention (internal/testfile) for
// the languages this plugin reports; Java and Kotlin are the java plugin's.
func language(p string) string {
	switch l := testfile.Language(p); l {
	case "go", "javascript", "python":
		return l
	}
	return ""
}

func isManifest(b string) bool {
	switch b {
	case "go.mod", "package.json", "pyproject.toml", "setup.py", "setup.cfg", "conftest.py", "pytest.ini", "tox.ini":
		return true
	}
	return strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")
}

// roots are the module folders of a language, deepest first wins.
func roots(r *repo.Repo, lang string) map[string]bool {
	out := map[string]bool{}
	for _, f := range r.Files {
		b := path.Base(f)
		switch lang {
		case "go":
			if b == "go.mod" {
				out[path.Dir(f)] = true
			}
		case "javascript":
			if b == "package.json" {
				out[path.Dir(f)] = true
			}
		case "python":
			if b == "pyproject.toml" || b == "setup.py" || b == "setup.cfg" || (strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")) {
				out[path.Dir(f)] = true
			}
		}
	}
	return out
}

func owner(roots map[string]bool, f string) string {
	for d := path.Dir(f); ; d = path.Dir(d) {
		if roots[d] {
			return d
		}
		if d == "." || d == "/" {
			return "."
		}
	}
}

const maxEvidence = 50

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	type suite struct {
		files []string
		dirs  map[string]bool
	}
	suites := map[string]map[string]*suite{} // language -> module -> suite
	rootsOf := map[string]map[string]bool{}
	for _, f := range r.Files {
		lang := language(f)
		if lang == "" {
			continue
		}
		if rootsOf[lang] == nil {
			rootsOf[lang] = roots(r, lang)
			suites[lang] = map[string]*suite{}
		}
		mod := owner(rootsOf[lang], f)
		s := suites[lang][mod]
		if s == nil {
			s = &suite{dirs: map[string]bool{}}
			suites[lang][mod] = s
		}
		s.files = append(s.files, f)
		s.dirs[path.Dir(f)] = true
	}
	langs := make([]string, 0, len(suites))
	for l := range suites {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		mods := make([]string, 0, len(suites[lang]))
		for m := range suites[lang] {
			mods = append(mods, m)
		}
		sort.Strings(mods)
		for _, mod := range mods {
			s := suites[lang][mod]
			var ev []fact.Evidence
			for i, f := range s.files {
				if i == maxEvidence {
					break
				}
				ev = append(ev, fact.Evidence{Path: f, Line: 1, Unit: mod})
			}
			dirs := make([]any, 0, len(s.dirs))
			keys := make([]string, 0, len(s.dirs))
			for d := range s.dirs {
				keys = append(keys, d)
			}
			sort.Strings(keys)
			for _, d := range keys {
				dirs = append(dirs, d)
			}
			fw, fwEv := framework(r, lang, mod)
			ev = append(ev, fwEv...)
			res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindTestSuite, Key: "test_suite:" + lang + ":" + mod, Plugin: "tests",
				Attrs: map[string]any{"path": mod, "language": lang, "framework": fw, "files": len(s.files), "dirs": dirs}, Evidence: ev})
		}
	}
	return res
}

// jsFrameworks in order of preference when a package declares several (the
// end-to-end runner last).
var jsFrameworks = []struct{ pkg, name string }{
	{"vitest", "vitest"}, {"jest", "jest"}, {"mocha", "mocha"}, {"ava", "ava"},
	{"@playwright/test", "playwright"}, {"cypress", "cypress"},
}

// framework reads the module's declared test framework; the evidence is the
// manifest line that declares it.
func framework(r *repo.Repo, lang, mod string) (string, []fact.Evidence) {
	switch lang {
	case "go":
		return "go-test", nil
	case "javascript":
		pj := path.Join(mod, "package.json")
		data, err := r.Read(pj)
		if err != nil {
			return "unknown", nil
		}
		var m struct {
			Deps    map[string]string `json:"dependencies"`
			DevDeps map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &m) != nil {
			return "unknown", nil
		}
		for _, fw := range jsFrameworks {
			if _, ok := m.DevDeps[fw.pkg]; ok {
				return fw.name, []fact.Evidence{{Path: pj, Line: lineOf(data, `"`+fw.pkg+`"`), Unit: mod}}
			}
			if _, ok := m.Deps[fw.pkg]; ok {
				return fw.name, []fact.Evidence{{Path: pj, Line: lineOf(data, `"`+fw.pkg+`"`), Unit: mod}}
			}
		}
		return "unknown", nil
	case "python":
		for _, f := range []string{"conftest.py", "pytest.ini"} {
			p := path.Join(mod, f)
			if r.Has(p) {
				return "pytest", []fact.Evidence{{Path: p, Line: 1, Unit: mod}}
			}
		}
		for _, f := range r.Files {
			b := path.Base(f)
			if path.Dir(f) != mod || !(b == "pyproject.toml" || b == "setup.cfg" || b == "tox.ini" || strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")) {
				continue
			}
			data, err := r.Read(f)
			if err != nil {
				continue
			}
			if l := lineOfWord(data, "pytest"); l > 0 {
				return "pytest", []fact.Evidence{{Path: f, Line: l, Unit: mod}}
			}
		}
		return "unittest", nil
	}
	return "unknown", nil
}

func lineOf(data []byte, needle string) int {
	i := strings.Index(string(data), needle)
	if i < 0 {
		return 1
	}
	return strings.Count(string(data[:i]), "\n") + 1
}

// lineOfWord finds pytest as a whole word outside comments.
func lineOfWord(data []byte, word string) int {
	for n, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) {
			if tok == word {
				return n + 1
			}
		}
	}
	return 0
}
