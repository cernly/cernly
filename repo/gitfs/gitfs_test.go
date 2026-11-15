package gitfs_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cernly/cernly/repo/gitfs"
	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

// noAt hides io.ReaderAt (and ReadLinkFS) of the files an FS opens, as a
// browser FS may.
type noAt struct{ fs.FS }

type plainFile struct{ fs.File }

func (n noAt) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if _, ok := f.(fs.ReadDirFile); ok {
		return f, nil // a folder: only files lose ReadAt
	}
	return plainFile{f}, nil
}

func TestMatchesGit(t *testing.T) {
	run := func(format string, cases []gitfstest.Case) {
		for _, c := range cases {
			if c.Name == "no-git" {
				continue
			}
			for _, wrap := range []bool{false, true} {
				if wrap && c.Name != "packed" && c.Name != "split-index" && c.Name != "sha256-packed" {
					continue
				}
				name := c.Name
				if wrap {
					name += "/no-readerat"
				}
				t.Run(name, func(t *testing.T) {
					g := gitfstest.New(t, format)
					c.Setup(g)
					var fsys fs.FS = os.DirFS(g.Dir)
					if wrap {
						fsys = noAt{fsys}
					}
					r, err := gitfs.Open(fsys)
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					compare(t, g, r, !strings.HasPrefix(c.Name, "clean-filter")) // plain git status runs the filter; repo's Dirty switches it off, see repo/fs_test.go
				})
			}
		}
	}
	run("sha1", gitfstest.Cases)
	run("sha256", gitfstest.SHA256Cases)
}

func compare(t *testing.T, g *gitfstest.Repo, r *gitfs.Repo, dirty bool) {
	t.Helper()
	if want := g.Git("rev-parse", "--show-object-format"); strings.TrimSpace(want) != r.ObjectFormat() {
		t.Errorf("ObjectFormat = %q, git says %q", r.ObjectFormat(), want)
	}
	// Index: every entry, every stage.
	out, _ := g.TryGit("ls-files", "--stage", "-z")
	var want []string
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			want = append(want, e)
		}
	}
	ix, err := r.Index()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ix.Entries {
		got = append(got, fmt.Sprintf("%06o %s %d\t%s", e.Mode, e.ID, e.Stage, e.Path))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("index\n got %q\nwant %q", got, want)
	}
	// Origin.
	url, err := g.TryGit("remote", "get-url", "origin")
	if err != nil {
		url = ""
	}
	if got := r.OriginURL(); got != strings.TrimSpace(url) {
		t.Errorf("OriginURL = %q, git says %q", got, strings.TrimSpace(url))
	}
	// HEAD, commit, tree.
	headOut, err := g.TryGit("rev-parse", "--verify", "-q", "HEAD")
	head, herr := r.Head()
	if err != nil {
		if herr == nil {
			t.Errorf("Head = %s on an unborn branch", head)
		}
		return
	}
	if herr != nil || head != strings.TrimSpace(headOut) {
		t.Fatalf("Head = %q, %v; git says %q", head, herr, headOut)
	}
	c, err := r.Commit(head)
	if err != nil {
		t.Fatal(err)
	}
	iso := strings.TrimSpace(g.Git("log", "-1", "--format=%cI", "HEAD"))
	tm, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	if c.CommittedAt != tm.UTC().Format(time.RFC3339) {
		t.Errorf("CommittedAt = %q, git says %q", c.CommittedAt, iso)
	}
	if tree := strings.TrimSpace(g.Git("rev-parse", "HEAD^{tree}")); c.Tree != tree {
		t.Errorf("Tree = %q, git says %q", c.Tree, tree)
	}
	m, err := r.Tree(c.Tree)
	if err != nil {
		t.Fatal(err)
	}
	var gotTree []string
	for p, e := range m {
		gotTree = append(gotTree, fmt.Sprintf("%06o %s\t%s", e.Mode, e.ID, p))
	}
	sort.Strings(gotTree)
	var wantTree []string
	for _, l := range strings.Split(strings.TrimRight(g.Git("ls-tree", "-r", "-z", "HEAD"), "\x00"), "\x00") {
		if l == "" {
			continue
		}
		f := strings.Fields(l[:strings.IndexByte(l, '\t')])
		wantTree = append(wantTree, fmt.Sprintf("%s %s\t%s", f[0], f[2], l[strings.IndexByte(l, '\t')+1:]))
	}
	sort.Strings(wantTree)
	if strings.Join(gotTree, "\n") != strings.Join(wantTree, "\n") {
		t.Errorf("tree\n got %q\nwant %q", gotTree, wantTree)
	}
	if !dirty {
		return
	}
	d, err := r.Dirty(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := g.TryGit("status", "--porcelain", "-z", "--untracked-files=no", "--ignore-submodules=all", "--", ".")
	if d != (st != "") {
		t.Errorf("Dirty = %v, git status says %q", d, st)
	}
}

