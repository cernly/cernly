package command

import (
	"bytes"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/cernly/cernly/repo"
)

// arg is one command-line word as written, with the source word it came from
// (an array element's own word when expanded from a literal array).
type arg struct {
	text    string
	literal bool
	word    *syntax.Word
	at      *hop     // where an array element is written, when it came from an array
	cond    *condRef // the element is added only under this condition (innermost, chained)
	rel     *condRef // of cond, the innermost condition that does not hold for the call; set by underCall
	relTop  *condRef // of cond, the innermost condition beyond the script's top-level flow at the call
}

// found is a recognised command at a file line.
type found struct {
	snip          *snippet
	line, endLine int
	build         *buildCmd
	compose       *composeCmd
	deploy        *deployCmd
	test          *testCmd
	invoke        *invocation
	cwd           string // repo-relative folder a literal `cd` in a workflow step moved to; "" when unchanged or unknown
	via           []hop  // wrapper function lines and array lines in other files the command was read through
	remoteCd      *hop   // a remote command (ssh) after this cd: paths are read from the repo root, an assumption
}

type buildCmd struct {
	dockerfile, context, target string
	tags                        []string
	literalFile, literalContext bool
	push                        bool // --push, or --output with type=registry or push=true
}

type composeCmd struct {
	files, envFiles []string
	condFiles       []condFile // -f flags added only under a condition
	literalFiles    bool
	unknown         []string // expansions before the subcommand that may hold -f flags
	sub             string
}

type condFile struct {
	raw  string
	at   hop
	cond condRef
}

// invocation is a call to another script, task, make target or npm script.
type invocation struct {
	kind    string // srcScript, srcTask, srcMake, srcNpm
	names   []string
	dir     string // -C / -d / --dir, "" when not given
	literal bool
}

// scan parses one snippet and returns every recognised command, including
// commands nested in ssh "…" strings, sh -c "…" and heredocs fed to a shell.
func scan(sn *snippet) ([]found, error) { return scanIn(sn, nil) }

// scanIn is scan with the repo's files at hand, so a script's `source` of
// another file is followed.
func scanIn(sn *snippet, ld *loader) ([]found, error) {
	var out []found
	err := scanText(sn, ld, sn.text, sn.fileLine, &out, 0, false)
	return out, err
}

// tmpl is one command in a wrapper function's body, as written in its file.
type tmpl struct {
	call   *syntax.CallExpr
	render func(*syntax.Word) string
	at     hop
}

// within maps positions in a text nested at p (a heredoc body, a quoted
// word's inside, skip bytes after p) to the file lines of the outer text.
func within(at func(l, c int) int, p syntax.Pos, skip int) func(l, c int) int {
	pl, pc := int(p.Line()), int(p.Col())+skip
	return func(l, c int) int {
		if l == 1 {
			return at(pl, pc+c-1)
		}
		return at(pl+l-1, c)
	}
}

