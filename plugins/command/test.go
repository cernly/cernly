package command

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/dotnetsln"
	"github.com/cernly/cernly/repo"
)

// testCmd is a command that runs tests. Tests never run here; the command
// is read as written.
type testCmd struct {
	tool    string
	dir     string // as written; "" = where the command runs
	command string
	sln     string // a dotnet solution target (.sln, .slnx) as written; "" otherwise
}

func joinArgs(args []arg) string {
	var b []string
	for _, a := range args {
		b = append(b, a.text)
	}
	s := strings.Join(b, " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// dirFlag returns the value of the first of flags in args.
func dirFlag(args []arg, flags ...string) string {
	for i, a := range args {
		for _, f := range flags {
			if a.text == f && i+1 < len(args) {
				return args[i+1].text
			}
			if strings.HasPrefix(a.text, f+"=") {
				return strings.TrimPrefix(a.text, f+"=")
			}
		}
	}
	return ""
}

func positional(args []arg) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a.text, "-") {
			out = append(out, a.text)
		}
	}
	return out
}

func has(xs []string, want ...string) bool {
	for _, x := range xs {
		for _, w := range want {
			if x == w || strings.HasSuffix(x, ":"+w) {
				return true
			}
		}
	}
	return false
}

// dotnetValueFlags are the options of dotnet test that take a value, which
// must not be read as the project or solution path. They count only after
// the test verb.
var dotnetValueFlags = map[string]bool{
	"-c": true, "--configuration": true, "-f": true, "--framework": true, "-r": true, "--runtime": true,
	"-o": true, "--output": true, "-s": true, "--settings": true, "-l": true, "--logger": true,
	"-v": true, "--verbosity": true, "-a": true, "--test-adapter-path": true, "-d": true, "--diag": true,
	"-e": true, "--environment": true, "--filter": true, "--results-directory": true, "--collect": true,
	"--arch": true, "--os": true, "--blame-hang-timeout": true, "--blame-crash-dump-type": true,
	"--blame-hang-dump-type": true, "--project": true, "--solution": true,
}