func TestIndexVersions(t *testing.T) {
	for name, want := range map[string]int{"loose": 2, "index-v3-skip-worktree": 3, "index-v4": 4, "split-index": 2} {
		for _, c := range gitfstest.Cases {
			if c.Name != name {
				continue
			}
			g := gitfstest.New(t, "")
			c.Setup(g)
			r, err := gitfs.Open(os.DirFS(g.Dir))
			if err != nil {
				t.Fatal(err)
			}
			ix, err := r.Index()
			if err != nil {
				t.Fatal(err)
			}
			if ix.Version != want {
				t.Errorf("%s: index version %d, want %d", name, ix.Version, want)
			}
		}
	}
}

func TestOpenErrors(t *testing.T) {
	g := func(t *testing.T) *gitfstest.Repo {
		g := gitfstest.New(t, "")
		g.Write("a.txt", "a\n")
		g.Commit("a")
		return g
	}
	appendTo := func(t *testing.T, g *gitfstest.Repo, rel, s string) {
		f, err := os.OpenFile(filepath.Join(g.Dir, rel), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		f.WriteString(s)
	}
	for name, c := range map[string]struct {
		setup func(t *testing.T, g *gitfstest.Repo)
		want  string
	}{
		"git file": {func(t *testing.T, g *gitfstest.Repo) {
			os.RemoveAll(filepath.Join(g.Dir, ".git"))
			os.WriteFile(filepath.Join(g.Dir, ".git"), []byte("gitdir: ../main/.git/worktrees/x\n"), 0o644)
		}, ".git is a file"},
		"no git":  {func(t *testing.T, g *gitfstest.Repo) { os.RemoveAll(filepath.Join(g.Dir, ".git")) }, "no .git"},
		"no HEAD": {func(t *testing.T, g *gitfstest.Repo) { os.Remove(filepath.Join(g.Dir, ".git/HEAD")) }, "no .git/HEAD"},
		"reftable": {func(t *testing.T, g *gitfstest.Repo) {
			appendTo(t, g, ".git/config", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefStorage = reftable\n")
		}, "reftable"},
		"alternates": {func(t *testing.T, g *gitfstest.Repo) {
			appendTo(t, g, ".git/objects/info/alternates", "/elsewhere/objects\n")
		}, "alternates"},
		"future format": {func(t *testing.T, g *gitfstest.Repo) {
			g.Git("config", "core.repositoryformatversion", "2")
		}, "version 2"},
		"unknown object format": {func(t *testing.T, g *gitfstest.Repo) {
			g.Git("config", "core.repositoryformatversion", "1")
			g.Git("config", "extensions.objectformat", "blake3")
		}, "object format"},
		"bare": {func(t *testing.T, g *gitfstest.Repo) { g.Git("config", "core.bare", "true") }, "bare"},
	} {
		t.Run(name, func(t *testing.T) {
			g := g(t)
			c.setup(t, g)
			_, err := gitfs.Open(os.DirFS(g.Dir))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Open error = %v, want one with %q", err, c.want)
			}
		})
	}
}

func TestSparseIndex(t *testing.T) {
	g := gitfstest.New(t, "")
	for _, d := range []string{"a", "b", "c"} {
		g.Write(d+"/x.txt", d)
		g.Write(d+"/y/z.txt", d)
	}
	g.Commit("dirs")
	if _, err := g.TryGit("sparse-checkout", "init", "--cone", "--sparse-index"); err != nil {
		t.Skip("git has no sparse index")
	}
	g.Git("sparse-checkout", "set", "a")
	ix, _ := os.ReadFile(filepath.Join(g.Dir, ".git/index"))
	if !bytes.Contains(ix, []byte("sdir")) {
		t.Skip("git wrote no sparse index")
	}
	r, err := gitfs.Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Index(); err == nil || !strings.Contains(err.Error(), "sparse index not supported") {
		t.Errorf("Index error = %v", err)
	}
}

func TestUnbornHead(t *testing.T) {
	g := gitfstest.New(t, "")
	r, err := gitfs.Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Head(); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("Head error = %v", err)
	}
}
