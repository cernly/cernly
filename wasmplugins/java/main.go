// Command java is the Java/Spring stack adapter, the first WebAssembly
// plugin: built with GOOS=wasip1 GOARCH=wasm, signed, and loaded by the
// scanner like any outside plugin. It reads, never runs:
//
//   - Maven pom.xml and Gradle build scripts (Groovy and Kotlin DSL) with
//     version catalogs: declared dependencies. A version computed in a
//     script or managed outside the repo stays as written, scheme unknown.
//   - application.yml / application.properties (and profiles, bootstrap):
//     environment placeholders ${NAME} as env names, public URLs as hosts.
//   - Java and Kotlin sources: @Value and System.getenv as env names;
//     RestTemplate, WebClient and Feign base URLs as outbound hosts.
//   - JPA @Entity / @Table / @Column (Java) and Liquibase changelogs as
//     tables and columns. Flyway's SQL migrations are read by the built-in
//     migrations-sql plugin.
//
// Test sources (src/test, *Test.java) are not read for env or hosts. See
// https://cernly.dev/how/wasm-plugins/.
package main

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/testfile"
	"github.com/cernly/cernly/plugin/sdk"
)

const name = "java"

func main() { sdk.Main(run) }

// emitter collects facts and errors.
type emitter struct {
	facts  []fact.Fact
	errors []fact.LocationError
}

func (e *emitter) add(kind, key string, attrs map[string]any, ev ...fact.Evidence) {
	e.facts = append(e.facts, fact.Fact{Kind: kind, Key: key, Attrs: attrs, Plugin: name, Evidence: ev})
}

// merged folds facts with one key into one, as the scanner would for
// different plugins: evidence joined, env origins united, the strongest
// outbound usage kept. Output is sorted by kind and key.
func (e *emitter) merged() []fact.Fact {
	rank := map[string]int{"mention": 1, "default": 2, "request": 3}
	idx := map[string]int{}
	var out []fact.Fact
	for _, f := range e.facts {
		id := f.Kind + "\x00" + f.Key
		i, ok := idx[id]
		if !ok {
			idx[id] = len(out)
			out = append(out, f)
			continue
		}
		o := &out[i]
		o.Evidence = append(o.Evidence, f.Evidence...)
		switch f.Kind {
		case fact.KindEnvVar:
			seen := map[string]bool{}
			var origins []any
			for _, list := range [][]any{o.Attrs["origins"].([]any), f.Attrs["origins"].([]any)} {
				for _, x := range list {
					if s := x.(string); !seen[s] {
						seen[s] = true
						origins = append(origins, s)
					}
				}
			}
			sort.Slice(origins, func(a, b int) bool { return origins[a].(string) < origins[b].(string) })
			o.Attrs["origins"] = origins
		case fact.KindOutboundHost:
			if rank[f.Attrs["usage"].(string)] > rank[o.Attrs["usage"].(string)] {
				o.Attrs["usage"] = f.Attrs["usage"]
			}
		}
	}
	for i := range out {
		sort.Slice(out[i].Evidence, func(a, b int) bool {
			x, y := out[i].Evidence[a], out[i].Evidence[b]
			if x.Path != y.Path {
				return x.Path < y.Path
			}
			return x.Line < y.Line
		})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Kind != out[b].Kind {
			return out[a].Kind < out[b].Kind
		}
		return out[a].Key < out[b].Key
	})
	return out
}

func (e *emitter) fail(p string, line int, msg string) {
	e.errors = append(e.errors, fact.LocationError{Path: p, Line: line, Message: msg})
}

// project is what the build files say about the repo's modules.
type project struct {
	files   map[string][]byte
	modules []string          // folders with a pom.xml or build.gradle(.kts), sorted
	engine  map[string]string // module folder -> postgres, mysql (from JDBC drivers)
	testFw  map[string]fact.Evidence
	fwName  map[string]string // module folder -> test framework its dependencies declare
}