// scanText reads one shell text; at gives the file line of a line and
// column in it. remote is true for a program run on another host (an ssh
// command, or a heredoc fed to ssh).
func scanText(sn *snippet, ld *loader, text []byte, at func(l, c int) int, out *[]found, depth int, remote bool) error {
	if depth > 4 {
		return nil
	}
	masked, m := mask(sn.kind, text)
	f, err := parseShell(masked, sn.path)
	if err != nil {
		return err
	}
	render := func(w *syntax.Word) string { return m.restore(wordText(w)) }
	fileLine := func(p syntax.Pos) int { return at(int(p.Line()), int(p.Col())) }
	self := ""
	if sn.kind == srcScript && depth == 0 {
		self = sn.path
	}
	envs := readEnv(ld, f, frame{path: sn.path, render: render, lineOf: fileLine, src: masked}, self)
	arrays := literalArrays(f, render)
	local := wrapperFuncs(f)
	templates := map[*syntax.CallExpr]bool{}
	for _, calls := range local {
		for _, c := range calls {
			templates[c] = true
		}
	}
	// lookup returns the wrapper templates of a function: this text's own,
	// else one defined in a file the script sourced.
	lookup := func(name string, env *shEnv) []tmpl {
		var ts []tmpl
		if calls, ok := local[name]; ok {
			for _, c := range calls {
				ts = append(ts, tmpl{call: c, render: render, at: hop{path: sn.path, line: fileLine(c.Pos())}})
			}
			return ts
		}
		if env == nil {
			return nil
		}
		if fd, ok := env.funcs[name]; ok && fd.fr.path != sn.path {
			for _, c := range wrapperCalls(fd.decl) {
				ts = append(ts, tmpl{call: c, render: fd.fr.render, at: hop{path: fd.fr.path, line: fd.fr.lineOf(c.Pos())}})
			}
		}
		return ts
	}
	expand := func(words []*syntax.Word, rnd func(*syntax.Word) string, env *shEnv) []arg {
		var args []arg
		for _, w := range words {
			t := rnd(w)
			if name, ok := arrayRef(t); ok {
				if env != nil {
					if env.bad[name] {
						args = append(args, arg{text: t, word: w})
						continue
					}
					if elems, known := env.arrays[name]; known {
						args = append(args, elems...)
						continue
					}
				}
				if elems, known := arrays[name]; known {
					args = append(args, elems...)
					continue
				}
			}
			lit := isLiteral(w)
			if !lit && env != nil {
				// A path under a variable the script set from its own
				// location ("$ROOT/docker-compose.prod.yml") reads as the
				// tracked file it names; anything else stays as written.
				if v, ok := env.evalPath(w); ok && ld.has(v) {
					t, lit = v, true
				}
			}
			args = append(args, arg{text: t, literal: lit, word: w})
		}
		return underCall(args, env)
	}
	// unwrap follows a chain of wrapper functions (compose_up -> compose ->
	// docker compose) to the commands they run, with the lines passed.
	type unwrapped struct {
		args []arg
		via  []hop
	}
	var unwrap func(args []arg, env *shEnv, via []hop, depth int) []unwrapped
	unwrap = func(args []arg, env *shEnv, via []hop, depth int) []unwrapped {
		if len(args) == 0 {
			return nil
		}
		ts := lookup(args[0].text, env)
		if len(ts) == 0 || depth > 6 {
			return []unwrapped{{args, via}}
		}
		var res []unwrapped
		for _, t := range ts {
			wrapped := stripWrappers(spliceArgs(expand(t.call.Args, t.render, env), args[1:]))
			res = append(res, unwrap(wrapped, env, append(append([]hop(nil), via...), t.at), depth+1)...)
		}
		return res
	}
	var cwds map[*syntax.CallExpr]string
	if depth == 0 && sn.kind == srcWorkflow {
		cwds = cdDirs(f, render)
	}
	var remoteCds map[*syntax.CallExpr]*hop
	if remote {
		remoteCds = remoteCdLines(f, sn.path, fileLine, func(n syntax.Node) string {
			return m.restore(frame{src: masked}.text(n))
		})
	}
	var nestedErr error
	nested := func(text string, at2 func(l, c int) int, rem bool) {
		if err := scanText(sn, ld, []byte(text), at2, out, depth+1, rem); err != nil && nestedErr == nil {
			nestedErr = err
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Stmt:
			call, ok := x.Cmd.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			name := path.Base(render(call.Args[0]))
			if name != "ssh" && !isShell(name) {
				return true
			}
			for _, rd := range x.Redirs {
				if (rd.Op == syntax.Hdoc || rd.Op == syntax.DashHdoc) && rd.Hdoc != nil {
					nested(m.restore(heredocBody(rd, masked)), within(at, rd.Hdoc.Pos(), 0), remote || name == "ssh")
				}
			}
		case *syntax.CallExpr:
			if templates[x] {
				return true // read at each call of its function instead
			}
			env := envs[x]
			args := stripWrappers(expand(x.Args, render, env))
			if len(args) == 0 {
				return true
			}
			// A call to a wrapper function (compose() { docker compose … "$@"; })
			// is read as the wrapped command with the call's arguments.
			if ts := lookup(args[0].text, env); len(ts) > 0 {
				for _, t := range ts {
					wrapped := stripWrappers(spliceArgs(expand(t.call.Args, t.render, env), args[1:]))
					// An ssh wrapper (ssh_cmd() { ssh $OPTS "$@"; }): the
					// remote program is the call's own last argument, read
					// as a nested shell program like any ssh "…" string.
					if len(wrapped) > 2 && path.Base(wrapped[0].text) == "ssh" && len(x.Args) > 1 {
						last := x.Args[len(x.Args)-1]
						if tx := wrapped[len(wrapped)-1].text; tx == render(last) && strings.ContainsAny(tx, " \n") {
							nested(m.restore(rawInner(last, masked)), within(at, last.Pos(), 1), true)
						}
					}
					for _, u := range unwrap(wrapped, env, []hop{t.at}, 1) {
						fd := found{snip: sn, line: fileLine(x.Pos()), cwd: cwds[x], via: withOrigins(u.via, u.args, sn.path), remoteCd: remoteCds[x]}
						if recognise(u.args, &fd) {
							*out = append(*out, fd)
						}
					}
				}
				return true
			}
			if inner := nestedShell(args); inner != nil {
				nested(m.restore(rawInner(inner, masked)), within(at, inner.Pos(), 1), remote || path.Base(args[0].text) == "ssh")
			}
			fd := found{snip: sn, line: fileLine(x.Pos()), endLine: fileLine(x.End()), cwd: cwds[x], via: withOrigins(nil, args, sn.path), remoteCd: remoteCds[x]}
			if fd.endLine <= fd.line {
				fd.endLine = 0
			}
			if recognise(args, &fd) {
				*out = append(*out, fd)
			}
		}
		return true
	})
	return nestedErr
}

