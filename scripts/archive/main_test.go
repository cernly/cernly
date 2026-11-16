package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The same files give the same bytes whatever their modes and dates on disk.
func TestSameBytes(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	make := func(mode os.FileMode, mtime time.Time) string {
		dir := filepath.Join(t.TempDir(), "cernly_1.0.0_linux_amd64")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{"cernly": "bin", "LICENSE": "lic", "README.md": "read"} {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(data), mode); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(p, mtime, mtime)
		}
		return dir
	}
	for _, ext := range []string{".tar.gz", ".zip"} {
		a, b := filepath.Join(t.TempDir(), "a"+ext), filepath.Join(t.TempDir(), "b"+ext)
		if err := run(make(0o600, time.Now()), a, at); err != nil {
			t.Fatal(err)
		}
		if err := run(make(0o777, time.Unix(5, 0)), b, at); err != nil {
			t.Fatal(err)
		}
		da, _ := os.ReadFile(a)
		db, _ := os.ReadFile(b)
		if !bytes.Equal(da, db) {
			t.Errorf("%s: two builds differ", ext)
		}
		modes := map[string]int64{}
		if ext == ".zip" {
			zr, err := zip.NewReader(bytes.NewReader(da), int64(len(da)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range zr.File {
				modes[f.Name] = int64(f.Mode().Perm())
				if !f.Modified.Equal(at) {
					t.Errorf("%s: date %v", f.Name, f.Modified)
				}
			}
		} else {
			gz, err := gzip.NewReader(bytes.NewReader(da))
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gz)
			for h, err := tr.Next(); err == nil; h, err = tr.Next() {
				modes[h.Name] = h.Mode
				if h.Uid != 0 || !h.ModTime.Equal(at) {
					t.Errorf("%s: uid %d date %v", h.Name, h.Uid, h.ModTime)
				}
			}
		}
		want := map[string]int64{"cernly_1.0.0_linux_amd64/": 0o755, "cernly_1.0.0_linux_amd64/LICENSE": 0o644, "cernly_1.0.0_linux_amd64/README.md": 0o644, "cernly_1.0.0_linux_amd64/cernly": 0o755}
		for n, m := range want {
			if modes[n] != m {
				t.Errorf("%s %s: mode %o, want %o", ext, n, modes[n], m)
			}
		}
		if len(modes) != len(want) {
			t.Errorf("%s: entries %v", ext, modes)
		}
	}
}
