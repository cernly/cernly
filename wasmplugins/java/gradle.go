package main

import (
	"path"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/version"
)

// Gradle build scripts are read as tokens, never run. What a script
// computes (string interpolation, variables, function calls) is kept as
// written with version scheme unknown.

// gradleScope maps a configuration to a usage scope.
func gradleScope(cfg string) (string, bool) {
	switch cfg {
	case "implementation", "api", "compile", "runtimeOnly", "runtime", "compileOnlyApi":
		return "prod", true
	case "compileOnly", "annotationProcessor", "kapt", "ksp":
		return "build", true
	case "developmentOnly":
		return "dev", true
	case "testImplementation", "testRuntimeOnly", "testCompileOnly", "testAnnotationProcessor", "testCompile",
		"testFixturesImplementation", "testFixturesApi", "integrationTestImplementation":
		return "test", true
	}
	return "", false
}

func gradle(p *project, paths []string, e *emitter) {
	cat := catalogs(p, paths, e)
	projects := settingsProjects(p, paths)
	for _, f := range paths {
		b := path.Base(f)
		if b != "build.gradle" && b != "build.gradle.kts" {
			continue
		}
		mod := path.Dir(f)
		ts := lex(p.files[f], true)
		for i := 0; i+1 < len(ts); i++ {
			if !ts[i].is("dependencies") || !ts[i+1].is("{") {
				continue
			}
			end := closing(ts, i+1)
			gradleDeps(p, f, mod, ts[i+2:end], cat, projects, e)
			i = end
		}
	}
}

// gradleDeps reads the statements of one dependencies { } block.
func gradleDeps(p *project, f, mod string, ts []token, cat catalog, projects map[string]string, e *emitter) {
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.kind != tIdent {
			continue
		}
		scope, ok := gradleScope(t.text)
		if !ok {
			// skip a nested block (constraints { }, components { }) as a
			// unit; the branches of an if/else are read, both of them
			if i+1 < len(ts) && ts[i+1].is("{") && !t.is("else") {
				i = closing(ts, i+1)
			}
			continue
		}
		// The argument: cfg(x) or cfg x up to the end of the line; a line
		// that ends in a comma goes on (cfg "a",\n "b").
		var args []token
		j := i + 1
		if j < len(ts) && ts[j].is("(") {
			end := closing(ts, j)
			args = ts[j+1 : end]
			j = end
		} else {
			line := t.line
			for j < len(ts) && ts[j].line == line {
				args = append(args, ts[j])
				if j+1 < len(ts) && ts[j].is(",") && ts[j+1].line > line {
					line = ts[j+1].line
				}
				j++
			}
			j--
		}
		i = j
		// Several coordinates in one call (cfg "a", "b"): one dependency each.
		if strs, ok := stringList(args); ok && len(strs) > 1 {
			for _, s := range strs {
				c := t
				c.line = s.line
				gradleDep(p, f, mod, c, scope, []token{s}, cat, projects, e)
			}
			continue
		}
		gradleDep(p, f, mod, t, scope, args, cat, projects, e)
	}
}

// stringList reads "a", "b", ... (strings separated by commas).
func stringList(args []token) ([]token, bool) {
	var out []token
	for k, a := range args {
		if k%2 == 0 {
			if a.kind != tString {
				return nil, false
			}
			out = append(out, a)
		} else if !a.is(",") {
			return nil, false
		}
	}
	return out, len(args)%2 == 1
}

