package repo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

// OpenFS must give what Open gives from git: the same files and the same
// answer to Dirty, in every state the catalogue builds.
func TestOpenFSMatchesGit(t *testing.T) {
	run := func(format string, cases []gitfstest.Case) {
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				g := gitfstest.New(t, format)
				c.Setup(g)
				want, err := Open(g.Dir)
				if err != nil {
					t.Fatal(err)
				}
				got, err := OpenFS(os.DirFS(g.Dir), filepath.Base(g.Dir))
				if err != nil {
					t.Fatal(err)
				}
				defer got.Close()
				if !slices.Equal(got.Files, want.Files) {
					t.Errorf("files\n got %q\nwant %q", got.Files, want.Files)
				}
				wd, err := want.Dirty()
				if err != nil {
					t.Fatal(err)
				}
				gd, err := got.Dirty()
				if err != nil {
					t.Fatal(err)
				}
				if gd != wd {
					t.Errorf("Dirty = %v, git says %v", gd, wd)
				}
				for _, f := range want.Files {
					a, aerr := want.Read(f)
					b, berr := got.Read(f)
					if (aerr == nil) != (berr == nil) || string(a) != string(b) {
						t.Errorf("Read(%q): %q, %v; git side %q, %v", f, b, berr, a, aerr)
					}
				}
			})
		}
	}
	run("sha1", gitfstest.Cases)
	run("sha256", gitfstest.SHA256Cases)
}

func TestReadFS(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/b.txt", "\xEF\xBB\xBFhello")
	write("big.txt", "0123456789")
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "x"), []byte("x"), 0o644)
	if os.Symlink(outside, filepath.Join(dir, "linkdir")) != nil {
		t.Skip("no symlinks")
	}
	os.Symlink(filepath.Join(dir, "a/b.txt"), filepath.Join(dir, "link.txt"))
	r, err := OpenFS(os.DirFS(dir), "acme")
	if err != nil {
		t.Fatal(err)
	}
	r.max = 8
	if r.Root != "acme" {
		t.Errorf("Root = %q", r.Root)
	}
	if got, err := r.Read("a/b.txt"); err != nil || string(got) != "hello" {
		t.Errorf("Read a/b.txt = %q, %v", got, err)
	}
	for _, rel := range []string{"big.txt", "link.txt", "linkdir/x", "../x", "a/../a/b.txt", "", "a", "missing"} {
		if got, err := r.Read(rel); err == nil {
			t.Errorf("Read(%q) = %q, want an error", rel, got)
		}
	}
	if r.Has("link.txt") || r.Has("linkdir/x") {
		t.Error("symlinks are listed")
	}
}