// underCall marks, for each array element added under a condition, the
// innermost condition that does not also hold for the call (rel). A guard
// `[ -f x ] || exit 1` before both, or one if around both, holds for both.
func underCall(args []arg, env *shEnv) []arg {
	var out []arg
	for i, a := range args {
		if a.cond == nil {
			continue
		}
		if out == nil {
			out = append([]arg(nil), args...)
		}
		var call, top *condRef
		if env != nil {
			call, top = env.cond, env.top
		}
		out[i].rel = notGoverned(a.cond, call)
		out[i].relTop = notGoverned(a.cond, top)
	}
	if out == nil {
		return args
	}
	return out
}

// withOrigins adds the lines in other files that array elements of a command
// were written on (a sourced file that builds the -f list).
func withOrigins(via []hop, args []arg, self string) []hop {
	seen := map[hop]bool{}
	for _, h := range via {
		seen[h] = true
	}
	for _, a := range args {
		if a.at != nil && a.at.path != self && !seen[*a.at] {
			seen[*a.at] = true
			via = append(via, *a.at)
		}
	}
	return via
}

// remoteCdLines finds the `cd` of a remote program (ssh): the folder is on
// another host, so every later command of the program is marked with it, as
// cdDirs walks a workflow step.
func remoteCdLines(f *syntax.File, p string, fileLine func(syntax.Pos) int, text func(syntax.Node) string) map[*syntax.CallExpr]*hop {
	out := map[*syntax.CallExpr]*hop{}
	var cur *hop
	assign := func(n syntax.Node) {
		if cur == nil {
			return
		}
		syntax.Walk(n, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				out[c] = cur
			}
			return true
		})
	}
	cd := func(st *syntax.Stmt) bool {
		c, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || len(c.Args) < 2 || wordText(c.Args[0]) != "cd" {
			return false
		}
		cur = &hop{path: p, line: fileLine(c.Pos()), note: text(c)}
		return true
	}
	for _, st := range f.Stmts {
		if cd(st) {
			continue
		}
		if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.AndStmt {
			for _, part := range andChain(b) {
				if !cd(part) {
					assign(part)
				}
			}
			continue
		}
		assign(st)
	}
	return out
}

