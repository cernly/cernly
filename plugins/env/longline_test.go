package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo"
)

// A dotenv line over the scanner buffer is an error, not a silent end of the
// file; the names before it still count.
func TestDotenvLongLine(t *testing.T) {
	root := t.TempDir()
	body := "FIRST=1\nLONG=" + strings.Repeat("x", 2<<20) + "\nLAST=1\n"
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	if len(res.Errors) != 1 || res.Errors[0].Path != ".env.example" || res.Errors[0].Line != 2 {
		t.Fatalf("errors = %v", res.Errors)
	}
	var names []string
	for _, f := range res.Facts {
		names = append(names, f.Attrs["name"].(string))
	}
	if len(names) != 1 || names[0] != "FIRST" {
		t.Errorf("names = %v", names)
	}
}
