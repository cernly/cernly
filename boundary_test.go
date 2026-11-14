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
