package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Symlinks are never visible, so no plugin can read outside the checkout
// through one.
func TestSymlinksSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.sh"), filepath.Join(dir, "link.sh")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 || r.Files[0] != "real.sh" {
		t.Errorf("files = %v, want [real.sh]", r.Files)
	}
}

// A commit view reads that commit's tree from git's objects: not the working
// tree, not untracked files, not symlinks.
func TestOpenCommit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("a.txt", "one\n")
	write("sub/b.txt", "bee")
	write("bom.txt", "\xEF\xBB\xBFbom\n")
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	first := git("rev-parse", "HEAD")
	write("a.txt", "two\n")
	git("commit", "-q", "-am", "second")
	write("a.txt", "dirty\n")      // working tree change
	write("untracked.txt", "nope") // never tracked

	r, sha, err := OpenCommit(dir, first)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if sha != first {
		t.Errorf("sha = %s, want %s", sha, first)
	}
	if got := strings.Join(r.Files, ","); got != "a.txt,bom.txt,sub/b.txt" {
		t.Errorf("files = %s", got)
	}
	for p, want := range map[string]string{"a.txt": "one\n", "sub/b.txt": "bee", "bom.txt": "bom\n"} {
		got, err := r.Read(p)
		if err != nil || string(got) != want {
			t.Errorf("Read(%s) = %q, %v; want %q", p, got, err, want)
		}
	}
	if _, err := r.Read("untracked.txt"); err == nil {
		t.Error("read an untracked file")
	}

	// Over the limit a file is not read, and the next read still works.
	r.max = 3
	if _, err := r.Read("a.txt"); err == nil || !strings.Contains(err.Error(), "larger than 3 bytes") {
		t.Errorf("Read over the limit: %v", err)
	}
	if got, err := r.Read("sub/b.txt"); err != nil || string(got) != "bee" {
		t.Errorf("Read after a skipped blob = %q, %v", got, err)
	}
}

// A leading byte order mark is dropped; one later in the file is content.
func TestReadStripsBOM(t *testing.T) {
	dir := t.TempDir()
	for p, s := range map[string]string{"a.sql": "\xEF\xBB\xBFCREATE TABLE a (id int);\n", "b.txt": "x\xEF\xBB\xBF\n"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"a.sql": "CREATE TABLE a (id int);\n", "b.txt": "x\xEF\xBB\xBF\n"} {
		if got, err := r.Read(p); err != nil || string(got) != want {
			t.Errorf("Read(%s) = %q, %v; want %q", p, got, err, want)
		}
	}
}

