// Command archive packs one release folder into a .tar.gz or a .zip that is
// the same bytes on any host: entries sorted by name, owner 0, fixed modes
// (0755 for the cernly binary and folders, 0644 for the rest) and one fixed
// date. The pinned Go toolchain does the compression, so the host's tar, gzip
// and zip play no part. scripts/release-build.sh runs it:
//
//	go run ./scripts/archive <folder> <out.tar.gz|out.zip> <unix-seconds>
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type entry struct {
	name string // slash-separated, below the folder's own name
	dir  bool
	mode fs.FileMode
	data []byte
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: archive <folder> <out.tar.gz|out.zip> <unix-seconds>")
		os.Exit(2)
	}
	sec, err := strconv.ParseInt(os.Args[3], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archive: the date:", err)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], time.Unix(sec, 0).UTC()); err != nil {
		fmt.Fprintln(os.Stderr, "archive:", err)
		os.Exit(1)
	}
}

func run(dir, out string, at time.Time) error {
	ents, err := collect(dir)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	switch {
	case strings.HasSuffix(out, ".tar.gz"):
		err = writeTarGz(&buf, ents, at)
	case strings.HasSuffix(out, ".zip"):
		err = writeZip(&buf, ents, at)
	default:
		err = fmt.Errorf("%s: neither .tar.gz nor .zip", out)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

// collect reads the folder, named by its base name inside the archive. Links
// and other special files are refused.
func collect(dir string) ([]entry, error) {
	base := filepath.Base(filepath.Clean(dir))
	ents := []entry{{name: base + "/", dir: true, mode: 0o755}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		name := path.Join(base, filepath.ToSlash(rel))
		switch {
		case d.IsDir():
			ents = append(ents, entry{name: name + "/", dir: true, mode: 0o755})
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			mode := fs.FileMode(0o644)
			if b := path.Base(name); b == "cernly" || b == "cernly.exe" {
				mode = 0o755
			}
			ents = append(ents, entry{name: name, mode: mode, data: data})
		default:
			return fmt.Errorf("%s: not a regular file", p)
		}
		return nil
	})
	sort.Slice(ents, func(i, j int) bool { return ents[i].name < ents[j].name })
	return ents, err
}

func writeTarGz(w io.Writer, ents []entry, at time.Time) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode), ModTime: at, Format: tar.FormatUSTAR, Typeflag: tar.TypeReg, Size: int64(len(e.data))}
		if e.dir {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(e.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(w io.Writer, ents []entry, at time.Time) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: at}
		h.SetMode(e.mode)
		if e.dir {
			h.Method = zip.Store
			h.SetMode(fs.ModeDir | e.mode)
		}
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := f.Write(e.data); err != nil {
			return err
		}
	}
	return zw.Close()
}
