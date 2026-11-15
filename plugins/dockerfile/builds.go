package dockerfile

import (
	"bytes"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// findBuilds reads a RUN script with the shell parser (never executed) and
// returns the go build / go install packages, npm workspace builds and dotnet
// publish / build projects in it, plus the command words of the calls that are
// neither a known build nor setup.
func findBuilds(script string) ([]build, []string) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		// Unreadable: name the first token so the stage does not look clean.
		if f := strings.Fields(script); len(f) > 0 {
			return nil, []string{f[0]}
		}
		return nil, nil
	}
	var out []build
	var others []string
	cd := false // a cd, pushd or popd came before: later paths do not resolve against WORKDIR
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		var a []string
		for _, w := range call.Args {
			a = append(a, render(w))
		}
		// Skip leading VAR=value words that some scripts write as arguments.
		for len(a) > 0 && strings.Contains(a[0], "=") && !strings.HasPrefix(a[0], "-") {
			a = a[1:]
		}
		if len(a) == 0 {
			return true
		}
		raw := strings.Join(a, " ")
		known := false
		start := len(out)
		if len(a) >= 2 {
			switch {
			case a[0] == "go" && (a[1] == "build" || a[1] == "install"):
				out = append(out, goBuilds(a[2:], raw)...)
				known = true
			case a[0] == "npm" || a[0] == "pnpm":
				if ws := workspace(a[1:]); ws != "" && slices.Contains(a, "build") {
					out = append(out, build{kind: "npm", pkg: ws, raw: raw})
					known = true
				}
			case a[0] == "dotnet" && (a[1] == "publish" || a[1] == "build"):
				out = append(out, dotnetBuild(a[2:], raw))
				known = true
			}
		}
		for i := start; cd && i < len(out); i++ {
			out[i].unresolved = true
		}
		if a[0] == "cd" || a[0] == "pushd" || a[0] == "popd" {
			cd = true
		}
		if word := commandWord(a); !known && !setupWords[word] {
			others = append(others, word)
		}
		return true
	})
	return out, others
}

// subcommandTools are the commands whose first non-flag argument is part of
// the command word ("dotnet test").
var subcommandTools = map[string]bool{"dotnet": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "pip": true, "pip3": true}

// setupWords are command words that prepare the image without building code.
var setupWords = map[string]bool{
	"apt-get": true, "apt": true, "apk": true, "yum": true, "dnf": true, "microdnf": true,
	"pip install": true, "pip3 install": true,
	"npm ci": true, "npm install": true, "pnpm install": true, "yarn install": true, "yarn": true,
	"go mod download": true, "dotnet restore": true,
	"mkdir": true, "chmod": true, "chown": true, "cp": true, "mv": true, "rm": true, "ln": true,
	"useradd": true, "adduser": true, "addgroup": true, "groupadd": true,
	"true": true, "echo": true, "set": true, "export": true, "cd": true, "test": true,
}

// commandWord is the command's first word, with its subcommand for the tools
// in subcommandTools. a has no leading VAR= words.
func commandWord(a []string) string {
	if !subcommandTools[a[0]] {
		return a[0]
	}
	rest := a[1:]
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		i++
	}
	if i == len(rest) {
		return a[0]
	}
	word := a[0] + " " + rest[i]
	if word == "go mod" && i+1 < len(rest) {
		word += " " + rest[i+1]
	}
	return word
}

var dotnetValued = map[string]bool{"-c": true, "--configuration": true, "-o": true, "--output": true, "-r": true,
	"--runtime": true, "-f": true, "--framework": true, "-a": true, "--arch": true, "--os": true, "-v": true,
	"--verbosity": true, "-s": true, "--artifacts-path": true, "-property": true, "-verbosity": true, "-p": true, "--source": true, "--version-suffix": true, "--manifest": true, "--property": true}

// dotnetBuild reads the arguments after dotnet publish / build: the one
// non-flag argument is the project, solution or folder; "" means the stage
// WORKDIR. Several of them are not read: the build stays unresolved.
func dotnetBuild(a []string, raw string) build {
	b := build{kind: "dotnet", raw: raw}
	for i := 0; i < len(a); i++ {
		arg := a[i]
		if strings.HasPrefix(arg, "-") {
			name, val, hasEq := strings.Cut(arg, "=")
			if dotnetValued[name] && !hasEq && i+1 < len(a) {
				val = a[i+1]
				i++
			}
			if name == "--self-contained" && !hasEq && i+1 < len(a) && (a[i+1] == "true" || a[i+1] == "false") {
				i++
			}
			if (name == "-tl" || name == "--tl" || name == "--terminal-logger") && !hasEq && i+1 < len(a) &&
				(a[i+1] == "auto" || a[i+1] == "on" || a[i+1] == "off") {
				i++
			}
			if name == "-o" || name == "--output" {
				b.output = val
			}
			continue
		}
		if b.pkg == "" {
			b.pkg = arg
		} else {
			b.unresolved = true
		}
	}
	return b
}

var goValued = map[string]bool{"-o": true, "-ldflags": true, "-gcflags": true, "-asmflags": true, "-tags": true,
	"-mod": true, "-modfile": true, "-overlay": true, "-pgo": true, "-p": true, "-buildmode": true,
	"-compiler": true, "-gccgoflags": true, "-installsuffix": true, "-pkgdir": true, "-toolexec": true}

func goBuilds(a []string, raw string) []build {
	var out []build
	output := ""
	var pkgs []string
	for i := 0; i < len(a); i++ {
		arg := a[i]
		if strings.HasPrefix(arg, "-") {
			name, val, hasEq := strings.Cut(arg, "=")
			if goValued[name] && !hasEq && i+1 < len(a) {
				val = a[i+1]
				i++
			}
			if name == "-o" {
				output = val
			}
			continue
		}
		pkgs = append(pkgs, arg)
	}
	if len(pkgs) == 0 {
		pkgs = []string{"."}
	}
	for _, p := range pkgs {
		if strings.HasSuffix(p, ".go") {
			continue
		}
		b := build{kind: "go", pkg: p, raw: raw}
		if len(pkgs) == 1 {
			b.output = output
		}
		out = append(out, b)
	}
	return out
}

// workspace finds --workspace=x, --workspace x, -w x (npm) and --filter x
// (pnpm).
func workspace(a []string) string {
	for i, arg := range a {
		for _, f := range []string{"--workspace", "-w", "--filter"} {
			if v, ok := strings.CutPrefix(arg, f+"="); ok {
				return v
			}
			if arg == f && i+1 < len(a) {
				return a[i+1]
			}
		}
	}
	return ""
}

func render(w *syntax.Word) string {
	if lit := w.Lit(); lit != "" {
		return lit
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if l, ok := q.(*syntax.Lit); ok {
					b.WriteString(l.Value)
				} else {
					var buf bytes.Buffer
					_ = syntax.NewPrinter().Print(&buf, q)
					b.Write(buf.Bytes())
				}
			}
		default:
			var buf bytes.Buffer
			_ = syntax.NewPrinter().Print(&buf, part)
			b.Write(buf.Bytes())
		}
	}
	return b.String()
}
