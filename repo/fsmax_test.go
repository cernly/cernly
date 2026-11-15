package repo

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

func TestOpenFSMaxWithoutGit(t *testing.T) {
	fsys := fstest.MapFS{}
	for i := 0; i < 5; i++ {
		fsys[fmt.Sprintf("src/f%d.txt", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	var te *TooManyFilesError
	if _, err := OpenFSMax(fsys, "acme", 3); !errors.As(err, &te) || te.Max != 3 || te.Files < 4 {
		t.Fatalf("limit 3: %v", err)
	} else if !strings.Contains(err.Error(), "more than the 3 allowed") {
		t.Errorf("message %q", err)
	}
	for _, max := range []int{0, 5} {
		r, err := OpenFSMax(fsys, "acme", max)
		if err != nil || len(r.Files) != 5 {
			t.Fatalf("limit %d: %v", max, err)
		}
		r.Close()
	}
}

func TestOpenFSMaxWithGit(t *testing.T) {
	g := gitfstest.New(t, "")
	for i := 0; i < 4; i++ {
		g.Write(fmt.Sprintf("f%d.txt", i), "x\n")
	}
	g.Commit("c")
	var te *TooManyFilesError
	if _, err := OpenFSMax(os.DirFS(g.Dir), "acme", 3); !errors.As(err, &te) || te.Files != 4 || te.Max != 3 {
		t.Fatalf("limit 3: %v", err)
	}
	for _, max := range []int{0, 4} {
		r, err := OpenFSMax(os.DirFS(g.Dir), "acme", max)
		if err != nil || len(r.Files) != 4 {
			t.Fatalf("limit %d: %v", max, err)
		}
		// The limit does not break a dirty check: the head tree has a folder too.
		if d, err := r.Dirty(); err != nil || d {
			t.Errorf("limit %d: Dirty = %v, %v", max, d, err)
		}
		r.Close()
	}
}
