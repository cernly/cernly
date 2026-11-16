package repo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cernly/cernly/repo/gitfs"
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
				refused := false
				for _, f := range want.Files {
					a, aerr := want.Read(f)
					b, berr := got.Read(f)
					// OpenFS refuses a file whose bytes are not its stage-0
					// blob, which the CLI reads from the work tree: right
					// only when git agrees the bytes differ.
					var nc *gitfs.NotCommittedError
					if errors.As(berr, &nc) {
						staged, serr := g.TryGit("rev-parse", "--verify", "-q", ":0:"+f)
						raw, _ := g.TryGit("hash-object", "--no-filters", f)
						if serr == nil && strings.TrimSpace(staged) == strings.TrimSpace(raw) {
							t.Errorf("Read(%q) refused a file that is its committed blob", f)
						}
						refused = true
						continue
					}
					if (aerr == nil) != (berr == nil) || string(a) != string(b) {
						t.Errorf("Read(%q): %q, %v; git side %q, %v", f, b, berr, a, aerr)
					}
				}
				// Dirty is git's answer, except that a refused file always
				// makes the checkout dirty (skip-worktree and assume-unchanged
				// files git does not look at included).
				if gd != wd && !(gd && refused) || refused && !gd {
					t.Errorf("Dirty = %v, git says %v, a file refused: %v", gd, wd, refused)
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

// openOnly is a folder source that, like a browser's, shows no symbolic
// links: only Open, so neither Lstat nor ReadLink is there to ask.
type openOnly struct{ fs.FS }

// A tracked file whose bytes are another file's, as a symbolic link read
// through by the folder source gives them, is a change and is never read,
// even when its size and time are what the index records (where git itself
// would trust them and say clean).
func TestReadThroughIsRefused(t *testing.T) {
	g := gitfstest.New(t, "")
	g.Write("a.txt", "alpha\n")
	g.Write("b.txt", "bravo\n")
	g.Touch("a.txt", -time.Hour)
	g.Touch("b.txt", -time.Hour)
	g.Commit("c")
	a := filepath.Join(g.Dir, "a.txt")
	fi, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if st := g.Git("--no-optional-locks", "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Logf("git status: %q", st) // git trusts the stat data; the check must not
	}
	r, err := OpenFS(openOnly{os.DirFS(g.Dir)}, "shop")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if d, err := r.Dirty(); err != nil || !d {
		t.Errorf("Dirty = %v, %v; want true", d, err)
	}
	var nc *gitfs.NotCommittedError
	if _, err := r.Read("a.txt"); !errors.As(err, &nc) || nc.Path != "a.txt" {
		t.Errorf("Read(a.txt) error = %v, want a NotCommittedError", err)
	}
	if data, err := r.Read("b.txt"); err != nil || string(data) != "bravo\n" {
		t.Errorf("Read(b.txt) = %q, %v", data, err)
	}
}