// cdDirs follows a workflow step's literal `cd` commands: a `cd <dir>` as a
// statement of its own, or at the head of an && chain, moves every later
// command of the step (a GitHub Actions step starts at the repo root). A cd
// inside a subshell, loop, condition or function is not followed, and a cd
// to an expansion, an absolute path or outside the repo makes the folder
// unknown again ("").
func cdDirs(f *syntax.File, render func(*syntax.Word) string) map[*syntax.CallExpr]string {
	out := map[*syntax.CallExpr]string{}
	cur := "." // "" once a cd went somewhere we cannot follow
	assign := func(n syntax.Node) {
		if cur == "" || cur == "." {
			return
		}
		syntax.Walk(n, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				out[c] = cur
			}
			return true
		})
	}
	cd := func(st *syntax.Stmt) bool {
		c, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || len(c.Args) == 0 || render(c.Args[0]) != "cd" || st.Negated || st.Background {
			return false
		}
		cur = cdTarget(cur, c.Args[1:], render)
		return true
	}
	for _, st := range f.Stmts {
		if cd(st) {
			continue
		}
		if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.AndStmt {
			for _, part := range andChain(b) {
				if !cd(part) {
					assign(part)
				}
			}
			continue
		}
		assign(st)
	}
	return out
}

// andChain flattens a && b && c into its statements, in order.
func andChain(b *syntax.BinaryCmd) []*syntax.Stmt {
	var out []*syntax.Stmt
	for _, s := range []*syntax.Stmt{b.X, b.Y} {
		if inner, ok := s.Cmd.(*syntax.BinaryCmd); ok && inner.Op == syntax.AndStmt && len(s.Redirs) == 0 {
			out = append(out, andChain(inner)...)
			continue
		}
		out = append(out, s)
	}
	return out
}

// cdTarget is the folder after `cd args` from cur, "" when it cannot be
// followed.
func cdTarget(cur string, args []*syntax.Word, render func(*syntax.Word) string) string {
	if cur == "" || len(args) != 1 || !isLiteral(args[0]) {
		return ""
	}
	t := render(args[0])
	if t == "" || t == "-" || strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") {
		return ""
	}
	p, ok := repo.Join(cur, t)
	if !ok {
		return ""
	}
	return p
}

func isShell(name string) bool {
	for _, s := range shells {
		if name == s {
			return true
		}
	}
	return false
}

// stripWrappers drops command prefixes that run the real command: sudo, env
// (with its VAR=value pairs), command, exec, time, nohup.
func stripWrappers(args []arg) []arg {
	for len(args) > 0 {
		switch path.Base(args[0].text) {
		case "sudo", "command", "exec", "time", "nohup":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0].text, "-") {
				args = args[1:]
			}
		case "env":
			args = args[1:]
			for len(args) > 0 && (strings.Contains(args[0].text, "=") || strings.HasPrefix(args[0].text, "-")) {
				args = args[1:]
			}
		default:
			return args
		}
	}
	return args
}

// nestedShell returns the word that holds a nested shell program: the last
// argument of ssh when it contains a space, or the argument after -c of a
// shell.
func nestedShell(args []arg) *syntax.Word {
	name := path.Base(args[0].text)
	if name == "ssh" && len(args) > 2 {
		last := args[len(args)-1]
		if strings.ContainsAny(last.text, " \n") {
			return last.word
		}
	}
	if isShell(name) {
		for i := 1; i+1 < len(args); i++ {
			if args[i].text == "-c" {
				return args[i+1].word
			}
		}
	}
	return nil
}

func recognise(args []arg, fd *found) bool {
	fd.test = parseTest(args)
	name := path.Base(args[0].text)
	rest := args[1:]
	switch name {
	case "docker", "podman":
		rest = skipDockerGlobals(rest)
		if len(rest) == 0 {
			return false
		}
		switch {
		case rest[0].text == "build":
			fd.build = parseBuild(rest[1:])
		case rest[0].text == "buildx" && len(rest) > 1 && rest[1].text == "build":
			fd.build = parseBuild(rest[2:])
		case rest[0].text == "image" && len(rest) > 1 && rest[1].text == "build":
			fd.build = parseBuild(rest[2:])
		case rest[0].text == "compose":
			fd.compose = parseCompose(rest[1:])
		}
	case "docker-compose", "podman-compose":
		fd.compose = parseCompose(rest)
	case "kubectl", "oc":
		fd.deploy = parseKubectl(rest)
	case "kustomize":
		fd.deploy = parseKustomize(rest)
	case "helm":
		fd.deploy = parseHelm(rest)
	case "task", "go-task":
		fd.invoke = parseTask(rest)
	case "make", "gmake":
		fd.invoke = parseMake(rest)
	case "npm", "pnpm", "yarn":
		fd.invoke = parseNpm(rest)
	case "source", ".":
		fd.invoke = scriptCall(rest)
	default:
		if isShell(name) {
			fd.invoke = scriptCall(rest)
		} else if a := args[0]; a.literal && (strings.Contains(a.text, "/") || isScriptName(a.text)) {
			fd.invoke = &invocation{kind: srcScript, names: []string{a.text}, literal: true}
		}
	}
	return fd.build != nil || fd.compose != nil || fd.deploy != nil || fd.test != nil || fd.invoke != nil
}

