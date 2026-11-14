package repo

import "testing"

// Memo computes once per view; a new view starts empty, so nothing cached
// outlives a scan.
func TestMemoPerView(t *testing.T) {
	var a, b Repo
	n := 0
	f := func() any { n++; return n }
	if a.Memo("k", f) != 1 || a.Memo("k", f) != 1 {
		t.Error("a: computed twice")
	}
	if b.Memo("k", f) != 2 {
		t.Error("b: saw a's value")
	}
}

// MemoFile reads once per path and content, and keeps result types apart.
func TestMemoFile(t *testing.T) {
	var r Repo
	n := 0
	read := func() (int, error) { n++; return n, nil }
	for range 2 {
		if v, _ := MemoFile(&r, "a.go", []byte("x"), read); v != 1 {
			t.Errorf("same file and content read again: %d", v)
		}
	}
	MemoFile(&r, "a.go", []byte("y"), read)
	MemoFile(&r, "b.go", []byte("x"), read)
	if n != 3 {
		t.Errorf("read %d times, want 3", n)
	}
	if s, _ := MemoFile(&r, "a.go", []byte("x"), func() (string, error) { return "s", nil }); s != "s" {
		t.Errorf("a result of another type came back: %q", s)
	}
}
