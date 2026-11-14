// Package site builds the developer docs at cernly.dev from Markdown pages:
// HTML in the brand's layout, each page's Markdown beside it, llms.txt,
// llms-full.txt, sitemap.xml, JSON-LD and the JSON Schemas. Same sources, same
// bytes: no dates or build ids in the output.
package site

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

const origin = "https://cernly.dev"

// Options name the folders a build reads and writes.
type Options struct {
	// Src holds layout.html, static/ and the optional agents.md, the notes
	// for AI agents that open llms.txt.
	Src string
	// Pages holds the Markdown pages; default Src/pages.
	Pages string
	// Schema holds the JSON Schemas, served at /schema/.
	Schema string
	// Skills holds agent skills, served at /skills/; skipped when empty or
	// missing.
	Skills string
	// Out is emptied and written.
	Out string
}

type navLink struct {
	Title, Path string
	Current     bool
}

type navSection struct {
	Name  string
	Pages []navLink
}

type view struct {
	Title, Description, Canonical, MarkdownURL string
	Home                                       bool
	Body                                       template.HTML
	JSONLD                                     template.JS
	Nav                                        []navSection
	Project                                    []navLink
}

// md renders without raw HTML: a <script> in a page never reaches the site.
var md = goldmark.New(
	goldmark.WithExtensions(extension.Table),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// Build writes the site into opt.Out.
func Build(opt Options) error {
	if opt.Pages == "" {
		opt.Pages = filepath.Join(opt.Src, "pages")
	}
	if err := checkOut(opt); err != nil {
		return err
	}
	layout, err := template.ParseFiles(filepath.Join(opt.Src, "layout.html"))
	if err != nil {
		return err
	}
	pages, err := ReadPages(opt.Pages)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(opt.Out); err != nil {
		return err
	}
	bodies := map[string]string{}
	for _, p := range pages {
		var body bytes.Buffer
		if err := md.Convert(p.Markdown, &body); err != nil {
			return fmt.Errorf("%s: %w", p.File, err)
		}
		bodies[p.File] = body.String()
		ld, err := jsonLD(p)
		if err != nil {
			return err
		}
		v := view{Title: p.Title, Description: p.Description, Canonical: origin + p.Path,
			Home: p.Path == "/", Body: template.HTML(body.String()), JSONLD: ld, Nav: nav(pages, p.Path)}
		for _, q := range pages {
			if q.Section == "Project" {
				v.Project = append(v.Project, navLink{Title: q.Title, Path: q.Path})
			}
		}
		if listed(p) {
			v.MarkdownURL = twinPath(p)
		}
		var buf bytes.Buffer
		if err := layout.Execute(&buf, v); err != nil {
			return fmt.Errorf("%s: %w", p.File, err)
		}
		name := p.Path
		if strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		if err := write(filepath.Join(opt.Out, filepath.FromSlash(name)), buf.Bytes()); err != nil {
			return err
		}
		if listed(p) {
			if err := write(filepath.Join(opt.Out, filepath.FromSlash(twinPath(p))), p.Markdown); err != nil {
				return err
			}
		}
	}
	if err := copyDir(filepath.Join(opt.Src, "static"), opt.Out); err != nil {
		return err
	}
	if err := copyDir(opt.Schema, filepath.Join(opt.Out, "schema"), ".json"); err != nil {
		return err
	}
	if _, err := os.Stat(opt.Skills); opt.Skills != "" && err == nil {
		if err := copyDir(opt.Skills, filepath.Join(opt.Out, "skills")); err != nil {
			return err
		}
	}
	notes, err := os.ReadFile(filepath.Join(opt.Src, "agents.md"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for name, data := range map[string][]byte{"sitemap.xml": sitemap(pages), "llms.txt": llms(pages, notes), "llms-full.txt": llmsFull(pages, notes)} {
		if err := write(filepath.Join(opt.Out, name), data); err != nil {
			return err
		}
	}
	return checkLinks(opt.Out, pages, bodies)
}

// checkOut refuses an output folder that is, or holds, the sources: Build
// empties it first.
func checkOut(opt Options) error {
	out, err := filepath.Abs(opt.Out)
	if err != nil {
		return err
	}
	for _, src := range []string{opt.Src, opt.Pages, opt.Schema, opt.Skills} {
		if src == "" {
			continue
		}
		abs, err := filepath.Abs(src)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(out, abs); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
			return fmt.Errorf("output folder %s holds the sources (%s): Build empties it", opt.Out, src)
		}
	}
	return nil
}

func nav(pages []Page, current string) []navSection {
	var out []navSection
	for _, s := range Sections[:2] {
		sec := navSection{Name: s}
		for _, p := range pages {
			if p.Section == s {
				sec.Pages = append(sec.Pages, navLink{Title: p.Title, Path: p.Path, Current: p.Path == current})
			}
		}
		out = append(out, sec)
	}
	return out
}

var href = regexp.MustCompile(`href="(/[^"#?]*)`)

// checkLinks fails on a link in a page body to a path the build did not write.
func checkLinks(out string, pages []Page, bodies map[string]string) error {
	var broken []string
	for _, p := range pages {
		for _, m := range href.FindAllStringSubmatch(bodies[p.File], -1) {
			link := m[1]
			if strings.HasPrefix(link, "//") {
				continue
			}
			name := link
			if strings.HasSuffix(name, "/") {
				name += "index.html"
			}
			if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(name))); err != nil {
				broken = append(broken, fmt.Sprintf("%s links %s", p.File, link))
			}
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("broken links:\n  %s", strings.Join(broken, "\n  "))
	}
	return nil
}

// copyDir copies the files under from into to; with exts, only those
// extensions.
func copyDir(from, to string, exts ...string) error {
	return filepath.WalkDir(from, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if len(exts) > 0 && !slices.Contains(exts, filepath.Ext(name)) {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, name)
		return write(filepath.Join(to, rel), data)
	})
}

func write(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o644)
}