func gradleDep(p *project, f, mod string, cfg token, scope string, args []token, cat catalog, projects map[string]string, e *emitter) {
	ext := map[string]any{"configuration": cfg.text}
	// platform(x) / enforcedPlatform(x): a BOM; read the inner reference.
	if len(args) >= 3 && (args[0].is("platform") || args[0].is("enforcedPlatform")) && args[1].is("(") {
		ext["platform"] = true
		args = args[2:closing(args, 1)]
	}
	// Strip a trailing configuration closure: { exclude ... }.
	for k, a := range args {
		if a.is("{") {
			args = args[:k]
			break
		}
	}
	for len(args) > 0 && args[len(args)-1].is(",") {
		args = args[:len(args)-1]
	}
	evs := []fact.Evidence{ev(f, cfg.line, mod)}
	var group, artifact, ver string
	computed := false
	switch {
	case len(args) == 0:
		return
	case args[0].kind == tString && len(args) == 1:
		// "group:artifact:version[:classifier][@ext]"
		c := strings.SplitN(strings.SplitN(args[0].text, "@", 2)[0], ":", 4)
		if len(c) < 2 {
			return
		}
		group, artifact = c[0], c[1]
		if len(c) >= 3 {
			ver = c[2]
		}
		computed = args[0].interp
	case args[0].is("libs") || (args[0].kind == tIdent && len(args) > 2 && args[1].is(".")):
		ref, n := dotted(args)
		if n != len(args) || !strings.HasPrefix(ref, "libs.") {
			e.fail(f, cfg.line, "dependency not read: "+joinText(args))
			return
		}
		alias := strings.TrimPrefix(ref, "libs.")
		entries, ok := cat[alias]
		if !ok {
			e.fail(f, cfg.line, "version catalog entry "+ref+" not found in gradle/libs.versions.toml")
			return
		}
		for _, ce := range entries {
			x := map[string]any{"configuration": cfg.text, "catalog": ref}
			if ext["platform"] != nil {
				x["platform"] = true
			}
			emitDep(p, f, mod, scope, ce.group, ce.artifact, ce.version, false, append([]fact.Evidence{ev(f, cfg.line, mod)}, ce.ev...), x, e)
		}
		return
	case args[0].is("project") && len(args) >= 3 && args[1].is("(") && args[2].kind == tString:
		target := args[2].text
		dir, ok := projects[target]
		if !ok {
			dir = strings.ReplaceAll(strings.TrimPrefix(target, ":"), ":", "/")
		}
		attrs := map[string]any{"ecosystem": "maven", "name": target, "version": "", "direct": true, "scope": scope,
			"version_info": map[string]any{"raw": "", "scheme": "unknown"},
			"internal":     map[string]any{"name": target, "path": dir, "spec": `project("` + target + `")`}, "ext": ext}
		e.add(fact.KindDependency, "dependency:maven:"+mod+":project:"+target, attrs, evs...)
		return
	case args[0].is("kotlin") && len(args) >= 3 && args[1].is("(") && args[2].kind == tString:
		group, artifact = "org.jetbrains.kotlin", "kotlin-"+args[2].text
	default:
		// Named arguments: group: 'g', name: 'a', version: 'v' (Groovy) or
		// group = "g", ... (Kotlin).
		named := map[string]token{}
		for k := 0; k+2 < len(args); k++ {
			if args[k].kind == tIdent && (args[k+1].is(":") || args[k+1].is("=")) {
				switch v := args[k+2]; {
				case v.kind == tString:
					named[args[k].text] = v
				case v.kind == tIdent:
					// a variable: its value is computed when the script runs
					named[args[k].text] = token{kind: tString, text: "$" + v.text, line: v.line, interp: true}
				}
			}
		}
		g, okG := named["group"]
		a, okA := named["name"]
		if !okG || !okA {
			if !args[0].is("files") && !args[0].is("fileTree") && !args[0].is("gradleApi") && !args[0].is("localGroovy") {
				e.fail(f, cfg.line, "dependency not read: "+joinText(args))
			}
			return
		}
		group, artifact = g.text, a.text
		if v, ok := named["version"]; ok {
			ver = v.text
			computed = v.interp
		}
		computed = computed || g.interp || a.interp
	}
	emitDep(p, f, mod, scope, group, artifact, ver, computed, evs, ext, e)
}

func emitDep(p *project, f, mod, scope, group, artifact, ver string, computed bool, evs []fact.Evidence, ext map[string]any, e *emitter) {
	if strings.Contains(group+artifact, "$") {
		e.fail(f, evs[0].Line, "dependency with a computed group or artifact: "+group+":"+artifact)
		return
	}
	p.noteDriver(mod, group, artifact)
	p.noteTest(mod, group, artifact, evs[0])
	attrs := map[string]any{"ecosystem": "maven", "name": group + ":" + artifact, "version": ver, "direct": true, "scope": scope}
	if computed || ver == "" || strings.Contains(ver, "$") {
		attrs["version_info"] = map[string]any{"raw": ver, "scheme": "unknown"}
		if computed || strings.Contains(ver, "$") {
			ext["computed"] = true // the script computes it when Gradle runs
		} else {
			ext["managed_by"] = "a platform or the Gradle plugin"
		}
	} else {
		attrs["version_info"] = version.Parse(ver).Attrs()
	}
	attrs["ext"] = ext
	e.add(fact.KindDependency, "dependency:maven:"+mod+":"+group+":"+artifact, attrs, evs...)
}

// dotted reads a.b.c and returns it with the number of tokens used.
func dotted(ts []token) (string, int) {
	var parts []string
	i := 0
	for i < len(ts) && ts[i].kind == tIdent {
		parts = append(parts, ts[i].text)
		i++
		if i < len(ts) && ts[i].is(".") {
			i++
			continue
		}
		break
	}
	return strings.Join(parts, "."), i
}

func joinText(ts []token) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 8 {
			b.WriteString(" ...")
			break
		}
		if t.kind == tString {
			b.WriteString(`"` + t.text + `"`)
		} else {
			b.WriteString(t.text)
		}
	}
	return b.String()
}

// settingsProjects maps include(":a:b") in settings.gradle(.kts) to folders,
// with projectDir overrides of the form project(":x").projectDir = file("y").
func settingsProjects(p *project, paths []string) map[string]string {
	out := map[string]string{}
	for _, f := range paths {
		b := path.Base(f)
		if b != "settings.gradle" && b != "settings.gradle.kts" {
			continue
		}
		root := path.Dir(f)
		ts := lex(p.files[f], true)
		for i := 0; i < len(ts); i++ {
			if ts[i].is("include") {
				for j := i + 1; j < len(ts) && (ts[j].line == ts[i].line || ts[j-1].is(",")); j++ {
					if ts[j].kind == tString {
						name := ts[j].text
						if !strings.HasPrefix(name, ":") {
							name = ":" + name
						}
						out[name] = path.Join(root, strings.ReplaceAll(strings.TrimPrefix(name, ":"), ":", "/"))
					}
				}
			}
			if ts[i].is("project") && i+9 < len(ts) && ts[i+1].is("(") && ts[i+2].kind == tString && ts[i+3].is(")") &&
				ts[i+4].is(".") && ts[i+5].is("projectDir") && ts[i+6].is("=") && ts[i+7].is("file") && ts[i+8].is("(") && ts[i+9].kind == tString {
				out[ts[i+2].text] = path.Join(root, ts[i+9].text)
			}
		}
	}
	return out
}