func skipDockerGlobals(args []arg) []arg {
	valued := map[string]bool{"--context": true, "-c": true, "-H": true, "--host": true, "--config": true, "-l": true, "--log-level": true}
	for len(args) > 0 && strings.HasPrefix(args[0].text, "-") {
		if valued[args[0].text] && len(args) > 1 {
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	return args
}

// flagValue splits "--file=x" and "-f x" forms. It returns the flag name, its
// value and how many args were consumed.
func flagValue(args []arg, i int, valued map[string]bool) (name, value string, literal bool, used int) {
	a := args[i].text
	if !strings.HasPrefix(a, "-") || a == "-" {
		return "", "", false, 0
	}
	if eq := strings.Index(a, "="); eq > 0 && strings.HasPrefix(a, "--") {
		return a[:eq], a[eq+1:], args[i].literal, 1
	}
	if valued[a] && i+1 < len(args) {
		return a, args[i+1].text, args[i+1].literal, 2
	}
	return a, "", true, 1
}

var buildValued = set("-f", "--file", "-t", "--tag", "--target", "--platform", "--build-arg", "--build-context",
	"--cache-from", "--cache-to", "--label", "--network", "--output", "-o", "--progress", "--secret", "--ssh",
	"--iidfile", "--metadata-file", "--builder", "--provenance", "--sbom", "--attest", "--annotation",
	"--add-host", "--allow", "--shm-size", "--ulimit", "--cgroup-parent", "--isolation", "--memory", "-m",
	"--memory-swap", "--cpu-shares", "--cpuset-cpus", "--cpuset-mems", "--cpu-period", "--cpu-quota",
	"--security-opt", "--call", "--build-arg-file")

func parseBuild(args []arg) *buildCmd {
	b := &buildCmd{literalFile: true}
	for i := 0; i < len(args); {
		name, val, lit, used := flagValue(args, i, buildValued)
		if used == 0 {
			// The build context is docker build's one positional argument and
			// comes last; earlier positionals are expansions (extra flags).
			b.context, b.literalContext = args[i].text, args[i].literal
			i++
			continue
		}
		switch name {
		case "-f", "--file":
			b.dockerfile, b.literalFile = val, lit
		case "-t", "--tag":
			b.tags = append(b.tags, val)
		case "--target":
			b.target = val
		case "--push":
			b.push = true
		case "--output", "-o":
			b.push = b.push || strings.Contains(val, "type=registry") || strings.Contains(val, "push=true")
		}
		i += used
	}
	return b
}

var composeValued = set("-f", "--file", "--env-file", "-p", "--project-name", "--profile", "--project-directory",
	"--ansi", "--progress", "--parallel")

// fileFlag is one -f of a compose command, with the words it came from.
type fileFlag struct {
	val   string
	lit   bool
	words []arg
}

func parseCompose(args []arg) *composeCmd {
	c := &composeCmd{literalFiles: true}
	var fs []fileFlag
	defer func() { c.splitFiles(fs) }()
	for i := 0; i < len(args); {
		name, val, lit, used := flagValue(args, i, composeValued)
		if used == 0 {
			if !args[i].literal && strings.HasPrefix(args[i].text, "$") {
				c.unknown = append(c.unknown, args[i].text)
				i++
				continue
			}
			c.sub = args[i].text
			break
		}
		switch name {
		case "-f", "--file":
			fs = append(fs, fileFlag{val: val, lit: lit, words: args[i : i+used]})
		case "--env-file":
			c.envFiles = append(c.envFiles, val)
		}
		i += used
	}
	return c
}

// splitFiles sorts the -f flags into the stack's files and its conditional
// files. The stack's files are the ones whose conditions all hold for the
// script's top-level flow at the command (none, or a guard such as `[ -f.env
// ] || exit 1` above it). When there are none, the ones whose conditions all
// hold for the command itself (one if around both). The rest are
// conditional, also for a command under the same condition, so one deploy
// never forms a variant stack per host condition.
func (c *composeCmd) splitFiles(fs []fileFlag) {
	cond := func(f fileFlag) (cc, rel, relTop *condRef) {
		for _, w := range f.words {
			if w.cond != nil {
				return w.cond, w.rel, w.relTop
			}
		}
		return nil, nil, nil
	}
	anyBase := false
	for _, f := range fs {
		if cc, _, relTop := cond(f); cc == nil || relTop == nil {
			anyBase = true
		}
	}
	for _, f := range fs {
		cc, rel, relTop := cond(f)
		if cc == nil || anyBase && relTop == nil || !anyBase && rel == nil {
			c.files = append(c.files, f.val)
			c.literalFiles = c.literalFiles && f.lit
			continue
		}
		shown := relTop
		if shown == nil {
			shown = rel
		}
		if shown == nil {
			shown = cc
		}
		cf := condFile{raw: f.val, cond: *shown}
		if last := f.words[len(f.words)-1]; last.at != nil {
			cf.at = *last.at
		}
		cf.cond.parent = nil
		c.condFiles = append(c.condFiles, cf)
	}
}

func parseTask(args []arg) *invocation {
	inv := &invocation{kind: srcTask, literal: true}
	valued := set("-d", "--dir", "-t", "--taskfile", "-o", "--output", "-C", "--concurrency")
	for i := 0; i < len(args); {
		if args[i].text == "--" {
			break
		}
		name, val, _, used := flagValue(args, i, valued)
		if used == 0 {
			if !strings.Contains(args[i].text, "=") {
				inv.names = append(inv.names, args[i].text)
				inv.literal = inv.literal && args[i].literal
			}
			i++
			continue
		}
		if name == "-d" || name == "--dir" {
			inv.dir = val
		}
		i += used
	}
	if len(inv.names) == 0 {
		inv.names = []string{"default"}
	}
	return inv
}

func parseMake(args []arg) *invocation {
	inv := &invocation{kind: srcMake, literal: true}
	valued := set("-C", "--directory", "-f", "--file", "--makefile", "-j", "--jobs", "-l", "-o", "-W", "-I")
	for i := 0; i < len(args); {
		name, val, _, used := flagValue(args, i, valued)
		if used == 0 {
			if !strings.Contains(args[i].text, "=") {
				inv.names = append(inv.names, args[i].text)
				inv.literal = inv.literal && args[i].literal
			}
			i++
			continue
		}
		if name == "-C" || name == "--directory" {
			inv.dir = val
		}
		i += used
	}
	return inv
}

// parseNpm only follows the explicit run forms: npm run x, npm run-script x,
// pnpm run x, yarn run x.
func parseNpm(args []arg) *invocation {
	for i, a := range args {
		if strings.HasPrefix(a.text, "-") {
			continue
		}
		if (a.text == "run" || a.text == "run-script") && i+1 < len(args) {
			n := args[i+1]
			return &invocation{kind: srcNpm, names: []string{n.text}, literal: n.literal}
		}
		return nil
	}
	return nil
}

func scriptCall(args []arg) *invocation {
	for _, a := range args {
		if a.text == "-c" || a.text == "-s" {
			return nil // program text or stdin, handled as nested shell
		}
		if strings.HasPrefix(a.text, "-") {
			continue
		}
		return &invocation{kind: srcScript, names: []string{a.text}, literal: a.literal}
	}
	return nil
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// rawInner returns the text inside a quoted word exactly as written, so line
// continuations inside an ssh "…" string keep their line numbers. Other words
// are rendered normally.
func rawInner(w *syntax.Word, src []byte) string {
	if len(w.Parts) == 1 {
		start, end := int(w.Pos().Offset()), int(w.End().Offset())
		if start >= 0 && end <= len(src) && end-start >= 2 {
			switch w.Parts[0].(type) {
			case *syntax.DblQuoted:
				return unescapeDouble(string(src[start+1 : end-1]))
			case *syntax.SglQuoted:
				return string(src[start+1 : end-1])
			}
		}
	}
	return wordText(w)
}

// heredocBody returns the program text a heredoc sends: the body as written
// when the delimiter is quoted (<<'EOF'), else with the backslash escapes the
// shell removes in an unquoted heredoc (\$, \`, \\ and line continuations).
// Parameters and command substitutions stay as text; they are not expanded.
func heredocBody(rd *syntax.Redirect, src []byte) string {
	var body string
	start, end := int(rd.Hdoc.Pos().Offset()), int(rd.Hdoc.End().Offset())
	if start >= 0 && end <= len(src) && start <= end {
		body = string(src[start:end])
	} else {
		var buf bytes.Buffer
		_ = syntax.NewPrinter().Print(&buf, rd.Hdoc)
		body = buf.String()
	}
	if quotedWord(rd.Word) {
		return body
	}
	return unescapeHeredoc(body)
}

// quotedWord reports a heredoc delimiter with any quoting, which turns off
// expansion and escapes in the body.
func quotedWord(w *syntax.Word) bool {
	if w == nil {
		return false
	}
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.Lit:
			if strings.Contains(x.Value, `\`) {
				return true
			}
		}
	}
	return false
}

// literalArrays collects bash arrays assigned with only literal elements
// (COMPOSE_FILES=(-f a.yml -f b.yml)). An array assigned twice with different
// values is left out, so it stays unresolved rather than guessed.
func literalArrays(f *syntax.File, render func(*syntax.Word) string) map[string][]arg {
	out := map[string][]arg{}
	bad := map[string]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		a, ok := n.(*syntax.Assign)
		if !ok || a.Array == nil || a.Name == nil {
			return true
		}
		name := a.Name.Value
		var elems []arg
		for _, e := range a.Array.Elems {
			if e.Value == nil || e.Index != nil || !isLiteral(e.Value) {
				bad[name] = true
				return true
			}
			elems = append(elems, arg{text: render(e.Value), literal: true, word: e.Value})
		}
		if prev, seen := out[name]; seen && !a.Append && fmtArgs(prev) != fmtArgs(elems) {
			bad[name] = true
		}
		if a.Append {
			elems = append(out[name], elems...)
		}
		out[name] = elems
		return true
	})
	for name := range bad {
		delete(out, name)
	}
	return out
}

func fmtArgs(as []arg) string {
	var b strings.Builder
	for _, a := range as {
		b.WriteString(a.text)
		b.WriteByte(0)
	}
	return b.String()
}

// arrayRef recognises ${NAME[@]} and ${NAME[*]}.
func arrayRef(t string) (string, bool) {
	if !strings.HasPrefix(t, "${") || !strings.HasSuffix(t, "]}") {
		return "", false
	}
	inner := t[2 : len(t)-1]
	for _, suf := range []string{"[@]", "[*]"} {
		if strings.HasSuffix(inner, suf) {
			name := strings.TrimSuffix(inner, suf)
			if name != "" && !strings.ContainsAny(name, "${}[]:-+=?#%/ ") {
				return name, true
			}
		}
	}
	return "", false
}

// wrapperFuncs finds functions whose body passes "$@" to a command: the
// calls inside them are templates for each call of the function.
func wrapperFuncs(f *syntax.File) map[string][]*syntax.CallExpr {
	out := map[string][]*syntax.CallExpr{}
	syntax.Walk(f, func(n syntax.Node) bool {
		fd, ok := n.(*syntax.FuncDecl)
		if !ok || fd.Name == nil {
			return true
		}
		syntax.Walk(fd.Body, func(m syntax.Node) bool {
			if c, ok := m.(*syntax.CallExpr); ok {
				for _, w := range c.Args {
					if wordText(w) == "$@" {
						out[fd.Name.Value] = append(out[fd.Name.Value], c)
						break
					}
				}
			}
			return true
		})
		return true
	})
	return out
}

// spliceArgs replaces "$@" in a wrapper's command with the call's arguments.
func spliceArgs(tmpl, callArgs []arg) []arg {
	var out []arg
	for _, a := range tmpl {
		if a.text == "$@" {
			out = append(out, callArgs...)
			continue
		}
		out = append(out, a)
	}
	return out
}
