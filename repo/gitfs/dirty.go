package gitfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
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
// build counts it); a symlink is not looked at. When core.autocrlf is true or
// input, or the root .gitattributes gives the file a text or eol attribute, a
// file that differs only in CRLF line ends counts as clean. That is an
// approximation of git's filters: ident, working-tree-encoding, nested
// .gitattributes, .git/info/attributes and a clean filter are not applied, so
// for a file they touch the answer can differ from git's.
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
	changed, err := r.changed(fsys, ix, 1)
	return len(changed) > 0, err
}

// Changed lists the tracked regular files whose work-tree content differs from
// the index, as Dirty counts a file dirty (a missing file, a non-regular one,
// a different size or content), in index order, at most max. A symlink in the
// work tree is not told from a file when the FS cannot Lstat.
func (r *Repo) Changed(fsys fs.FS, max int) ([]string, error) {
	ix, err := r.Index()
	if err != nil {
		return nil, err
	}
	return r.changed(fsys, ix, max)
}

func (r *Repo) changed(fsys fs.FS, ix *Index, max int) ([]string, error) {
	fi, err := fs.Stat(fsys, ".git/index")
	if err != nil {
		return nil, err
	}
	indexSec := fi.ModTime().Unix()
	eol := r.eolRules(fsys)
	var out []string
	for _, e := range ix.Entries {
		if len(out) >= max {
			break
		}
		if e.Mode != 0o100644 && e.Mode != 0o100755 || e.SkipWorktree || e.AssumeValid {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == e.Path {
			continue
		}
		dirty, err := r.fileDirty(fsys, e, indexSec, eol.normalizes(e.Path))
		if err != nil {
			return nil, err
		}
		if dirty {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// eolRule is one line of the root .gitattributes that sets or unsets line-end
// conversion: on for text, text=... and eol=..., off for -text, !text and
// binary.
type eolRule struct {
	pattern string
	on      bool
}

// eolRules says, per file, whether git would turn CRLF into LF when it hashes
// the work-tree file: always when core.autocrlf is true or input, otherwise by
// the root .gitattributes lines whose pattern matches the file's path.
type eolRules struct {
	all   bool
	rules []eolRule
}

func (r *Repo) eolRules(fsys fs.FS) eolRules {
	if v, ok := r.cfg.get("core.autocrlf"); ok && (isTrue(v) || strings.EqualFold(v, "input")) {
		return eolRules{all: true}
	}
	data, err := readCapped(fsys, ".gitattributes", 1<<20)
	if err != nil {
		return eolRules{}
	}
	var out eolRules
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "[attr]") {
			continue
		}
		for _, a := range f[1:] {
			switch {
			case a == "text" || strings.HasPrefix(a, "text=") || strings.HasPrefix(a, "eol="):
				out.rules = append(out.rules, eolRule{f[0], true})
			case a == "-text" || a == "!text" || a == "binary":
				out.rules = append(out.rules, eolRule{f[0], false})
			}
		}
	}
	return out
}

// normalizes reports the conversion for p; the last matching line wins, as
// in git.
func (e eolRules) normalizes(p string) bool {
	if e.all {
		return true
	}
	on := false
	for _, r := range e.rules {
		if attrMatch(r.pattern, p) {
			on = r.on
		}
	}
	return on
}

// attrMatch matches a .gitattributes pattern against a slash-separated path
// from the root: a pattern without a slash matches the base name at any depth,
// one with a slash the whole path from the root; a leading "**/" matches at any
// depth and a trailing "/**" everything below. Other uses of "**" do not match.
func attrMatch(pattern, p string) bool {
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		parts := strings.Split(p, "/")
		for i := range parts {
			if attrMatch("/"+rest, strings.Join(parts[i:], "/")) {
				return true
			}
		}
		return false
	}
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		return strings.HasPrefix(p, strings.TrimPrefix(dir, "/")+"/")
	}
	if strings.Contains(pattern, "**") {
		return false
	}
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(p))
		return ok
	}
	ok, _ := path.Match(strings.TrimPrefix(pattern, "/"), p)
	return ok
}

func readCapped(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, max)
	}
	return data, nil
}

func (r *Repo) fileDirty(fsys fs.FS, e IndexEntry, indexSec int64, eol bool) (bool, error) {
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
	// As git: a size other than the index's is a change, whatever the line
	// ends; only a file of that size is hashed again (with conversion).
	sameSize := uint32(fi.Size()) == e.Size
	if !sameSize {
		return true, nil
	}
	mtimeMs := fi.ModTime().UnixMilli()
	entryMs := int64(e.MtimeSec)*1000 + int64(e.MtimeNsec)/1_000_000
	if sameSize && mtimeMs == entryMs && fi.ModTime().Unix() < indexSec {
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
	if same && id == e.ID {
		return false, nil
	}
	if !eol || fi.Size() > maxObject {
		return true, nil
	}
	data, err := readCapped(fsys, e.Path, maxObject)
	if err != nil {
		return false, err
	}
	if bytes.IndexByte(data, 0) >= 0 { // binary: git does not convert it
		return true, nil
	}
	return r.BlobID(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))) != e.ID, nil
}
