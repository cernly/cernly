package site

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Page is one Markdown file under the pages folder.
type Page struct {
	Title, Description, Path, Section string
	Order                             int
	// Commands are the CLI commands the page documents, for the flag check.
	Commands []string
	// Markdown is the page without its front matter.
	Markdown []byte
	// File is where the page was read from, for error messages.
	File string
}

// Sections, in the order llms.txt and the sidebar list them. Pages without a
// section are the home page and 404.
var Sections = []string{"Use", "How", "Project"}

// ReadPages reads every .md file under dir: home first, then by section,
// order and path, 404 last.
func ReadPages(dir string) ([]Page, error) {
	var pages []Page
	seen := map[string]string{}
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		p, err := parsePage(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p.File = name
		if other, dup := seen[p.Path]; dup {
			return fmt.Errorf("%s and %s share path %s", other, name, p.Path)
		}
		seen[p.Path] = name
		pages = append(pages, p)
		return nil
	})
	sort.Slice(pages, func(i, j int) bool {
		if ri, rj := rank(pages[i]), rank(pages[j]); ri != rj {
			return ri < rj
		}
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Path < pages[j].Path
	})
	return pages, err
}

func parsePage(data []byte) (Page, error) {
	var p Page
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return p, fmt.Errorf("no front matter: the file must start with ---")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return p, fmt.Errorf("front matter not closed with ---")
	}
	for _, line := range strings.Split(string(head), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return p, fmt.Errorf("front matter line %q", line)
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "title":
			p.Title = v
		case "description":
			p.Description = v
		case "path":
			p.Path = v
		case "section":
			p.Section = v
		case "order":
			n, err := strconv.Atoi(v)
			if err != nil {
				return p, fmt.Errorf("order %q is not a number", v)
			}
			p.Order = n
		case "commands":
			for _, c := range strings.Split(v, ",") {
				if c = strings.TrimSpace(c); c != "" {
					p.Commands = append(p.Commands, c)
				}
			}
		default:
			return p, fmt.Errorf("unknown front matter key %q", strings.TrimSpace(k))
		}
	}
	if p.Title == "" || p.Description == "" {
		return p, fmt.Errorf("title and description are required")
	}
	if p.Path != "/404.html" && !(strings.HasPrefix(p.Path, "/") && strings.HasSuffix(p.Path, "/")) {
		return p, fmt.Errorf("path %q must start and end with /", p.Path)
	}
	if p.Section != "" && !slices.Contains(Sections, p.Section) {
		return p, fmt.Errorf("section %q is not one of %s", p.Section, strings.Join(Sections, ", "))
	}
	p.Markdown = bytes.TrimLeft(body, "\n")
	return p, nil
}

// rank orders the home page first, then the sections in order, then 404.
func rank(p Page) int {
	if p.Path == "/404.html" {
		return len(Sections) + 1
	}
	for i, s := range Sections {
		if p.Section == s {
			return i + 1
		}
	}
	return 0
}