// A checkout file over the limit is not read.
func TestReadLimit(t *testing.T) {
	dir := t.TempDir()
	for p, s := range map[string]string{"big.txt": "four", "ok.txt": "two"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.max = 3
	if _, err := r.Read("big.txt"); err == nil || !strings.Contains(err.Error(), "larger than 3 bytes") {
		t.Errorf("Read over the limit: %v", err)
	}
	if got, err := r.Read("ok.txt"); err != nil || string(got) != "two" {
		t.Errorf("Read(ok.txt) = %q, %v", got, err)
	}
}

// A tag named like the branch never wins; a clone without a local head falls
// back to origin's.
func TestBranchRef(t *testing.T) {
	origin := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(origin, "init", "-q", "-b", "main")
	git(origin, "commit", "-q", "--allow-empty", "-m", "first")
	git(origin, "tag", "main")
	if ref, err := BranchRef(origin, "main"); err != nil || ref != "refs/heads/main" {
		t.Errorf("local = %q, %v", ref, err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	git(origin, "clone", "-q", origin, clone)
	git(clone, "checkout", "-q", "--detach")
	git(clone, "branch", "-q", "-D", "main")
	if ref, err := BranchRef(clone, "main"); err != nil || ref != "refs/remotes/origin/main" {
		t.Errorf("clone = %q, %v", ref, err)
	}
	if _, err := BranchRef(clone, "nope"); err == nil {
		t.Error("missing branch resolved")
	}
}

// A folder inside a work tree shows only its tracked files, relative to it;
// a tracked file replaced by a symlink is not read; a bare repository needs
// a commit.
func TestOpenInsideWorkTree(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(dir, "init", "-q")
	write("app/a.txt", "a")
	write("app/conf/b.txt", "b")
	write("top.txt", "top")
	git(dir, "add", "-A")
	git(dir, "commit", "-q", "-m", "first")
	write("app/.env", "SECRET=1") // untracked
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Open(filepath.Join(dir, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Files, ","); got != "a.txt,conf/b.txt" {
		t.Errorf("files = %s", got)
	}
	if b, err := r.Read("conf/b.txt"); err != nil || string(b) != "b" {
		t.Errorf("Read = %q, %v", b, err)
	}

	if err := os.Remove(filepath.Join(dir, "app/a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "app/a.txt")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if b, err := r.Read("a.txt"); err == nil {
		t.Errorf("read through a symlinked file: %q", b)
	}
	if err := os.RemoveAll(filepath.Join(dir, "app/conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "conf/b.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "conf"), filepath.Join(dir, "app/conf")); err != nil {
		t.Fatal(err)
	}
	if b, err := r.Read("conf/b.txt"); err == nil {
		t.Errorf("read through a symlinked folder: %q", b)
	}

	bare := filepath.Join(t.TempDir(), "bare.git")
	git(dir, "clone", "-q", "--bare", dir, bare)
	if _, err := Open(bare); err == nil || !strings.Contains(err.Error(), "--commit") {
		t.Errorf("bare: err = %v", err)
	}
	if _, err := Open(filepath.Join(dir, ".git")); err == nil {
		t.Error("opened a git directory")
	}
}

// A scanned repository's .git/config is untrusted: Dirty must not run its
// fsmonitor hook or a filter driver, whichever attributes file picks it, and
// still see the change.
func TestDirtyRunsNoRepoPrograms(t *testing.T) {
	for _, tc := range []struct{ name, attrs, driver string }{
		{"gitattributes", ".gitattributes", "clean"},
		{"info attributes", ".git/info/attributes", "clean"},
		{"attributesFile", "attrs.txt", "clean"},
		{"process filter", ".gitattributes", "process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marks := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			write := func(p, s string) {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q")
			write("a.txt", "one\n")
			git("add", "a.txt")
			git("commit", "-qm", "x")
			write(tc.attrs, "*.txt filter=evil\n")
			if tc.attrs == "attrs.txt" {
				git("config", "core.attributesFile", filepath.Join(dir, "attrs.txt"))
			}
			git("config", "core.fsmonitor", "touch "+filepath.Join(marks, "fsmonitor"))
			git("config", "filter.evil."+tc.driver, "touch "+filepath.Join(marks, "filter")+"; cat")
			git("config", "filter.evil.required", "true")
			write("a.txt", "two\n") // same size: git has to hash it to tell
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(dir, "a.txt"), future, future); err != nil {
				t.Fatal(err)
			}
			r, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			dirty, err := r.Dirty()
			if err != nil {
				t.Fatal(err)
			}
			if !dirty {
				t.Error("dirty = false, want true")
			}
			for _, m := range []string{"fsmonitor", "filter"} {
				if _, err := os.Stat(filepath.Join(marks, m)); err == nil {
					t.Errorf("git ran the repository's %s program", m)
				}
			}
		})
	}
}

// A repository set up as a partial clone fetches a missing object from its
// promisor remote, and an ext:: or ssh remote is a command. No scan path
// may fetch, whatever the repository's protocol settings allow.
func TestNoLazyFetch(t *testing.T) {
	setup := func(t *testing.T, missing func(git func(...string) string) []string) (dir, mark string) {
		dir, mark = t.TempDir(), filepath.Join(t.TempDir(), "fetched")
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		git("init", "-q")
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-qm", "x")
		for _, kv := range [][2]string{
			{"core.repositoryformatversion", "1"},
			{"extensions.partialClone", "origin"},
			{"remote.origin.promisor", "true"},
			{"remote.origin.url", "ext::sh -c touch% " + mark},
			{"protocol.ext.allow", "always"},
			{"protocol.ssh.allow", "always"},
			{"protocol.file.allow", "always"},
		} {
			git("config", kv[0], kv[1])
		}
		for _, p := range missing(git) {
			if err := os.Remove(filepath.Join(dir, ".git", p)); err != nil {
				t.Fatal(err)
			}
		}
		return dir, mark
	}
	object := func(git func(...string) string, rev string) string {
		sha := git("rev-parse", rev)
		return filepath.Join("objects", sha[:2], sha[2:])
	}
	t.Run("commit", func(t *testing.T) {
		dir, mark := setup(t, func(git func(...string) string) []string {
			return []string{object(git, "HEAD:Dockerfile")}
		})
		r, _, err := OpenCommit(dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		_, _ = r.Read("Dockerfile")
		if _, err := os.Stat(mark); err == nil {
			t.Error("reading a commit fetched from the promisor remote")
		}
	})
	t.Run("work tree", func(t *testing.T) {
		dir, mark := setup(t, func(git func(...string) string) []string {
			return []string{object(git, "HEAD^{tree}"), "index"}
		})
		if r, err := Open(dir); err == nil {
			_, _ = r.Dirty()
		}
		if _, err := os.Stat(mark); err == nil {
			t.Error("git status fetched from the promisor remote")
		}
	})
}
