package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A checkout scan without a commit reads the working tree but is labelled
// with HEAD: uncommitted changes to tracked files under the scanned folder,
// staged or not, mark it dirty. Untracked files, changes elsewhere and a
// given commit do not.
func TestDirty(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dirty := func(folder, sha string) bool {
		t.Helper()
		s, _, err := Scan(filepath.Join(dir, folder), ScanOptions{RepoID: "local/x", Commit: sha})
		if err != nil {
			t.Fatal(err)
		}
		return s.Commit.Dirty
	}
	git("init", "-q")
	write("sub/a.txt", "one\n")
	write("other/b.txt", "bee\n")
	git("add", "-A")
	git("commit", "-q", "-m", "first")

	if dirty(".", "") || dirty("sub", "") {
		t.Error("clean checkout dirty")
	}
	write("untracked.txt", "x")
	write("sub/untracked.txt", "x")
	if dirty(".", "") {
		t.Error("untracked file made it dirty")
	}
	write("other/b.txt", "changed\n")
	if !dirty(".", "") {
		t.Error("unstaged edit not dirty")
	}
	if dirty("sub", "") {
		t.Error("edit outside the scanned folder made it dirty")
	}
	write("sub/a.txt", "two\n")
	git("add", "sub/a.txt")
	if !dirty("sub", "") {
		t.Error("staged edit not dirty")
	}
	if dirty(".", "HEAD") {
		t.Error("a given commit is never dirty")
	}

	plain := t.TempDir() // outside git: the walk fallback
	if err := os.WriteFile(filepath.Join(plain, "a.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := Scan(plain, ScanOptions{RepoID: "local/x", Commit: strings.Repeat("a", 40), CommittedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Commit.Dirty {
		t.Error("folder outside git dirty")
	}
}

// A commit given for a folder inside a checkout is read from git, scoped to
// that folder: never the working tree under the commit's name. labelOnly
// (fixtures) keeps the files as they are.
func TestCommitInSubfolder(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for p, s := range map[string]string{"sub/a.txt": "committed\n", "other/b.txt": "b\n"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(dir, "sub/a.txt"), []byte("edited\n"), 0o644)

	read := func(labelOnly bool) ([]string, string) {
		t.Helper()
		r, _, err := openRepo(filepath.Join(dir, "sub"), "HEAD", labelOnly)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		b, err := r.Read("a.txt")
		if err != nil {
			t.Fatal(err)
		}
		return r.Files, string(b)
	}
	if files, got := read(false); got != "committed\n" || len(files) != 1 || files[0] != "a.txt" {
		t.Errorf("commit in a subfolder: files %v, a.txt %q", files, got)
	}
	if _, got := read(true); got != "edited\n" {
		t.Errorf("labelOnly read %q, want the working tree", got)
	}
}
