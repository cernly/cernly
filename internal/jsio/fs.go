//go:build js && wasm

package jsio

import (
	"errors"
	"io"
	"io/fs"
	"sort"
	"syscall/js"
	"time"
)

// FS is a read-only fs.FS over a folder the page picked. src has three
// methods, each returning a promise:
//
//	stat(path)              -> {dir, link, size, mtime (ms)} or null
//	readDir(path)           -> [{name, dir, link}]
//	read(path, off, length) -> Uint8Array (shorter at the end of the file)
//
// Paths are fs.ValidPath names; "." is the folder. Nothing is read until a
// file is: a pack file of gigabytes is read only where ReadAt asks. An FS is
// one scan's view of the folder: each path is stat'ed once (every call is a
// round trip to the page), as a folder that changes during a scan gives no
// answer worth keeping anyway.
//
// A source sets link on an entry it knows is a symbolic link. FS reports it
// as one and never follows it (Lstat is Stat, Open refuses it), so the scan
// skips it as the CLI does. A browser that follows links without saying so
// gives a regular file here: the page says so to the reader.
type FS struct {
	src   js.Value
	stats map[string]statResult
}

type statResult struct {
	fi  fs.FileInfo
	err error
}

// NewFS wraps a source object.
func NewFS(src js.Value) *FS { return &FS{src: src, stats: map[string]statResult{}} }

var (
	_ fs.StatFS     = (*FS)(nil)
	_ fs.ReadDirFS  = (*FS)(nil)
	_ fs.ReadLinkFS = (*FS)(nil)
)

// errLink is the answer to reading through a symbolic link.
var errLink = errors.New("a symbolic link, not followed")

// Lstat is Stat: a link is never followed.
func (f *FS) Lstat(name string) (fs.FileInfo, error) { return f.Stat(name) }

// ReadLink refuses: the page never knows a link's target.
func (f *FS) ReadLink(name string) (string, error) {
	return "", &fs.PathError{Op: "readlink", Path: name, Err: errLink}
}

func (f *FS) call(method string, args ...any) (js.Value, error) {
	return Await(f.src.Call(method, args...))
}

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if r, ok := f.stats[name]; ok {
		return r.fi, r.err
	}
	fi, err := f.stat(name)
	f.stats[name] = statResult{fi, err}
	return fi, err
}

func (f *FS) stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	v, err := f.call("stat", name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	if v.IsNull() || v.IsUndefined() {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return info{name: base(name), dir: v.Get("dir").Truthy(), link: v.Get("link").Truthy(), size: int64(v.Get("size").Float()), mtime: int64(v.Get("mtime").Float())}, nil
}

func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	v, err := f.call("readDir", name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	out := make([]fs.DirEntry, v.Length())
	for i := range out {
		e := v.Index(i)
		out[i] = entry{fs: f, dir: name, name: e.Get("name").String(), isDir: e.Get("dir").Truthy(), link: e.Get("link").Truthy()}
	}
	sortEntries(out)
	return out, nil
}

func (f *FS) Open(name string) (fs.File, error) {
	fi, err := f.Stat(name)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return &dir{fs: f, name: name, fi: fi}, nil
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errLink}
	}
	return &file{fs: f, name: name, fi: fi}, nil
}

type info struct {
	name        string
	dir, link   bool
	size, mtime int64
}

func (i info) Name() string { return i.name }
func (i info) Size() int64  { return i.size }
func (i info) Mode() fs.FileMode {
	if i.link {
		return fs.ModeSymlink | 0o777
	}
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i info) ModTime() time.Time { return time.UnixMilli(i.mtime) }
func (i info) IsDir() bool        { return i.dir && !i.link }
func (i info) Sys() any           { return nil }

type entry struct {
	fs          *FS
	dir, name   string
	isDir, link bool
}

func (e entry) Name() string { return e.name }
func (e entry) IsDir() bool  { return e.isDir && !e.link }
func (e entry) Type() fs.FileMode {
	if e.link {
		return fs.ModeSymlink
	}
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e entry) Info() (fs.FileInfo, error) { return e.fs.Stat(join(e.dir, e.name)) }

type dir struct {
	fs      *FS
	name    string
	fi      fs.FileInfo
	entries []fs.DirEntry
	read    bool
}

func (d *dir) Stat() (fs.FileInfo, error) { return d.fi, nil }
func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}
func (d *dir) Close() error { return nil }
func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	if !d.read {
		all, err := d.fs.ReadDir(d.name)
		if err != nil {
			return nil, err
		}
		d.entries, d.read = all, true
	}
	if n <= 0 {
		out := d.entries
		d.entries = nil
		return out, nil
	}
	if len(d.entries) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(d.entries))
	out := d.entries[:n]
	d.entries = d.entries[n:]
	return out, nil
}

// file reads lazily: a small file in one call on its first Read, a large one
// where Read or ReadAt asks.
type file struct {
	fs   *FS
	name string
	fi   fs.FileInfo
	off  int64
	buf  []byte // a small file's content, once read
}

const whole = 4 << 20

func (f *file) Stat() (fs.FileInfo, error) { return f.fi, nil }
func (f *file) Close() error               { return nil }

func (f *file) Read(p []byte) (int, error) {
	if f.off == 0 && f.buf == nil && f.fi.Size() <= whole {
		// The caller grows its buffer as it goes (io.ReadAll): one call to
		// the page, not one per buffer size.
		f.buf = make([]byte, f.fi.Size())
		n, err := f.ReadAt(f.buf, 0)
		if err != nil && err != io.EOF {
			return 0, err
		}
		f.buf = f.buf[:n]
	}
	if f.buf != nil {
		if f.off >= int64(len(f.buf)) {
			return 0, io.EOF
		}
		n := copy(p, f.buf[f.off:])
		f.off += int64(n)
		return n, nil
	}
	if f.off >= f.fi.Size() {
		return 0, io.EOF
	}
	n, err := f.ReadAt(p, f.off)
	f.off += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= f.fi.Size() {
		return 0, io.EOF
	}
	v, err := f.fs.call("read", f.name, off, len(p))
	if err != nil {
		return 0, &fs.PathError{Op: "read", Path: f.name, Err: err}
	}
	if v.Get("length").Int() > len(p) {
		return 0, &fs.PathError{Op: "read", Path: f.name, Err: errors.New("read more than asked")}
	}
	n := js.CopyBytesToGo(p, v)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func base(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

func join(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

func sortEntries(es []fs.DirEntry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name() < es[j].Name() })
}
