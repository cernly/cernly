package site

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func build(t *testing.T, pages string) (string, error) {
	t.Helper()
	out := t.TempDir()
	return out, Build(Options{Src: ".", Pages: pages, Schema: filepath.Join("..", "schema"),
		Skills: filepath.Join("..", "skills"), Out: out})
}

func mustBuild(t *testing.T) string {
	t.Helper()
	out, err := build(t, filepath.Join("testdata", "pages"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		m[rel] = read(t, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuildDeterministic(t *testing.T) {
	a, b := tree(t, mustBuild(t)), tree(t, mustBuild(t))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("trees differ in size: %d, %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s differs between builds", k)
		}
	}
}

func TestMarkdownTwin(t *testing.T) {
	out := mustBuild(t)
	md := read(t, filepath.Join(out, "use", "scan", "index.md"))
	if strings.Contains(md, "commands:") || !strings.HasPrefix(md, "# cernly scan\n") {
		t.Errorf("twin is not the page's Markdown without front matter:\n%s", md)
	}
	h := read(t, filepath.Join(out, "use", "scan", "index.html"))
	for _, want := range []string{
		`<link rel="alternate" type="text/markdown" href="/use/scan/index.md">`,
		`<link rel="canonical" href="https://cernly.dev/use/scan/">`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("html lacks %s", want)
		}
	}
}

var (
	llmsLine = regexp.MustCompile(`^- \[[^\]]+\]\((https://cernly\.dev/[^)]*index\.md)\): .+$`)
	locTag   = regexp.MustCompile(`<loc>([^<]+)</loc>`)
)

// fileFor maps a cernly.dev URL to the file that serves it.
func fileFor(out, url string) string {
	p := strings.TrimPrefix(url, origin)
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	return filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(p, "/")))
}

func TestAgentFiles(t *testing.T) {
	out := mustBuild(t)
	llms := read(t, filepath.Join(out, "llms.txt"))
	if !strings.HasPrefix(llms, "# Cernly\n") {
		t.Errorf("llms.txt starts %q", llms[:20])
	}
	iu, ih, ip := strings.Index(llms, "\n## Use\n"), strings.Index(llms, "\n## How\n"), strings.Index(llms, "\n## Project\n")
	if iu < 0 || ih < iu || ip < ih {
		t.Errorf("sections out of order: Use %d, How %d, Project %d", iu, ih, ip)
	}
	var urls []string
	for _, line := range strings.Split(llms, "\n") {
		if strings.HasPrefix(line, "- ") {
			m := llmsLine.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("llms.txt line %q", line)
				continue
			}
			urls = append(urls, m[1])
		}
	}
	if len(urls) != 3 {
		t.Errorf("llms.txt lists %d pages, want 3 (Use, How, Project)", len(urls))
	}
	for _, m := range locTag.FindAllStringSubmatch(read(t, filepath.Join(out, "sitemap.xml")), -1) {
		urls = append(urls, m[1])
	}
	for _, u := range urls {
		if _, err := os.Stat(fileFor(out, u)); err != nil {
			t.Errorf("%s is listed but not built", u)
		}
	}
	full := read(t, filepath.Join(out, "llms-full.txt"))
	last := -1
	for _, p := range []string{"/", "/use/scan/", "/how/facts/", "/contributing/"} {
		i := strings.Index(full, "URL: "+origin+p+"\n")
		if i <= last {
			t.Errorf("llms-full.txt: %s missing or out of order", p)
		}
		last = i
	}
	if !strings.Contains(full, "| `--out` | Output file. |") {
		t.Error("llms-full.txt does not carry the pages' Markdown")
	}
}

var ldScript = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

