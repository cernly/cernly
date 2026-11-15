//go:build js && wasm

package main

import (
	"io/fs"
	"path"
	"strings"
	"syscall/js"
	"testing"
	"testing/fstest"
	"time"
)

// dirSource is a folder source (see jsio.FS) over an in-memory folder, as
// the page's would answer; links names the paths it reports as symbolic
// links. In memory, since a callback from JavaScript must not wait on the
// file system.
func dirSource(fsys fs.FS, links map[string]bool) js.Value {
	resolve := func(v any) any { return js.Global().Get("Promise").Call("resolve", v) }
	obj := js.Global().Get("Object").New()
	obj.Set("stat", js.FuncOf(func(_ js.Value, a []js.Value) any {
		p := a[0].String()
		fi, err := fs.Stat(fsys, p)
		if err != nil {
			return resolve(js.Null())
		}
		return resolve(map[string]any{"dir": fi.IsDir(), "link": links[p], "size": float64(fi.Size()), "mtime": float64(fi.ModTime().UnixMilli())})
	}))
	obj.Set("readDir", js.FuncOf(func(_ js.Value, a []js.Value) any {
		ents, _ := fs.ReadDir(fsys, a[0].String())
		out := make([]any, len(ents))
		for i, e := range ents {
			p := path.Join(a[0].String(), e.Name())
			out[i] = map[string]any{"name": e.Name(), "dir": e.IsDir(), "link": links[p]}
		}
		return resolve(out)
	}))
	obj.Set("read", js.FuncOf(func(_ js.Value, a []js.Value) any {
		data, _ := fs.ReadFile(fsys, a[0].String())
		off, n := a[1].Int(), a[2].Int()
		if off > len(data) {
			off = len(data)
		}
		data = data[off:min(len(data), off+n)]
		u := js.Global().Get("Uint8Array").New(len(data))
		js.CopyBytesToJS(u, data)
		return resolve(u)
	}))
	return obj
}

func folder(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for p, c := range files {
		m[p] = &fstest.MapFile{Data: []byte(c), Mode: 0o644, ModTime: time.UnixMilli(1000)}
	}
	return m
}

const env = "DATABASE_URL=postgres://db.example.com/shop\n"

func TestScanFolder(t *testing.T) {
	dir := folder(map[string]string{".env.example": env, "docs/a.md": "hi\n"})
	s, err := scanFolder(dirSource(dir, nil), "shop", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "local/shop" || s.Git || len(s.View) == 0 || s.ViewError != "" || !strings.Contains(s.Facts, "DATABASE_URL") {
		t.Fatalf("got repo %q git %v view %d bytes, error %q", s.Repo, s.Git, len(s.View), s.ViewError)
	}
}

// A path the source reports as a link is neither listed nor read.
func TestScanFolderSkipsLinks(t *testing.T) {
	dir := folder(map[string]string{".env.example": env})
	s, err := scanFolder(dirSource(dir, map[string]bool{".env.example": true}), "shop", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Facts, "DATABASE_URL") {
		t.Fatal("a file reported as a link was read")
	}
}

func TestScanFolderRefusesTooManyFiles(t *testing.T) {
	was := maxFiles
	maxFiles = 1
	defer func() { maxFiles = was }()
	dir := folder(map[string]string{"a": "1", "b": "2"})
	_, err := scanFolder(dirSource(dir, nil), "shop", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "more than the 1 a browser scan reads") {
		t.Fatalf("got %v", err)
	}
}
