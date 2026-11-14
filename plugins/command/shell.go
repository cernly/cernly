// Package command finds build and deploy commands (docker build, docker
// compose -f …) in workflow steps, shell scripts, Taskfiles, Makefiles and
// package.json scripts. It parses shell with mvdan.cc/sh/v3/syntax and never
// executes anything: only the syntax package is imported, never the
// interpreter.
package command

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Template syntaxes that are not shell: GitHub Actions ${{ … }}, Taskfile /
// Go templates {{ … }}, and make variables $(VAR) in recipes.
var (
	ghaExpr = regexp.MustCompile(`\$\{\{[^}]*\}\}`)
	goTmpl  = regexp.MustCompile(`\{\{[^}]*\}\}`)
	makeVar = regexp.MustCompile(`\$\(([A-Za-z0-9_.]+)\)`)
)

// masker swaps template syntax for shell variables of the same length, so the
// text parses and every line and column stays put, and swaps the original
// text back when a word is rendered.
type masker struct{ subst map[string]string }

func (m *masker) restore(s string) string {
	for ph, orig := range m.subst {
		s = strings.ReplaceAll(s, ph, orig)
	}
	return s
}

func (m *masker) placeholders(re *regexp.Regexp, src []byte) []byte {
	return re.ReplaceAllFunc(src, func(match []byte) []byte {
		ph := fmt.Sprintf("${P%d", len(m.subst))
		if len(ph)+1 > len(match) {
			return bytes.Repeat([]byte("_"), len(match))
		}
		ph += strings.Repeat("_", len(match)-len(ph)-1) + "}"
		m.subst[ph] = string(match)
		return []byte(ph)
	})
}

// mask prepares text from a snippet of the given kind for the shell parser.
func mask(kind string, src []byte) ([]byte, *masker) {
	m := &masker{subst: map[string]string{}}
	switch kind {
	case srcWorkflow:
		src = m.placeholders(ghaExpr, src)
	case srcTask:
		src = m.placeholders(goTmpl, src)
	case srcMake:
		// $$ is make's escape for a literal $; $(VAR) reads the same as ${VAR}.
		src = bytes.ReplaceAll(src, []byte("$$"), []byte(`\$`))
		src = makeVar.ReplaceAll(src, []byte("$${$1}"))
	}
	return src, m
}

func parseShell(src []byte, name string) (*syntax.File, error) {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	return p.Parse(bytes.NewReader(src), name)
}

// wordText renders a word the way an auditor would read it: literal parts
// unquoted, expansions kept as written (${REGISTRY}/x:${tag}).
func wordText(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		writePart(&b, part)
	}
	return b.String()
}

func writePart(b *strings.Builder, part syntax.WordPart) {
	switch p := part.(type) {
	case *syntax.Lit:
		b.WriteString(unescapeUnquoted(p.Value))
	case *syntax.SglQuoted:
		b.WriteString(p.Value)
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			if l, ok := q.(*syntax.Lit); ok {
				b.WriteString(unescapeDouble(l.Value))
				continue
			}
			writePart(b, q)
		}
	default:
		var buf bytes.Buffer
		_ = syntax.NewPrinter().Print(&buf, part)
		b.Write(buf.Bytes())
	}
}

// isLiteral reports whether a word has no expansions at all.
func isLiteral(w *syntax.Word) bool {
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeDouble applies the escapes valid inside double quotes. A backslash
// before a newline is kept, so text parsed again as shell (an ssh command)
// keeps its line numbers.
func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '$', '`', '"', '\\':
				i++
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeHeredoc removes the escapes of an unquoted heredoc body: \$, \`,
// \\ and backslash-newline. Other backslashes stay, as in bash.
func unescapeHeredoc(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '$', '`', '\\':
				i++
			case '\n':
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
