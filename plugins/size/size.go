// Package size counts source files and lines per folder, as context for the
// map: one source_size fact per folder and language, and one more for its test
// files (by the language's convention, internal/testfile), with every counted
// file as evidence (line 1 to its last line). Physical lines: a final line
// without a newline counts. The language comes from the file extension alone;
// files are never parsed here. The rules sum the folders inside a service's
// sources. See https://cernly.dev/how/map/.
package size

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/testfile"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "size" }
func (Plugin) Version() string { return "0.1.1" }

// languages maps a file extension to the language it counts as. Markup,
// styles, data and configuration are not counted.
var languages = map[string]string{
	".go":     "go",
	".ts":     "typescript",
	".tsx":    "typescript",
	".mts":    "typescript",
	".cts":    "typescript",
	".js":     "javascript",
	".jsx":    "javascript",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".vue":    "vue",
	".svelte": "svelte",
	".py":     "python",
	".java":   "java",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".scala":  "scala",
	".rb":     "ruby",
	".rs":     "rust",
	".php":    "php",
	".cs":     "csharp",
	".swift":  "swift",
	".c":      "c",
	".h":      "c",
	".cc":     "cpp",
	".cpp":    "cpp",
	".hpp":    "cpp",
	".sql":    "sql",
	".sh":     "shell",
	".bash":   "shell",
}

// Language returns the language a path counts as, "" when it is not counted.
// Minified bundles are not source.
func Language(p string) string {
	b := path.Base(p)
	if strings.Contains(b, ".min.") {
		return ""
	}
	return languages[strings.ToLower(path.Ext(b))]
}

func (Plugin) Watches(p string) bool { return Language(p) != "" }

// Lines counts physical lines.
func Lines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	type group struct {
		dir, lang    string
		test         bool
		files, lines int
		ev           []fact.Evidence
	}
	groups := map[string]*group{}
	for _, f := range r.Files {
		lang := Language(f)
		if lang == "" {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		d := path.Dir(f)
		test := testfile.Is(f)
		k := d + ":" + lang
		if test {
			k += ":test"
		}
		g := groups[k]
		if g == nil {
			g = &group{dir: d, lang: lang, test: test}
			groups[k] = g
		}
		n := Lines(data)
		g.files++
		g.lines += n
		ev := fact.Evidence{Path: f, Line: 1}
		if n > 1 {
			ev.EndLine = n
		}
		g.ev = append(g.ev, ev)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		attrs := map[string]any{"dir": g.dir, "language": g.lang, "files": g.files, "lines": g.lines}
		if g.test {
			attrs["test"] = true
		}
		res.Facts = append(res.Facts, fact.Fact{Kind: fact.KindSourceSize, Key: "source_size:" + k,
			Attrs:  attrs,
			Plugin: "size", Evidence: g.ev})
	}
	return res
}