func parseTest(args []arg) *testCmd {
	if len(args) == 0 {
		return nil
	}
	name := path.Base(args[0].text)
	rest := args[1:]
	t := &testCmd{command: joinArgs(args)}
	switch name {
	case "go":
		r := rest
		if len(r) >= 2 && r[0].text == "-C" {
			t.dir, r = r[1].text, r[2:]
		}
		if len(r) == 0 || r[0].text != "test" {
			return nil
		}
		t.tool = "go"
	case "npm", "pnpm", "yarn", "bun":
		p := positional(rest)
		if len(p) == 0 {
			return nil
		}
		switch {
		case p[0] == "test" || p[0] == "t":
		case (p[0] == "run" || p[0] == "run-script") && len(p) > 1 && (p[1] == "test" || strings.HasPrefix(p[1], "test:")):
		default:
			return nil
		}
		t.tool = name
		t.dir = dirFlag(rest, "--prefix", "-C", "--dir", "--cwd")
	case "npx", "pnpx", "bunx":
		p := positional(rest)
		if len(p) == 0 {
			return nil
		}
		return parseTest(append([]arg{{text: p[0], literal: true}}, rest[1:]...))
	case "jest", "vitest", "mocha", "ava", "pytest":
		t.tool = name
	case "playwright":
		if p := positional(rest); len(p) == 0 || p[0] != "test" {
			return nil
		}
		t.tool = name
	case "cypress":
		if p := positional(rest); len(p) == 0 || p[0] != "run" {
			return nil
		}
		t.tool = name
	case "python", "python3":
		if len(rest) < 2 || rest[0].text != "-m" || (rest[1].text != "pytest" && rest[1].text != "unittest") {
			return nil
		}
		t.tool = "pytest"
		if rest[1].text == "unittest" {
			t.tool = "unittest"
		}
	case "mvn", "mvnw":
		goals := positional(rest)
		if !has(goals, "test", "verify", "install", "package", "deploy") {
			return nil
		}
		for _, a := range rest {
			if a.text == "-DskipTests" || strings.HasPrefix(a.text, "-Dmaven.test.skip") || strings.HasPrefix(a.text, "-DskipTests=true") {
				return nil
			}
		}
		t.tool = "maven"
		if f := dirFlag(rest, "-f", "--file"); f != "" {
			t.dir = path.Dir(f)
			if !strings.HasSuffix(f, ".xml") {
				t.dir = f
			}
		}
	case "gradle", "gradlew":
		if !has(positional(rest), "test", "check", "build") {
			return nil
		}
		for i, a := range rest {
			if a.text == "-x" && i+1 < len(rest) && (rest[i+1].text == "test" || strings.HasSuffix(rest[i+1].text, ":test")) {
				return nil
			}
		}
		t.tool = "gradle"
		t.dir = dirFlag(rest, "-p", "--project-dir")
	case "dotnet":
		var p []string
		target := ""
		for i := 0; i < len(rest); i++ {
			x := rest[i].text
			if !strings.HasPrefix(x, "-") {
				p = append(p, x)
				continue
			}
			if len(p) == 0 {
				continue // a dotnet host option (-d, --info): it takes no value
			}
			if x == "--list-tests" || x == "-t" {
				return nil // discovers only; it runs nothing
			}
			name, val, eq := strings.Cut(x, "=")
			if !dotnetValueFlags[name] {
				continue
			}
			if !eq && i+1 < len(rest) {
				i++
				val = rest[i].text
			}
			if name == "--project" || name == "--solution" {
				target = val
			}
		}
		if len(p) == 0 || p[0] != "test" {
			return nil
		}
		if target == "" && len(p) > 1 {
			target = p[1]
		}
		t.tool = "dotnet"
		if target != "" {
			d := strings.ReplaceAll(target, `\`, "/") // a Windows path
			switch strings.ToLower(path.Ext(d)) {
			case ".csproj", ".fsproj", ".vbproj", ".sln", ".slnx":
				if e := strings.ToLower(path.Ext(d)); e == ".sln" || e == ".slnx" {
					t.sln = strings.TrimPrefix(d, "./")
				}
				d = path.Dir(d)
			}
			d = strings.TrimSuffix(strings.TrimPrefix(d, "./"), "/")
			if d == "" {
				d = "."
			}
			t.dir = d
		}
	default:
		return nil
	}
	return t
}

// solutionOf finds the tracked solution file a dotnet test target names, the
// way resolveDir finds its folder: from the repo root, then from base.
func (st *state) solutionOf(sln, base, dir string) (string, bool) {
	for _, b := range []string{".", base} {
		if p, ok := repo.Join(b, sln); ok && path.Dir(p) == dir && st.r.Has(p) {
			return p, true
		}
	}
	return "", false
}

func (st *state) testFacts() []fact.Fact {
	type agg struct {
		tool, dir, command string
		projects           map[string]bool // folders a solution lists; nil once a run without a list joins
		open               bool
		ev                 []fact.Evidence
		wfs                map[string]any
	}
	aggs := map[string]*agg{}
	for _, f := range st.found {
		t := f.test
		if t == nil {
			continue
		}
		base := "."
		if f.snip.kind == srcNpm || f.snip.kind == srcMake || f.snip.kind == srcTask {
			base = path.Dir(f.snip.path) // scripts and targets run in their own folder
		} else if f.cwd != "" {
			base = f.cwd // a literal cd earlier in the workflow step
		}
		dir := base
		if t.dir != "" {
			if p, ok := st.resolveDir(t.dir, base); ok {
				dir = p
			} else {
				dir = t.dir
			}
		}
		id := t.tool + ":" + dir
		a := aggs[id]
		if a == nil {
			a = &agg{tool: t.tool, dir: dir, command: t.command, wfs: map[string]any{}, projects: map[string]bool{}}
			aggs[id] = a
		}
		if t.tool == "dotnet" {
			var list []string
			if t.sln != "" {
				if sp, ok := st.solutionOf(t.sln, base, dir); ok {
					data, err := st.r.Read(sp)
					if err == nil {
						list, err = dotnetsln.Projects(data, sp)
					}
					if err != nil {
						st.testErrs = append(st.testErrs, fact.LocationError{Path: f.snip.path, Line: f.line, Message: "solution " + sp + ": " + err.Error()})
					}
				}
			}
			if len(list) == 0 {
				a.open = true // no list: the run covers what it covers today
			}
			for _, p := range list {
				a.projects[p] = true
			}
		}
		chain, wfs := st.reachOf(f.snip, false)
		a.ev = append(a.ev, st.evidence(f, chain...)...)
		for _, w := range wfs {
			m := w.(map[string]any)
			wid := m["path"].(string) + "#" + m["job"].(string)
			if _, seen := a.wfs[wid]; !seen {
				a.wfs[wid] = w
			}
		}
	}
	ids := make([]string, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []fact.Fact
	for _, id := range ids {
		a := aggs[id]
		attrs := map[string]any{"tool": a.tool, "dir": a.dir, "command": a.command, "ci_invoked": len(a.wfs) > 0}
		if len(a.wfs) > 0 {
			attrs["workflows"] = fact.SortedValues(a.wfs)
		}
		if a.tool == "dotnet" && !a.open && len(a.projects) > 0 {
			ps := make([]string, 0, len(a.projects))
			for p := range a.projects {
				ps = append(ps, p)
			}
			sort.Strings(ps)
			list := make([]any, len(ps))
			for i, p := range ps {
				list[i] = p
			}
			attrs["projects"] = list
		}
		out = append(out, fact.Fact{Kind: fact.KindTestRun, Key: "test_run:" + id, Attrs: attrs, Plugin: "commands", Evidence: a.ev})
	}
	return out
}
