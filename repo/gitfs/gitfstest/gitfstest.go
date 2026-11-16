// Package gitfstest builds real git repositories for tests that compare
// gitfs and the FS-based scan with git itself.
package gitfstest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Repo is a git repository in a temporary folder.
type Repo struct {
	T       *testing.T
	Dir     string
	Format  string
	commits int
}

// New makes an empty repository; the test is skipped without git. Git runs
// with no global or system config and fixed identities, and the process
// environment is set the same way for code under test that runs git.
func New(t *testing.T, objectFormat string) *Repo {
	t.Helper()
	if objectFormat == "" {
		objectFormat = "sha1"
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	g := &Repo{T: t, Dir: t.TempDir(), Format: objectFormat}
	if out, err := g.TryGit("init", "-q", "-b", "main", "--object-format="+objectFormat); err != nil {
		t.Skipf("git cannot init a %s repository: %s", objectFormat, out)
	}
	g.Git("config", "user.name", "Acme Dev")
	g.Git("config", "user.email", "dev@example.com")
	return g
}

// Git runs git in the repository and returns its output; failure fails the test.
func (g *Repo) Git(args ...string) string {
	g.T.Helper()
	out, err := g.TryGit(args...)
	if err != nil {
		g.T.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// TryGit is Git that returns the error.
func (g *Repo) TryGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	n := g.commits + 1
	zone := "+0200"
	if n%2 == 0 {
		zone = "-0530"
	}
	date := fmt.Sprintf("2026-11-%02dT10:00:00 %s", n%28+1, zone)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Acme Dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=Acme Dev", "GIT_COMMITTER_EMAIL=dev@example.com", "GIT_COMMITTER_DATE="+date,
		"GIT_EDITOR=true",
	)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// Write writes a file (slash path) with mode 0644, making folders.
func (g *Repo) Write(rel, content string) {
	g.T.Helper()
	p := filepath.Join(g.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.T.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		g.T.Fatal(err)
	}
}

// Touch sets a file's modification time to now plus d.
func (g *Repo) Touch(rel string, d time.Duration) {
	g.T.Helper()
	at := time.Now().Add(d)
	if err := os.Chtimes(filepath.Join(g.Dir, filepath.FromSlash(rel)), at, at); err != nil {
		g.T.Fatal(err)
	}
}

// Commit stages everything and commits.
func (g *Repo) Commit(msg string) {
	g.T.Helper()
	g.Git("add", "-A")
	g.commits++
	g.Git("commit", "-q", "--allow-empty", "-m", msg)
}

// Case is one repository state to compare.
type Case struct {
	Name  string
	Setup func(g *Repo)
}

func base(g *Repo) {
	g.Write("README.md", "# acme\n")
	g.Write("src/main.go", "package main\n\nfunc main() {}\n")
	g.Write("src/util/util.go", "package util\n")
	g.Write("src/util/util_test.go", "package util\n")
	g.Write("docs/guide.txt", "guide\n")
	g.Write("ünï cødé/file with space.txt", "x\n")
	g.Write("vendor/skipped.go", "package skipped\n")
	g.Commit("base")
}

func big(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %05d of the big file, some padding text to compress %s\n", i, tag)
	}
	return b.String()
}

// Cases is the catalogue: every state differential tests run through.
var Cases = []Case{
	{"loose", func(g *Repo) { base(g) }},
	{"packed", func(g *Repo) {
		base(g)
		for i := 0; i < 30; i++ {
			g.Write("big.txt", strings.Replace(big(3000, "v"), "line 01500", fmt.Sprintf("edit %05d", i), 1))
			g.Write(fmt.Sprintf("many/f%03d.txt", i), "x\n")
			g.Write("many/f000.txt", fmt.Sprintf("changed %d\n", i))
			g.Commit(fmt.Sprintf("c%d", i))
		}
		g.Git("repack", "-adfq", "--depth=50", "--window=50")
		g.Git("pack-refs", "--all")
		g.Git("prune-packed")
		if !strings.Contains(g.Git("verify-pack", "-v", firstPack(g)), "chain length") {
			g.T.Fatal("no deltas in the pack")
		}
	}},
	{"packed-branches", func(g *Repo) {
		base(g)
		g.Git("checkout", "-q", "-b", "feature/x")
		g.Write("a.txt", "a\n")
		g.Commit("a")
		g.Git("tag", "-a", "-m", "tag", "v1")
		g.Git("gc", "-q")
	}},
	{"detached", func(g *Repo) {
		base(g)
		g.Write("a.txt", "a\n")
		g.Commit("second")
		g.Git("checkout", "-q", "--detach", "HEAD~1")
	}},
	{"detached-tag", func(g *Repo) {
		base(g)
		g.Git("tag", "-a", "-m", "tag", "v1")
		g.Write("a.txt", "a\n")
		g.Commit("second")
		g.Git("checkout", "-q", "--detach", "v1")
	}},
	{"index-v3-skip-worktree", func(g *Repo) {
		base(g)
		g.Git("update-index", "--skip-worktree", "docs/guide.txt")
		g.Write("docs/guide.txt", "modified, hidden by skip-worktree\n")
	}},
	{"skip-worktree-deleted", func(g *Repo) {
		base(g)
		g.Git("update-index", "--skip-worktree", "docs/guide.txt")
		if err := os.Remove(filepath.Join(g.Dir, "docs/guide.txt")); err != nil {
			g.T.Fatal(err)
		}
	}},
	{"assume-unchanged", func(g *Repo) {
		base(g)
		g.Git("update-index", "--assume-unchanged", "docs/guide.txt")
		g.Write("docs/guide.txt", "modified\n")
	}},
	{"index-v4", func(g *Repo) {
		base(g)
		g.Git("update-index", "--index-version", "4")
		g.Write("src/util/extra.go", "package util\n")
		g.Git("add", "src/util/extra.go")
	}},
	{"index-v4-clean", func(g *Repo) {
		base(g)
		g.Git("update-index", "--index-version", "4")
	}},
	{"split-index", func(g *Repo) {
		g.Git("config", "splitIndex.maxPercentChange", "100")
		for i := 0; i < 20; i++ {
			g.Write(fmt.Sprintf("d%d/f%02d.txt", i%3, i), fmt.Sprintf("file %d\n", i))
		}
		g.Commit("base")
		g.Git("update-index", "--split-index")
		g.Write("d0/f00.txt", "edited zero\n")
		g.Write("d1/f04.txt", "edited four\n")
		g.Write("d1/f07.txt", "edited seven\n")
		g.Git("add", "d0/f00.txt", "d1/f04.txt", "d1/f07.txt")
		g.Git("rm", "-q", "d2/f02.txt", "d0/f09.txt")
		g.Write("new/added.txt", "added\n")
		g.Write("d0/f00a.txt", "added between\n")
		g.Git("add", "new/added.txt", "d0/f00a.txt")
		ix, err := os.ReadFile(filepath.Join(g.Dir, ".git/index"))
		if err != nil || !bytes.Contains(ix, []byte("link")) {
			g.T.Fatal("no split index was written")
		}
	}},
	{"merge-conflict", func(g *Repo) {
		base(g)
		g.Git("checkout", "-q", "-b", "other")
		g.Write("README.md", "# other\n")
		g.Commit("other")
		g.Git("checkout", "-q", "main")
		g.Write("README.md", "# main\n")
		g.Commit("main")
		if _, err := g.TryGit("merge", "other"); err == nil {
			g.T.Fatal("expected a conflict")
		}
	}},
	{"intent-to-add", func(g *Repo) {
		base(g)
		g.Write("new.txt", "new\n")
		g.Git("add", "-N", "new.txt")
	}},
	{"symlink", func(g *Repo) {
		base(g)
		if err := os.Symlink("README.md", filepath.Join(g.Dir, "link.md")); err != nil {
			g.T.Skip("no symlinks")
		}
		g.Commit("link")
	}},
	{"symlink-replaces-file", func(g *Repo) {
		base(g)
		p := filepath.Join(g.Dir, "docs/guide.txt")
		os.Remove(p)
		if err := os.Symlink("../README.md", p); err != nil {
			g.T.Skip("no symlinks")
		}
	}},
	{"submodule-gitlink", func(g *Repo) {
		base(g)
		g.Git("update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(g.Git("rev-parse", "HEAD"))+",sub")
		g.Commit("gitlink")
	}},
	{"staged-change", func(g *Repo) {
		base(g)
		g.Write("README.md", "# changed\n")
		g.Git("add", "README.md")
	}},
	{"staged-new-file", func(g *Repo) {
		base(g)
		g.Write("added.txt", "added\n")
		g.Git("add", "added.txt")
	}},
	{"staged-removal", func(g *Repo) {
		base(g)
		g.Git("rm", "-q", "--cached", "README.md")
	}},
	{"staged-mode-change", func(g *Repo) {
		base(g)
		g.Git("update-index", "--chmod=+x", "README.md")
	}},
	{"unstaged-change", func(g *Repo) {
		base(g)
		g.Write("README.md", "# acmf\n")
		g.Touch("README.md", 10*time.Second)
	}},
	{"unstaged-longer", func(g *Repo) {
		base(g)
		g.Write("README.md", "# acme, longer now\n")
	}},
	{"deleted-file", func(g *Repo) {
		base(g)
		if err := os.Remove(filepath.Join(g.Dir, "README.md")); err != nil {
			g.T.Fatal(err)
		}
	}},
	{"touched-unchanged", func(g *Repo) {
		base(g)
		g.Touch("README.md", 10*time.Second)
		g.Touch("src/main.go", -time.Hour)
	}},
	{"untracked-only", func(g *Repo) {
		base(g)
		g.Write("untracked.txt", "u\n")
		g.Write(".env", "SECRET=1\n")
	}},
	{"clean-filter", func(g *Repo) {
		g.Git("config", "filter.up.clean", "tr a-z A-Z")
		g.Write(".gitattributes", "*.txt filter=up\n")
		g.Write("a.txt", "hello\n")
		g.Commit("filtered")
	}},
	{"clean-filter-touched", func(g *Repo) {
		g.Git("config", "filter.up.clean", "tr a-z A-Z")
		g.Write(".gitattributes", "*.txt filter=up\n")
		g.Write("a.txt", "hello\n")
		g.Commit("filtered")
		g.Touch("a.txt", 10*time.Second)
	}},
	{"untracked-cache", func(g *Repo) {
		base(g)
		g.Git("update-index", "--force-untracked-cache")
		g.Write("untracked.txt", "u\n")
		g.Git("status", "-s")
	}},
	{"origin-https", func(g *Repo) { base(g); g.Git("remote", "add", "origin", "https://github.com/Acme/App.git") }},
	{"origin-scp", func(g *Repo) { base(g); g.Git("remote", "add", "origin", "git@github.com:acme/app.git") }},
	{"origin-ssh", func(g *Repo) { base(g); g.Git("remote", "add", "origin", "ssh://git@example.com:2222/acme/app.git") }},
	{"origin-credentials", func(g *Repo) {
		base(g)
		g.Git("remote", "add", "origin", "https://user:token@example.com/acme/app")
	}},
	{"origin-instead-of", func(g *Repo) {
		base(g)
		g.Git("config", "url.https://example.com/.insteadOf", "ex:")
		g.Git("remote", "add", "origin", "ex:acme/app.git")
	}},
	{"origin-local-path", func(g *Repo) { base(g); g.Git("remote", "add", "origin", "/srv/mirror.git") }},
	{"origin-quoted-config", func(g *Repo) {
		base(g)
		cfg := filepath.Join(g.Dir, ".git/config")
		f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			g.T.Fatal(err)
		}
		defer f.Close()
		f.WriteString("[Remote \"origin\"]\n\tURL = \"https://example.com/acme/app.git\" ; comment\n[core]\n\teditor = vi # x\n")
	}},
	{"empty-commit", func(g *Repo) { g.Commit("empty") }},
	{"unborn", func(g *Repo) {
		g.Write("a.txt", "a\n")
		g.Git("add", "a.txt")
	}},
	{"no-git", func(g *Repo) {
		if err := os.RemoveAll(filepath.Join(g.Dir, ".git")); err != nil {
			g.T.Fatal(err)
		}
		g.Write("README.md", "# plain\n")
		g.Write("src/a.go", "package a\n")
		g.Write("node_modules/x/index.js", "x\n")
		g.Write("vendor/v.go", "package v\n")
		if err := os.Symlink("README.md", filepath.Join(g.Dir, "link.md")); err != nil {
			g.T.Log("no symlinks")
		}
	}},
}

// SHA256Cases are run in a sha256 repository.
var SHA256Cases = []Case{
	{"sha256-loose", func(g *Repo) { base(g) }},
	{"sha256-packed", func(g *Repo) {
		base(g)
		for i := 0; i < 10; i++ {
			g.Write("big.txt", strings.Replace(big(2000, "v"), "line 01000", fmt.Sprintf("edit %05d", i), 1))
			g.Commit(fmt.Sprintf("c%d", i))
		}
		g.Git("gc", "-q", "--aggressive")
		g.Write("README.md", "# dirty\n")
	}},
}

func firstPack(g *Repo) string {
	m, _ := filepath.Glob(filepath.Join(g.Dir, ".git/objects/pack/*.idx"))
	if len(m) == 0 {
		g.T.Fatal("no pack")
	}
	return m[0]
}

// HiddenChange names the cases with a changed file git does not look at
// (skip-worktree, assume-unchanged): git status calls them clean, the
// git-free check dirty, because it refuses any file that is not its blob.
var HiddenChange = map[string]bool{"index-v3-skip-worktree": true, "assume-unchanged": true}
