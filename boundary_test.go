// Package core_test guards the open core's boundary: it imports only the
// standard library, its own packages and the third-party modules listed
// below. A new dependency is added here on purpose, in review; a database
// driver or a package from a module that embeds this one fails. It runs
// both where this code is a folder of a larger module and as the module
// root of the open repo. See https://cernly.dev/how/open-core/.
package core_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// allowed are the third-party module paths the open core may import.
var allowed = []string{
	"github.com/BurntSushi/toml",
	"github.com/evanw/esbuild",
	"github.com/moby/buildkit",
	"github.com/modelcontextprotocol/go-sdk",
	"github.com/santhosh-tekuri/jsonschema/v6",
	"github.com/tdewolff/parse/v2",
	"github.com/tetratelabs/wazero",
	"github.com/yuin/goldmark",
	"golang.org/x/mod",
	"gopkg.in/yaml.v3",
	"mvdan.cc/sh/v3",
}

// forbidden are named on their own so the failure says why.
var forbidden = map[string]string{
	"github.com/jackc/":          "a database driver; the scanner needs none",
	"architecture-observability": "the packages of a module that embeds this one",
	"github.com/cernly/":         "a Cernly module other than this one",
}

// ownPrefix is the import path of the walked directory: the module itself
// when go.mod sits here, <module>/core when it sits one level up.
func ownPrefix(t *testing.T) string {
	if data, err := os.ReadFile("go.mod"); err == nil {
		return modfile.ModulePath(data)
	}
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal("no go.mod here or one level up")
	}
	wd, _ := os.Getwd()
	return modfile.ModulePath(data) + "/" + filepath.Base(wd)
}

func TestBoundary(t *testing.T) {
	self := ownPrefix(t)
	if self == "" || strings.HasPrefix(self, "/") {
		t.Fatal("go.mod names no module")
	}

	var files, ownImports int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") && path != ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == self || strings.HasPrefix(p, self+"/") {
				ownImports++
				continue
			}
			if why := forbiddenReason(p); why != "" {
				t.Errorf("%s imports %s: %s", path, p, why)
				continue
			}
			if !strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
				continue // standard library
			}
			if !isAllowed(p) {
				t.Errorf("%s imports %s: not an allowed dependency of the open core (add it to boundary_test.go on purpose)", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The walk must have seen the code, or the test passes on nothing.
	if files < 20 || ownImports == 0 {
		t.Fatalf("boundary walk saw %d files and %d imports of %s: the test is not checking the module", files, ownImports, self)
	}
}

func forbiddenReason(p string) string {
	for prefix, why := range forbidden {
		if strings.HasPrefix(p, prefix) {
			return why
		}
	}
	return ""
}

func isAllowed(p string) bool {
	for _, m := range allowed {
		if p == m || strings.HasPrefix(p, m+"/") {
			return true
		}
	}
	return false
}

// The web packages under core/web hold to the same boundary: relative imports
// stay inside core/, bare ones name only React, the open packages and node.
// Import and export declarations and import() count, read by webImports: a
// "from" or "import" in a string, a comment or the page's prose is not one.

// webImportProblem says why the import spec in the file at rel (a slash path
// from core/, like web/views/src/a.ts) is not allowed, or "" when it is.
func webImportProblem(rel, spec string) string {
	if strings.HasPrefix(spec, ".") {
		resolved := path.Join(path.Dir(rel), spec)
		if resolved == ".." || strings.HasPrefix(resolved, "../") {
			return "a relative import that leaves core/"
		}
		return ""
	}
	if strings.HasPrefix(spec, "node:") {
		return ""
	}
	for _, ok := range []string{"react", "react-dom", "react/jsx-runtime", "react-dom/client", "vite", "@cernly/views", "@cernly/brand"} {
		if spec == ok || strings.HasPrefix(spec, ok+"/") && strings.HasPrefix(ok, "@") {
			return ""
		}
	}
	return "a package the open core does not allow (react, react-dom, vite, @cernly/views, @cernly/brand, node:*)"
}

func TestWebImportsStayInCore(t *testing.T) {
	var files int
	err := filepath.WalkDir("web", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") && !strings.HasSuffix(p, ".mjs") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for _, spec := range webImports(string(data)) {
			if why := webImportProblem(filepath.ToSlash(p), spec); why != "" {
				t.Errorf("%s imports %q: %s", p, spec, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 8 {
		t.Fatalf("web walk saw %d files: the test is not checking core/web", files)
	}
}

func TestWebImportProblem(t *testing.T) {
	for _, c := range []struct {
		file, spec string
		bad        bool
	}{
		// From core/web/views/src, ../../../../ is the repo root: the app, outside core/.
		{"web/views/src/a.ts", "../../../../web/app/lib/api", true},
		{"web/views/src/a.ts", "../../../../../x", true},
		{"web/views/src/a.ts", "./types", false},
		{"web/views/src/a.ts", "../../brand/mark", false},
		{"web/views/src/a.ts", "react", false},
		{"web/views/src/a.ts", "react/jsx-runtime", false},
		{"web/views/src/a.ts", "react-dom", false},
		{"web/views/src/a.ts", "@cernly/views/types", false},
		{"web/views/src/a.ts", "@cernly/brand/mark", false},
		{"web/views/src/a.ts", "node:test", false},
		{"web/views/src/a.ts", "react-router", true},
		{"web/views/src/a.ts", "@cernly/design/mark", true},
		{"web/views/src/a.ts", "reactive", true},
		{"web/views/src/a.ts", "lodash", true},
	} {
		if got := webImportProblem(c.file, c.spec) != ""; got != c.bad {
			t.Errorf("%s importing %q: rejected %v, want %v", c.file, c.spec, got, c.bad)
		}
	}
}

func TestWebImportPattern(t *testing.T) {
	src := "import a from \"x1\";\nimport \"./a.css\";\nexport { b } from 'x2';\nimport {\n  c,\n} from \"x3\";\nconst d = await import(\"x4\");\n" +
		"const e = plural(n, \"import\") + \"from\";\nArray.from(\"no\");\n<span>{plural(k, \"import\")} · from \"no\"</span>\n" +
		"export { f } from \"x5\"; export { g } from \"x6\"; import { \"n-1\" as h } from \"x7\";\n" +
		"import { plural } from \"./format\"; import { api } from \"../../../../web/app/lib/api\"\n" +
		"import x // ;\nfrom \"x8\"\n" +
		"/* import y from \"no\" */ // import z from \"no\"\nconst r = s.replace(/[\"']\\//g, \"\"); const t = `import q from \"no\" ${a}`;\n" +
		"export * as ns from \"x9\";\nexport const k = 1;\nexport type { T } from \"x10\";\nimport type { U } from \"x11\";\n"
	got := strings.Join(webImports(src), ",")
	want := "x1,./a.css,x2,x3,x4,x5,x6,x7,./format,../../../../web/app/lib/api,x8,x9,x10,x11"
	if got != want {
		t.Errorf("imports %v, want %v", got, want)
	}
}
