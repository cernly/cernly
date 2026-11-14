package dockerfile

import (
	"bytes"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// findBuilds reads a RUN script with the shell parser (never executed) and
// returns the go build / go install packages and npm workspace builds in it.
func findBuilds(script string) []build {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil
	}
	var out []build
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
		if len(a) < 2 {
			return true
		}
		raw := strings.Join(a, " ")
		switch {
		case a[0] == "go" && (a[1] == "build" || a[1] == "install"):
			out = append(out, goBuilds(a[2:], raw)...)
		case a[0] == "npm" || a[0] == "pnpm":
			if ws := workspace(a[1:]); ws != "" && slices.Contains(a, "build") {
				out = append(out, build{kind: "npm", pkg: ws, raw: raw})
			}
		}
		return true
	})
	return out
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
