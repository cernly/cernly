package repo

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/cernly/cernly/repo/gitfs"
)

// OpenFS is Open without git: the checkout is the work tree in fsys, and its
// .git folder, when there is one, is read by gitfs. name is for display only.
// With .git the files are the index's entries (symlinks and submodules
// dropped), as `git ls-files` gives them; without it, the regular files below
// the root, as Open's walk outside git does.
func OpenFS(fsys fs.FS, name string) (*Repo, error) {
	var files []string
	var g *gitfs.Repo
	if _, err := fs.Stat(fsys, ".git"); err == nil {
		if g, err = gitfs.Open(fsys); err != nil {
			return nil, err
		}
		ix, err := g.Index()
		if err != nil {
			g.Close()
			return nil, err
		}
		for _, e := range ix.Entries {
			if e.Mode == 0o120000 || e.Mode == 0o160000 {
				continue
			}
			files = append(files, e.Path)
		}
		files = dedupe(files) // a file in a merge conflict has several stages
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	} else {
		err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != "." && skipped(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				files = append(files, p)
			}
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
	r := &Repo{Root: name, fsys: fsys, git: g}
	r.setFiles(kept)
	return r, nil
}

// GitFS is the reader of the .git folder of an OpenFS repo, nil without one.
func (r *Repo) GitFS() *gitfs.Repo { return r.git }

// readFS reads rel from the work tree in fsys: only a regular file under real
// folders, as read does on disk.
func readFS(fsys fs.FS, rel string, max int64) ([]byte, error) {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("%s: not a repo-relative path", rel)
		}
		p := strings.Join(parts[:i+1], "/")
		var fi fs.FileInfo
		var err error
		if l, ok := fsys.(fs.ReadLinkFS); ok {
			fi, err = l.Lstat(p)
		} else {
			fi, err = fs.Stat(fsys, p)
		}
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
	f, err := fsys.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, tooLarge(rel, max)
	}
	return data, nil
}