func TestJSONLD(t *testing.T) {
	out := mustBuild(t)
	for _, c := range []struct{ file, want string }{
		{"use/scan/index.html", `"@type":"TechArticle"`},
		{"how/facts/index.html", `"headline":"Facts and evidence"`},
		{"index.html", `"@type":"SoftwareApplication"`},
		{"index.html", `"license":"https://www.apache.org/licenses/LICENSE-2.0"`},
		{"index.html", `"codeRepository":"https://github.com/cernly/cernly"`},
	} {
		h := read(t, filepath.Join(out, c.file))
		s := ldScript.FindAllStringSubmatch(h, -1)
		if len(s) != 1 {
			t.Errorf("%s: %d JSON-LD scripts, want 1", c.file, len(s))
			continue
		}
		if !strings.Contains(s[0][1], c.want) {
			t.Errorf("%s: JSON-LD lacks %s: %s", c.file, c.want, s[0][1])
		}
	}
	if s := ldScript.FindStringSubmatch(read(t, filepath.Join(out, "use/scan/index.html"))); s != nil &&
		!strings.Contains(s[1], `"url":"https://cernly.dev/use/scan/"`) {
		t.Errorf("TechArticle url: %s", s[1])
	}
}

// The docs point readers to the hosted product on every page.
func TestProductLink(t *testing.T) {
	out := mustBuild(t)
	for _, f := range []string{"index.html", "use/scan/index.html"} {
		if h := read(t, filepath.Join(out, f)); !strings.Contains(h, `<a href="https://cernly.eu">`) {
			t.Errorf("%s does not link cernly.eu", f)
		}
	}
}

