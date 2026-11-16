package gitfs

import (
	"bytes"
	"fmt"
	"io/fs"
	"sync"
)

// NotCommittedError is a tracked file whose bytes are not the blob the
// index records for it: an uncommitted change, or a symbolic link the folder
// source read through to another file, which a browser cannot tell from a
// file. Its content is never used as the commit's.
type NotCommittedError struct{ Path string }

func (e *NotCommittedError) Error() string {
	return fmt.Sprintf("%s: its bytes are not the committed file's (an uncommitted change, or a link to another file); not read", e.Path)
}

// verifier holds what Verify needs, read once: each path's stage-0 blob and
// the line-end rules.
type verifier struct {
	once  sync.Once
	blobs map[string]string
	eol   eolRules
	err   error
}

// Verify checks data, the bytes read from the work tree in fsys for the
// tracked file p, against the blob the index records for it, with the CRLF
// conversion git would apply (Changed's rules). A file with no stage-0 entry
// (in a merge conflict) or other bytes is a *NotCommittedError.
func (r *Repo) Verify(fsys fs.FS, p string, data []byte) error {
	v := &r.verify
	v.once.Do(func() {
		ix, err := r.Index()
		if err != nil {
			v.err = err
			return
		}
		v.blobs = map[string]string{}
		for _, e := range ix.Entries {
			if e.Stage == 0 {
				v.blobs[e.Path] = e.ID
			}
		}
		v.eol = r.eolRules(fsys)
	})
	if v.err != nil {
		return v.err
	}
	id, ok := v.blobs[p]
	if ok && r.BlobID(data) == id {
		return nil
	}
	if ok && v.eol.normalizes(p) && bytes.IndexByte(data, 0) < 0 &&
		r.BlobID(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))) == id {
		return nil
	}
	return &NotCommittedError{Path: p}
}