func run(in *sdk.Input) sdk.Output {
	p := &project{files: map[string][]byte{}, engine: map[string]string{}, testFw: map[string]fact.Evidence{}, fwName: map[string]string{}}
	var paths []string
	for _, f := range in.Repo.Files {
		p.files[f.Path] = f.Content
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	mods := map[string]bool{}
	for _, f := range paths {
		switch path.Base(f) {
		case "pom.xml", "build.gradle", "build.gradle.kts":
			mods[path.Dir(f)] = true
		}
	}
	for m := range mods {
		p.modules = append(p.modules, m)
	}
	sort.Strings(p.modules)

	var e emitter
	maven(p, paths, &e)
	gradle(p, paths, &e)
	// Config first: a JDBC URL there names the engine the tables use.
	for _, f := range paths {
		if isSpringConfig(f) {
			springConfig(p, f, &e)
		}
	}
	for _, f := range paths {
		if (strings.HasSuffix(f, ".java") || strings.HasSuffix(f, ".kt")) && !isTestSource(f) {
			source(p, f, &e)
		}
	}
	liquibase(p, paths, &e)
	testSuites(p, paths, &e)
	return sdk.Output{Facts: e.merged(), Errors: e.errors}
}

// module is the nearest folder at or above f holding a build file; "." when
// none.
func (p *project) module(f string) string {
	d := path.Dir(f)
	for {
		for _, m := range p.modules {
			if m == d {
				return d
			}
		}
		if d == "." || d == "/" {
			return "."
		}
		d = path.Dir(d)
	}
}

// qualify names a table by its database's convention: PostgreSQL puts an
// unqualified table in public; elsewhere it stays as written.
func (p *project) qualify(module, schema, table string) string {
	if schema != "" {
		return schema + "." + table
	}
	if p.engineOf(module) == "postgres" {
		return "public." + table
	}
	return table
}

// engineOf finds the JDBC driver of a module or of a module above it.
func (p *project) engineOf(module string) string {
	for d := module; ; d = path.Dir(d) {
		if e := p.engine[d]; e != "" {
			return e
		}
		if d == "." || d == "/" {
			return p.engine["."]
		}
	}
}

// frameworks maps a test dependency to its framework; the first a module
// declares in this order wins.
var frameworks = []struct{ group, artifact, name string }{
	{"org.junit.jupiter", "", "junit5"}, {"org.springframework.boot", "spring-boot-starter-test", "junit5"},
	{"io.kotest", "", "kotest"}, {"org.spockframework", "", "spock"}, {"org.testng", "", "testng"},
	{"junit", "junit", "junit4"},
}

func rankOf(name string) int {
	for i, f := range frameworks {
		if f.name == name {
			return i
		}
	}
	return len(frameworks)
}

// noteTest records the test framework a dependency declares.
func (p *project) noteTest(module, group, artifact string, at fact.Evidence) {
	for _, f := range frameworks {
		if (f.group == group || strings.HasPrefix(group, f.group+".")) && (f.artifact == "" || f.artifact == artifact) {
			if cur, ok := p.fwName[module]; !ok || rankOf(f.name) < rankOf(cur) {
				p.fwName[module] = f.name
				p.testFw[module] = at
			}
			return
		}
	}
}

// noteDriver records the engine a dependency's coordinates name.
func (p *project) noteDriver(module, group, artifact string) {
	switch {
	case group == "org.postgresql" && artifact == "postgresql":
		p.engine[module] = "postgres"
	case group == "com.mysql" || group == "mysql" || group == "org.mariadb.jdbc":
		if p.engine[module] == "" {
			p.engine[module] = "mysql"
		}
	}
}

// testSuites reports each module's test sources (src/test and the other
// Gradle test source sets) as one suite.
func testSuites(p *project, paths []string, e *emitter) {
	type suite struct {
		files []string
		dirs  map[string]bool
	}
	suites := map[string]*suite{}
	var mods []string
	for _, f := range paths {
		if testfile.Language(f) != "java" {
			continue
		}
		m := p.module(f)
		s := suites[m]
		if s == nil {
			s = &suite{dirs: map[string]bool{}}
			suites[m] = s
			mods = append(mods, m)
		}
		s.files = append(s.files, f)
		s.dirs[path.Dir(f)] = true
	}
	sort.Strings(mods)
	for _, m := range mods {
		s := suites[m]
		var evs []fact.Evidence
		for i, f := range s.files {
			if i == 50 {
				break
			}
			evs = append(evs, ev(f, 1, m))
		}
		fw := "unknown"
		if n, ok := p.fwName[m]; ok {
			fw = n
			evs = append(evs, p.testFw[m])
		}
		var dirs []string
		for d := range s.dirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		dl := make([]any, len(dirs))
		for i, d := range dirs {
			dl[i] = d
		}
		e.add(fact.KindTestSuite, "test_suite:java:"+m, map[string]any{"path": m, "language": "java", "framework": fw, "files": len(s.files), "dirs": dl}, evs...)
	}
}

// isTestSource is the shared test-file convention (internal/testfile).
func isTestSource(f string) bool { return testfile.Is(f) }

// evSpan is ev over lines line..end, so a <dependency> block's evidence
// covers the groupId and artifactId it names.
func evSpan(f string, line, end int, unit string) fact.Evidence {
	e := ev(f, line, unit)
	if end > e.Line {
		e.EndLine = end
	}
	return e
}

func ev(f string, line int, unit string) fact.Evidence {
	if line < 1 {
		line = 1
	}
	return fact.Evidence{Path: f, Line: line, Unit: unit}
}