// writePages puts files under a fresh pages folder, the fixture's home and 404
// included unless replaced.
func writePages(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	base := map[string]string{
		"index.md":     read(t, filepath.Join("testdata", "pages", "index.md")),
		"404.md":       read(t, filepath.Join("testdata", "pages", "404.md")),
		"use/scan.md":  read(t, filepath.Join("testdata", "pages", "use", "scan.md")),
		"how/facts.md": read(t, filepath.Join("testdata", "pages", "how", "facts.md")),
	}
	for k, v := range files {
		base[k] = v
	}
	for k, v := range base {
		name := filepath.Join(dir, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const evilPage = `---
title: Evil
description: Raw HTML.
path: /use/evil/
section: Use
order: 20
---
# Evil

**TL;DR** Nothing runs.

<script>alert(1)</script>

<iframe src="https://example.com/"></iframe>
`

func TestRawHTMLDropped(t *testing.T) {
	out, err := build(t, writePages(t, map[string]string{"use/evil.md": evilPage}))
	if err != nil {
		t.Fatal(err)
	}
	h := read(t, filepath.Join(out, "use", "evil", "index.html"))
	for _, bad := range []string{"<script>alert", "<iframe"} {
		if strings.Contains(h, bad) {
			t.Errorf("html carries %s", bad)
		}
	}
}

func TestBrokenLink(t *testing.T) {
	page := strings.Replace(evilPage, "Nothing runs.", "See [nope](/use/nope/) and [schema](/schema/scan.v1.schema.json).", 1)
	_, err := build(t, writePages(t, map[string]string{"use/evil.md": page}))
	if err == nil || !strings.Contains(err.Error(), "evil.md") || !strings.Contains(err.Error(), "/use/nope/") {
		t.Errorf("err = %v, want one naming evil.md and /use/nope/", err)
	}
	if err != nil && strings.Contains(err.Error(), "scan.v1.schema.json") {
		t.Errorf("schema link reported broken: %v", err)
	}
}

func TestHeaderErrors(t *testing.T) {
	head := "---\ntitle: T\ndescription: D.\npath: /use/x/\nsection: Use\n---\n# T\n"
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no title":    {map[string]string{"use/x.md": strings.Replace(head, "title: T\n", "", 1)}, "x.md"},
		"unknown key": {map[string]string{"use/x.md": strings.Replace(head, "section: Use\n", "section: Use\ncolour: red\n", 1)}, "colour"},
		"no slash":    {map[string]string{"use/x.md": strings.Replace(head, "/use/x/", "/use/x", 1)}, "x.md"},
		"bad section": {map[string]string{"use/x.md": strings.Replace(head, "section: Use", "section: Blog", 1)}, "Blog"},
		"no header":   {map[string]string{"use/x.md": "# T\n"}, "x.md"},
		"duplicate":   {map[string]string{"use/x.md": head, "use/y.md": head}, "/use/x/"},
	} {
		_, err := build(t, writePages(t, c.files))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
}

func TestReadPagesCommands(t *testing.T) {
	ps, err := ReadPages(filepath.Join("testdata", "pages"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Path == "/use/scan/" {
			if len(p.Commands) != 1 || p.Commands[0] != "scan" || !bytes.HasPrefix(p.Markdown, []byte("# cernly scan")) {
				t.Errorf("scan page: commands %q, markdown %q", p.Commands, p.Markdown[:12])
			}
			return
		}
	}
	t.Error("scan page not read")
}

// A section without pages is left out of llms.txt rather than listed empty.
func TestLLMSSkipsEmptySection(t *testing.T) {
	dir := writePages(t, nil)
	out, err := build(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if llms := read(t, filepath.Join(out, "llms.txt")); strings.Contains(llms, "## Project") {
		t.Errorf("llms.txt lists the empty Project section:\n%s", llms)
	}
}

// Build empties its output folder, so it refuses one that holds the sources.
func TestBuildRefusesSourceAsOut(t *testing.T) {
	src := t.TempDir()
	pages := writePages(t, nil)
	for _, out := range []string{src, pages, filepath.Dir(pages)} {
		err := Build(Options{Src: src, Pages: pages, Schema: filepath.Join("..", "schema"), Out: out})
		if err == nil || !strings.Contains(err.Error(), "output folder") {
			t.Errorf("out %s: err = %v, want a refusal", out, err)
		}
		if _, err := os.Stat(filepath.Join(pages, "index.md")); err != nil {
			t.Fatalf("pages deleted: %v", err)
		}
	}
}

// A page saved with Windows line endings reads like any other.
func TestCRLFPage(t *testing.T) {
	page := strings.ReplaceAll(read(t, filepath.Join("testdata", "pages", "how", "facts.md")), "\n", "\r\n")
	out, err := build(t, writePages(t, map[string]string{"how/facts.md": page}))
	if err != nil {
		t.Fatal(err)
	}
	if h := read(t, filepath.Join(out, "how", "facts", "index.html")); !strings.Contains(h, "<h1 id=\"facts-and-evidence\">Facts and evidence</h1>") {
		t.Errorf("CRLF page not rendered:\n%s", h)
	}
}

// Brackets and parentheses in a title or description do not break llms.txt links.
func TestLLMSEscapes(t *testing.T) {
	page := strings.Replace(read(t, filepath.Join("testdata", "pages", "how", "facts.md")), "title: Facts and evidence", "title: Facts [v1] (core)", 1)
	out, err := build(t, writePages(t, map[string]string{"how/facts.md": page}))
	if err != nil {
		t.Fatal(err)
	}
	if llms := read(t, filepath.Join(out, "llms.txt")); !strings.Contains(llms, `- [Facts \[v1\] (core)](https://cernly.dev/how/facts/index.md)`) {
		t.Errorf("llms.txt:\n%s", llms)
	}
}

// The agent notes in agents.md open llms.txt, before the page lists, and
// llms-full.txt.
func TestLLMSAgentNotes(t *testing.T) {
	out := mustBuild(t)
	notes := strings.TrimSpace(read(t, "agents.md"))
	llms := read(t, filepath.Join(out, "llms.txt"))
	i := strings.Index(llms, notes)
	if i < 0 || i > strings.Index(llms, "\n## Use\n") {
		t.Errorf("llms.txt does not open with agents.md:\n%s", llms)
	}
	if !strings.Contains(read(t, filepath.Join(out, "llms-full.txt")), notes) {
		t.Error("llms-full.txt lacks agents.md")
	}
}

// The agent skill is served beside the pages, byte for byte.
func TestSkillsServed(t *testing.T) {
	out := mustBuild(t)
	want := read(t, filepath.Join("..", "skills", "cernly", "SKILL.md"))
	if got := read(t, filepath.Join(out, "skills", "cernly", "SKILL.md")); got != want {
		t.Error("skills/cernly/SKILL.md differs from skills")
	}
}

// A source tree without a skills folder still builds, with no /skills/.
func TestSkillsFolderMissing(t *testing.T) {
	out := t.TempDir()
	err := Build(Options{Src: ".", Pages: filepath.Join("testdata", "pages"), Schema: filepath.Join("..", "schema"),
		Skills: filepath.Join(t.TempDir(), "skills"), Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "skills")); !os.IsNotExist(err) {
		t.Errorf("skills folder written: %v", err)
	}
}
