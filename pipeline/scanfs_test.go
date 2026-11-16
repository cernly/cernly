package pipeline

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

func canon(t *testing.T, s *fact.Scan) []byte {
	t.Helper()
	b, err := fact.Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// scanBoth scans dir with git and without, and wants the same bytes, or an
// error from both.
func scanBoth(t *testing.T, dir string, o ScanOptions) { scanBothHidden(t, dir, o, false) }

// scanBothHidden is scanBoth for a checkout with a changed file git does not
// look at (gitfstest.HiddenChange): ScanFS calls it dirty where Scan does not.
func scanBothHidden(t *testing.T, dir string, o ScanOptions, hidden bool) {
	t.Helper()
	o.Plugins = plugin.All()
	want, _, werr := Scan(dir, o)
	if werr == nil && hidden {
		want.Commit.Dirty = true
	}
	got, _, gerr := ScanFS(os.DirFS(dir), filepath.Base(dir), o)
	if (werr == nil) != (gerr == nil) {
		t.Fatalf("Scan error %v, ScanFS error %v", werr, gerr)
	}
	if werr != nil {
		return
	}
	if a, b := canon(t, got), canon(t, want); !bytes.Equal(a, b) {
		t.Errorf("ScanFS differs from Scan\n got %.600s\nwant %.600s", a, b)
	}
}

func TestScanFSMatchesScan(t *testing.T) {
	run := func(format string, cases []gitfstest.Case) {
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				g := gitfstest.New(t, format)
				c.Setup(g)
				scanBothHidden(t, g.Dir, ScanOptions{}, gitfstest.HiddenChange[c.Name])
			})
		}
	}
	run("sha1", gitfstest.Cases)
	run("sha256", gitfstest.SHA256Cases)
}

func TestScanFSOptions(t *testing.T) {
	g := gitfstest.New(t, "")
	g.Write("Dockerfile", "FROM alpine:3.20\n")
	g.Write("package.json", `{"name":"acme","dependencies":{"left-pad":"1.3.0"}}`)
	g.Commit("one")
	g.Git("remote", "add", "origin", "git@github.com:acme/app.git")
	for name, o := range map[string]ScanOptions{
		"repo id":   {RepoID: "example.com/acme/other"},
		"date only": {CommittedAt: "2026-01-02T03:04:05Z"},
	} {
		t.Run(name, func(t *testing.T) { scanBoth(t, g.Dir, o) })
	}
	// A given commit only labels the work tree, as ScanWorkTree does.
	sha, at := "0123456789abcdef0123456789abcdef01234567", "2026-01-02T03:04:05Z"
	want, _, err := ScanWorkTree(g.Dir, "", sha, at, plugin.All(), config.Operator{})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ScanFS(os.DirFS(g.Dir), filepath.Base(g.Dir), ScanOptions{Commit: sha, CommittedAt: at, Plugins: plugin.All()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canon(t, got), canon(t, want)) {
		t.Error("labelled ScanFS differs from ScanWorkTree")
	}
	s, _, err := ScanFS(os.DirFS(g.Dir), "x", ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "github.com/acme/app" {
		t.Errorf("repo = %q", s.Repo)
	}
	// Outside git the labels must be given, and the repo is named by the folder.
	plain := t.TempDir()
	os.WriteFile(filepath.Join(plain, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o644)
	if _, _, err := ScanFS(os.DirFS(plain), "plain", ScanOptions{}); err == nil {
		t.Error("scan without a commit and without git worked")
	}
	s, _, err = ScanFS(os.DirFS(plain), "plain", ScanOptions{Commit: "abc", CommittedAt: "2026-01-02T03:04:05Z"})
	if err != nil || s.Repo != "local/plain" || s.Commit.SHA != "abc" {
		t.Errorf("labelled scan = %v, %v", s, err)
	}
}

// This repository itself, cloned so that its .git is a folder (a worktree's
// is a file), with and without an origin of its own.
func TestScanFSSelf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", "HEAD").CombinedOutput(); err != nil {
		t.Skipf("not a git checkout: %s", out)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(t.TempDir(), "cernly")
	if out, err := exec.Command("git", "clone", "-q", "--local", "--no-hardlinks", root, dir).CombinedOutput(); err != nil {
		t.Skipf("clone: %v\n%s", err, out)
	}
	scanBoth(t, dir, ScanOptions{})
	if out, err := exec.Command("git", "-C", dir, "remote", "set-url", "origin", "https://user:token@github.com/Acme/Cernly.git").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	scanBoth(t, dir, ScanOptions{})
	// Packed after a gc, with a change on top.
	if out, err := exec.Command("git", "-C", dir, "gc", "-q").CombinedOutput(); err != nil {
		t.Fatalf("gc: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644)
	scanBoth(t, dir, ScanOptions{})
}

// ScanOpenedFS scans a repo opened once with OpenFSMax and leaves it open.
func TestScanOpenedFS(t *testing.T) {
	g := gitfstest.New(t, "")
	g.Write("Dockerfile", "FROM alpine:3.20\n")
	g.Commit("c")
	o := ScanOptions{Plugins: plugin.All()}
	want, _, err := ScanFS(os.DirFS(g.Dir), filepath.Base(g.Dir), o)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.OpenFSMax(os.DirFS(g.Dir), filepath.Base(g.Dir), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _, err := ScanOpenedFS(r, filepath.Base(g.Dir), o)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canon(t, got), canon(t, want)) {
		t.Error("ScanOpenedFS differs from ScanFS")
	}
	if _, err := r.GitFS().Head(); err != nil { // still open
		t.Errorf("repo closed by the scan: %v", err)
	}
}
