// Package repotest builds repo views for tests.
package repotest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cernly/cernly/repo"
)

// Files writes files (slash paths to content) under a temporary folder and
// opens it as a repo.
func Files(t *testing.T, files map[string]string) *repo.Repo {
	t.Helper()
	return Write(t, t.TempDir(), files)
}

// Write is Files under root. A path that leaves root fails the test.
func Write(t *testing.T, root string, files map[string]string) *repo.Repo {
	t.Helper()
	for p, content := range files {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			t.Fatalf("repotest: %q is not a path inside the repo", p)
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
