package gitfs

import (
	"errors"
	"io/fs"
)

const (
	modeSymlink = 0o120000
	modeGitlink = 0o160000
)

// Dirty reports what `git status --porcelain --untracked-files=no
// --ignore-submodules=all` would: a staged difference from HEAD, an unmerged or
// intent-to-add entry, or a tracked regular file that differs from the index.
// fsys is the work tree. A file is hashed (without filters) only when its size
// or its modification time (to the millisecond) differs from the index, or when
// it is as new as the index file, to the second (racy git, as a default git
// build counts it); a symlink is not looked at.
func (r *Repo) Dirty(fsys fs.FS) (bool, error) {
	ix, err := r.Index()
	if err != nil {
		return false, err
	}
	head := map[string]TreeEntry{}
	if id, err := r.Head(); err == nil {
		c, err := r.Commit(id)
		if err != nil {
			return false, err
		}
		if head, err = r.Tree(c.Tree); err != nil {
			return false, err
		}
	} else if !errors.Is(err, ErrUnborn) {
		return false, err
	}
	seen := map[string]bool{}
	for _, e := range ix.Entries {
		if e.Stage > 0 || e.IntentToAdd {
			return true, nil
		}
		if e.Mode == modeGitlink {
			continue
		}
		h, ok := head[e.Path]
		if !ok || h.Mode != e.Mode || h.ID != e.ID {
			return true, nil
		}
		seen[e.Path] = true
	}
	for p, h := range head {
		if h.Mode != modeGitlink && !seen[p] {
			return true, nil // staged for removal
		}
	}
	fi, err := fs.Stat(fsys, ".git/index")
	if err != nil {
		return false, err
	}
	indexSec := fi.ModTime().Unix()
	for _, e := range ix.Entries {
		if e.Mode != 0o100644 && e.Mode != 0o100755 || e.SkipWorktree || e.AssumeValid {
			continue
		}
		dirty, err := r.fileDirty(fsys, e, indexSec)
		if err != nil || dirty {
			return dirty, err
		}
	}
	return false, nil
}

func (r *Repo) fileDirty(fsys fs.FS, e IndexEntry, indexSec int64) (bool, error) {
	var fi fs.FileInfo
	var err error
	if l, ok := fsys.(fs.ReadLinkFS); ok {
		fi, err = l.Lstat(e.Path)
	} else {
		fi, err = fs.Stat(fsys, e.Path)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return true, nil
	}
	if uint32(fi.Size()) != e.Size {
		return true, nil
	}
	mtimeMs := fi.ModTime().UnixMilli()
	entryMs := int64(e.MtimeSec)*1000 + int64(e.MtimeNsec)/1_000_000
	if mtimeMs == entryMs && fi.ModTime().Unix() < indexSec {
		return false, nil
	}
	f, err := fsys.Open(e.Path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	id, same, err := r.blobIDReader(f, fi.Size())
	if err != nil {
		return false, err
	}
	return !same || id != e.ID, nil
}
