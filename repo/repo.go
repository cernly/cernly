// Package repo gives plugins a read-only, deterministic view of one checkout.
//
// Only files tracked by git are visible. That matters twice: untracked files
// (a real .env, local build output) are never read, and two checkouts of the
// same commit always present the same file list.
package repo

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cernly/cernly/defaults"
)

// skipped reports a folder name never scanned, even when tracked (vendored
// or generated code); the list is in the shipped defaults.
func skipped(name string) bool {
	for _, d := range defaults.Get().Files.SkipDirs {
		if name == d {
			return true
		}
	}
	return false
}

type Repo struct {
	Root  string   // absolute path of the checkout
	Files []string // repo-relative, forward slashes, sorted
	byDir map[string][]string
	blobs map[string]string // commit view (OpenCommit): path -> blob id
	cat   *catFile
	max   int64 // largest file Read returns; 0 is the shipped max_file_bytes
	tree  bool  // a checkout inside a git work tree (Open), not a walk
	memo  memo  // per-scan results shared by plugins, see Memo
}

func (r *Repo) maxBytes() int64 {
	if r.max > 0 {
		return r.max
	}
	return defaults.Get().MaxFileBytes
}

func tooLarge(rel string, max int64) error {
	return fmt.Errorf("%s: larger than %d bytes, not read", rel, max)
}

// Open lists tracked files with `git ls-files`, also when root is a folder
// inside a work tree (paths are then relative to root). Outside git it falls
// back to a directory walk (used by tests on plain folders). A bare
// repository or a git directory has no files as they are: use OpenCommit.
func Open(root string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	inTree, err := insideWorkTree(abs)
	if err != nil {
		return nil, err
	}
	var files []string
	if inTree {
		// --stage gives each entry's mode: 120000 is a symlink, 160000 a
		// submodule. Both are skipped, so no plugin can read outside the
		// checkout through a link. Run in root, ls-files lists only what
		// is below it, relative to it.
		cmd := Git(abs, "ls-files", "--stage", "-z")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w", err)
		}
		for _, e := range bytes.Split(out, []byte{0}) {
			tab := bytes.IndexByte(e, '\t')
			if tab < 0 {
				continue
			}
			if mode := string(e[:6]); mode == "120000" || mode == "160000" {
				continue
			}
			files = append(files, string(e[tab+1:]))
		}
		files = dedupe(files) // a file in a merge conflict has several stages
	} else {
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != abs && skipped(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil // symlinks and other special files, as in git mode
			}
			rel, _ := filepath.Rel(abs, p)
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var kept []string
	for _, f := range files {
		if !inSkippedDir(f) {
			kept = append(kept, f)
		}
	}
	sort.Strings(kept)
	r := &Repo{Root: abs, tree: inTree}
	r.setFiles(kept)
	return r, nil
}

// Dirty reports uncommitted changes, staged or not, to tracked files under
// Root: the working tree Read sees then differs from HEAD. A commit view or
// a folder outside git is never dirty. Submodules are ignored, as in Open.
func (r *Repo) Dirty() (bool, error) {
	if !r.tree || r.blobs != nil {
		return false, nil
	}
	// status may rehash a changed file through a clean filter: switch every
	// configured one off. GIT_OPTIONAL_LOCKS=0 (in Git) keeps it from
	// writing the refreshed index.
	args, err := noFilters(r.Root)
	if err != nil {
		return false, err
	}
	args = append(args, "status", "--porcelain", "-z", "--untracked-files=no", "--ignore-submodules=all", "--", ".")
	out, err := Git(r.Root, args...).Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(out) > 0, nil
}

// InsideWorkTree reports whether dir is in a git work tree (false outside
// git; an error for a bare repository or a git directory).
func InsideWorkTree(dir string) (bool, error) { return insideWorkTree(dir) }

// insideWorkTree reports whether dir is in a git work tree, false when it is
// not in git at all. A bare repository or a git directory is an error.
func insideWorkTree(dir string) (bool, error) {
	cmd := Git(dir, "rev-parse", "--is-inside-work-tree")
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return false, nil
		}
		return false, fmt.Errorf("git rev-parse: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if strings.TrimSpace(string(out)) != "true" {
		return false, fmt.Errorf("%s: a bare repository or git directory has no checkout; pass --commit", dir)
	}
	return true, nil
}

// Restrict narrows the visible files to keep (which must be a subset of the
// current files, as produced by the scan's exclusion plan).
func (r *Repo) Restrict(keep []string) {
	sorted := append([]string(nil), keep...)
	sort.Strings(sorted)
	r.setFiles(sorted)
}

func (r *Repo) setFiles(files []string) {
	r.Files = files
	r.byDir = map[string][]string{}
	for _, f := range files {
		d := path.Dir(f)
		r.byDir[d] = append(r.byDir[d], f)
	}
}

func dedupe(files []string) []string {
	seen := map[string]bool{}
	out := files[:0]
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func inSkippedDir(f string) bool {
	parts := strings.Split(f, "/")
	for _, p := range parts[:len(parts)-1] {
		if skipped(p) {
			return true
		}
	}
	return false
}

// Match returns tracked files whose base name satisfies match, sorted.
func (r *Repo) Match(match func(base string) bool) []string {
	var out []string
	for _, f := range r.Files {
		if match(path.Base(f)) {
			out = append(out, f)
		}
	}
	return out
}

// Has reports whether a tracked file exists at the repo-relative path.
func (r *Repo) Has(rel string) bool {
	for _, f := range r.byDir[path.Dir(rel)] {
		if f == rel {
			return true
		}
	}
	return false
}

// Read returns a tracked file's content; a file over max_file_bytes is an
// error, so a crafted repo cannot exhaust memory.
func (r *Repo) Read(rel string) ([]byte, error) {
	data, err := r.read(rel)
	// A leading UTF-8 byte order mark is not content: every parser would
	// otherwise see it glued to the first token. Line numbers are unchanged.
	return bytes.TrimPrefix(data, bom), err
}

var bom = []byte("\xEF\xBB\xBF")

func (r *Repo) read(rel string) ([]byte, error) {
	max := r.maxBytes()
	if r.blobs != nil {
		b, ok := r.blobs[rel]
		if !ok {
			return nil, fmt.Errorf("%s: not in the commit's tree", rel)
		}
		data, err := r.cat.blob(b, max)
		if err == errTooLarge {
			return nil, tooLarge(rel, max)
		}
		return data, err
	}
	// A tracked file may have been replaced by a symlink in the working
	// tree, or one of its folders: only regular files under real folders
	// are read.
	p := r.Root
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("%s: not a repo-relative path", rel)
		}
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if last := i == len(parts)-1; last && !fi.Mode().IsRegular() || !last && !fi.IsDir() {
			return nil, fmt.Errorf("%s: not a regular file", rel)
		}
		if i == len(parts)-1 && fi.Size() > max {
			return nil, tooLarge(rel, max)
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1)) // it may have grown since
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, tooLarge(rel, max)
	}
	return data, nil
}

// Dir returns the repo-relative directory of a file, "." for the root.
func Dir(rel string) string { return path.Dir(rel) }

// Join joins repo-relative path elements and cleans the result. It returns
// ok=false when the result escapes the repository (starts with "..").
func Join(elem ...string) (string, bool) {
	p := path.Clean(path.Join(elem...))
	if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return "", false
	}
	return p, true
}
