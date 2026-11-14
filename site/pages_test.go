package site

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func realPages(t *testing.T) []Page {
	t.Helper()
	ps, err := ReadPages("pages")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 10 {
		t.Fatalf("only %d pages", len(ps))
	}
	return ps
}

var fence = regexp.MustCompile("(?s)```.*?```")

// prose is a page's text outside fenced code and table rows.
func prose(md []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(fence.ReplaceAllString(string(md), ""), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func TestPagesTLDR(t *testing.T) {
	for _, p := range realPages(t) {
		if p.Path == "/404.html" {
			continue
		}
		parts := strings.SplitN(string(p.Markdown), "\n\n", 3)
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "# ") || !strings.HasPrefix(parts[1], "**TL;DR**") {
			t.Errorf("%s: the first paragraph after the heading must start with **TL;DR**", p.File)
		}
	}
}

func TestPagesLength(t *testing.T) {
	for _, p := range realPages(t) {
		n := len(strings.Fields(prose(p.Markdown)))
		if n > 400 {
			t.Errorf("%s: %d words of prose, at most 400", p.File, n)
		}
		if p.Section == "How" && n < 150 {
			t.Errorf("%s: %d words of prose, a How page has at least 150", p.File, n)
		}
	}
}

var (
	helpFlag  = regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)`)
	tableFlag = regexp.MustCompile("(?m)^\\|\\s*`--([a-z][a-z-]*)")
)

// TestPagesFlags keeps every Use page's flag tables in step with the command:
// a flag the command has and the page lacks fails, and so does the reverse.
func TestPagesFlags(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cernly")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/cernly").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	checked := 0
	for _, p := range realPages(t) {
		if len(p.Commands) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, c := range p.Commands {
			out, _ := exec.Command(bin, append(strings.Fields(c), "-h")...).CombinedOutput()
			names := helpFlag.FindAllStringSubmatch(string(out), -1)
			if len(names) == 0 {
				t.Errorf("%s: %s -h printed no flags:\n%s", p.File, c, out)
			}
			for _, m := range names {
				have[m[1]] = true
			}
		}
		documented := map[string]bool{}
		for _, m := range tableFlag.FindAllStringSubmatch(string(p.Markdown), -1) {
			documented[m[1]] = true
		}
		for _, f := range keys(have) {
			if !documented[f] {
				t.Errorf("%s: --%s exists on %s but is not in the page's tables", p.File, f, strings.Join(p.Commands, ", "))
			}
		}
		for _, f := range keys(documented) {
			if !have[f] {
				t.Errorf("%s: the page documents --%s, which %s does not have", p.File, f, strings.Join(p.Commands, ", "))
			}
		}
		checked++
	}
	if checked < 6 {
		t.Errorf("only %d pages name commands", checked)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The real pages build, links included.
func TestPagesBuild(t *testing.T) {
	if err := Build(Options{Src: ".", Schema: filepath.Join("..", "schema"), Out: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}
