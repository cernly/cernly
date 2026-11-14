package site

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strings"
)

const summary = "Open-source CLI and MCP server that reads a git repository and reports its architecture as facts with file and line evidence: services, data stores, third-party providers, environment variables, outside hosts, routes, database tables, dependencies and AI tooling. Deterministic, read-only, Apache 2.0."

// linkText escapes what would end a Markdown link's text early.
var linkText = strings.NewReplacer("[", `\[`, "]", `\]`)

func listed(p Page) bool { return p.Path != "/404.html" }

// sitemap lists every page but 404.
func sitemap(pages []Page) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range pages {
		if listed(p) {
			fmt.Fprintf(&b, "  <url><loc>%s</loc></url>\n", html.EscapeString(origin+p.Path))
		}
	}
	b.WriteString("</urlset>\n")
	return []byte(b.String())
}

// llms follows the llms.txt proposal (llmstxt.org): a title, a summary, the
// agent notes, then sections of links to each page's Markdown, one line each.
func llms(pages []Page, notes []byte) []byte {
	var b strings.Builder
	b.WriteString("# Cernly\n\n> " + summary + "\n\n")
	b.WriteString("Source: https://github.com/cernly/cernly\n")
	b.WriteString("All pages in one file: " + origin + "/llms-full.txt\n")
	writeNotes(&b, notes)
	for _, s := range Sections {
		var lines []string
		for _, p := range pages {
			if p.Section == s {
				lines = append(lines, fmt.Sprintf("- [%s](%s): %s\n", linkText.Replace(p.Title), origin+twinPath(p), p.Description))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n## %s\n\n%s", s, strings.Join(lines, ""))
		}
	}
	return []byte(b.String())
}

// writeNotes adds the agent notes (agents.md) as free text, which the
// llms.txt proposal allows between the summary and the link sections.
func writeNotes(b *strings.Builder, notes []byte) {
	if n := strings.TrimSpace(string(notes)); n != "" {
		b.WriteString("\n" + n + "\n")
	}
}

// llmsFull holds the agent notes, then every listed page's Markdown in order.
func llmsFull(pages []Page, notes []byte) []byte {
	var b strings.Builder
	b.WriteString("# Cernly: full documentation\n\n> " + summary + "\n")
	writeNotes(&b, notes)
	for _, p := range pages {
		if listed(p) {
			fmt.Fprintf(&b, "\n---\n\nURL: %s\n\n%s", origin+p.Path, strings.TrimRight(string(p.Markdown), "\n")+"\n")
		}
	}
	return []byte(b.String())
}

// jsonLD describes the home page as the software, every other page as an
// article about it.
func jsonLD(p Page) (template.JS, error) {
	app := map[string]any{"@type": "SoftwareApplication", "name": "Cernly", "url": origin + "/"}
	var v map[string]any
	if p.Path == "/" {
		v = app
		v["description"] = p.Description
		v["applicationCategory"] = "DeveloperApplication"
		v["operatingSystem"] = "Linux, macOS, Windows"
		v["license"] = "https://www.apache.org/licenses/LICENSE-2.0"
		v["codeRepository"] = "https://github.com/cernly/cernly"
		v["downloadUrl"] = "https://github.com/cernly/cernly/releases"
	} else {
		v = map[string]any{"@type": "TechArticle", "headline": p.Title, "description": p.Description,
			"url": origin + p.Path, "about": app}
	}
	v["@context"] = "https://schema.org"
	b, err := json.Marshal(v)
	return template.JS(b), err
}

// twinPath is where a page's raw Markdown is served.
func twinPath(p Page) string { return p.Path + "index.md" }
