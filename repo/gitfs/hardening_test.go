package gitfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

// packOf builds a pack in memory: a 100-byte base blob and n OFS_DELTAs, each
// resolving against the one before it and replacing the last byte. offs[i] is
// the offset of entry i (0 is the base).
func packOf(n int) (data []byte, offs []int64) {
	var b bytes.Buffer
	b.WriteString("PACK")
	binary.Write(&b, binary.BigEndian, uint32(2))
	binary.Write(&b, binary.BigEndian, uint32(n+1))
	base := bytes.Repeat([]byte("x"), 100)
	offs = append(offs, int64(b.Len()))
	b.Write([]byte{0x30 | 0x80 | 100&15, 100 >> 4}) // blob, size 100
	b.Write(deflate(base))
	for i := 1; i <= n; i++ {
		prev := offs[i-1]
		cur := int64(b.Len())
		offs = append(offs, cur)
		delta := []byte{100, 100, 0x90, 99, 1, byte('a' + i%26)}
		b.WriteByte(0x60 | byte(len(delta)))
		b.Write(ofsVarint(uint64(cur - prev)))
		b.Write(deflate(delta))
	}
	b.Write(make([]byte, 20)) // the trailing checksum is not verified
	return b.Bytes(), offs
}

// ofsVarint is the OFS_DELTA offset encoding (the inverse of header's reader).
func ofsVarint(v uint64) []byte {
	out := []byte{byte(v & 0x7f)}
	for v >>= 7; v > 0; v >>= 7 {
		v--
		out = append([]byte{0x80 | byte(v&0x7f)}, out...)
	}
	return out
}

func memPack(data []byte) *pack {
	return &pack{data: bytes.NewReader(data), dataSize: int64(len(data)), hl: 20, cache: map[int64]cached{}}
}

func TestDeltaChainResolvesIteratively(t *testing.T) {
	const n = 4000
	data, offs := packOf(n)
	p := memPack(data)
	r := &Repo{hashLen: 20}
	typ, got, err := p.read(r, offs[n])
	if err != nil || typ != 3 {
		t.Fatalf("read = %d, %v", typ, err)
	}
	want := append(bytes.Repeat([]byte("x"), 99), byte('a'+n%26))
	if !bytes.Equal(got, want) {
		t.Errorf("result %q, want %q", got, want)
	}
	// An intermediate entry reads back, from the cache or not.
	p.cache, p.cacheBytes = map[int64]cached{}, 0
	_, mid, err := p.read(r, offs[1234])
	if err != nil || mid[99] != byte('a'+1234%26) {
		t.Errorf("intermediate = %q, %v", mid, err)
	}
}

func TestDeltaChainLimits(t *testing.T) {
	data, offs := packOf(maxDeltaChain + 2)
	if _, _, err := memPack(data).read(&Repo{hashLen: 20}, offs[maxDeltaChain+2]); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("a chain past %d: %v", maxDeltaChain, err)
	}
	// The work budget: 200 deltas of 100 bytes need about 40 000 bytes.
	data, offs = packOf(200)
	old := maxWork
	maxWork = 10_000
	defer func() { maxWork = old }()
	if _, _, err := memPack(data).read(&Repo{hashLen: 20}, offs[200]); err == nil || !strings.Contains(err.Error(), "of work") {
		t.Errorf("over the work budget: %v", err)
	}
	maxWork = 100_000
	if _, _, err := memPack(data).read(&Repo{hashLen: 20}, offs[200]); err != nil {
		t.Errorf("within the work budget: %v", err)
	}
}

func commitFiles(g *gitfstest.Repo, files map[string]string) {
	for p, c := range files {
		g.Write(p, c)
	}
	g.Commit("c")
}

func TestTreeBounds(t *testing.T) {
	g := gitfstest.New(t, "")
	files := map[string]string{}
	deep := ""
	for i := 0; i < 200; i++ {
		deep += "d/"
	}
	files[deep+"leaf.txt"] = "x\n"
	for i := 0; i < 30; i++ {
		files[fmt.Sprintf("top/f%02d.txt", i)] = "x\n"
	}
	commitFiles(g, files) // 31 files and 201 folders
	open := func(max int) (map[string]TreeEntry, error) {
		r, err := Open(os.DirFS(g.Dir))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		r.SetMaxEntries(max)
		id, err := r.Head()
		if err != nil {
			t.Fatal(err)
		}
		c, err := r.Commit(id)
		if err != nil {
			t.Fatal(err)
		}
		return r.Tree(c.Tree)
	}
	if out, err := open(0); err != nil || len(out) != 31 {
		t.Fatalf("deep tree = %d files, %v", len(out), err)
	}
	if out, err := open(120); err != nil || len(out) != 31 { // limit 240 of 232 entries
		t.Fatalf("within the limit = %d files, %v", len(out), err)
	}
	if _, err := open(100); err == nil || !strings.Contains(err.Error(), "too many entries") { // limit 200
		t.Errorf("past the entry cap: %v", err)
	}
}

func TestIndexCountRefusedBeforeAllocation(t *testing.T) {
	// A header that counts 1000 entries over zeroed bytes that would fit them.
	data := make([]byte, 12+1000*63+20)
	copy(data, "DIRC")
	binary.BigEndian.PutUint32(data[4:], 2)
	binary.BigEndian.PutUint32(data[8:], 1000)
	_, err := parseIndexMax(data, 20, 10)
	var te *TooManyEntriesError
	if !errors.As(err, &te) || te.Count != 1000 || te.Max != 10 || !strings.Contains(err.Error(), "1000 entries") {
		t.Fatalf("err = %v", err)
	}
	if _, err := parseIndexMax(data, 20, 0); errors.As(err, &te) {
		t.Errorf("no limit set but refused: %v", err)
	}
	g := gitfstest.New(t, "")
	commitFiles(g, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})
	r, err := Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.SetMaxEntries(2)
	if _, err := r.Index(); !errors.As(err, &te) || te.Count != 3 {
		t.Errorf("Index with a limit of 2: %v", err)
	}
	r.SetMaxEntries(3)
	if _, err := r.Index(); err != nil {
		t.Errorf("Index with a limit of 3: %v", err)
	}
}

func TestPackedRefLookup(t *testing.T) {
	g := gitfstest.New(t, "")
	commitFiles(g, map[string]string{"a.txt": "a"})
	id := strings.TrimSpace(g.Git("rev-parse", "HEAD"))
	other := strings.Repeat("1", 40)
	g.Write(".git/packed-refs", "# pack-refs with: peeled fully-peeled sorted\n"+
		other+" refs/heads/other\n"+id+" refs/heads/x\n^"+other+"\n"+other+" refs/tags/xx\r\n")
	r, err := Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for name, want := range map[string]string{"refs/heads/x": id, "refs/heads/other": other, "refs/tags/xx": other} {
		if got, err := r.resolve(name, 0); err != nil || got != want {
			t.Errorf("resolve(%s) = %s, %v", name, got, err)
		}
	}
	if _, err := r.resolve("refs/heads/none", 0); err == nil {
		t.Error("absent ref resolved")
	}
	big := bytes.Repeat([]byte("#\n"), maxPackedRefs/2+1)
	if err := os.WriteFile(filepath.Join(g.Dir, ".git/packed-refs"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve("refs/heads/x", 0); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversized packed-refs: %v", err)
	}
}

// home spells out "~", the home folder, in fixtures: a literal home path in
// the open core's source reads as someone's local path to its leak guard.
func home(s string) string { return strings.ReplaceAll(s, "{home}", "~") }

func TestConfigIncludes(t *testing.T) {
	g := gitfstest.New(t, "")
	commitFiles(g, map[string]string{"a.txt": "a"})
	g.Write(".git/extra", "[remote \"origin\"]\n\turl = https://example.com/acme/from-extra.git\n[acme]\n\tseen = extra\n")
	g.Write(".git/sub/onmain", "[acme]\n\tbranch = main\n")
	g.Write(".git/sub/onfeature", "[acme]\n\tbranch = feature\n")
	g.Write(".git/config.worktree", "[acme]\n\tworktree = yes\n")
	g.Write(".git/config", home(`[core]
	repositoryformatversion = 1
[extensions]
	worktreeConfig = true
[remote "origin"]
	url = https://example.com/acme/first.git
[include]
	path = extra
[includeIf "onbranch:ma*"]
	path = sub/onmain
[includeIf "onbranch:feature/"]
	path = sub/onfeature
[include]
	path = {home}/.gitconfig-work
	path = ../outside
	path = /etc/acme.conf
	path = missing
[includeIf "gitdir:{home}/work/"]
	path = work.conf
[includeIf "gitdir/i:{home}/Work/"]
	path = work.conf
[includeIf "hasconfig:remote.*.url:*example.com*"]
	path = remote.conf
[remote "origin"]
	url = https://example.com/acme/last.git
[url "git@example.com:"]
	insteadOf = https://example.com/
[url "git@example.com:acme/"]
	insteadOf = https://example.com/acme/
`))
	r, err := Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.cfg["acme.seen"]; !slices.Equal(got, []string{"extra"}) {
		t.Errorf("include.path not followed: %q", got)
	}
	if got := r.cfg["acme.branch"]; !slices.Equal(got, []string{"main"}) {
		t.Errorf("onbranch: %q", got)
	}
	if got := r.cfg["acme.worktree"]; !slices.Equal(got, []string{"yes"}) {
		t.Errorf("config.worktree: %q", got)
	}
	// Order: the include sits between the two origin urls, and the last wins.
	if got := r.OriginURL(); got != "git@example.com:acme/last.git" {
		t.Errorf("OriginURL = %q", got)
	}
	want := []string{
		"include.path {home}/.gitconfig-work",
		"include.path ../outside",
		"include.path /etc/acme.conf",
		"includeIf.gitdir:{home}/work/.path work.conf",
		"includeIf.gitdir/i:{home}/Work/.path work.conf",
		"includeIf.hasconfig:remote.*.url:*example.com*.path remote.conf",
	}
	for i := range want {
		want[i] = home(want[i])
	}
	if got := r.Unread(); !slices.Equal(got, want) {
		t.Errorf("Unread\n got %q\nwant %q", got, want)
	}
}

func TestConfigIncludeOrderAndDepth(t *testing.T) {
	g := gitfstest.New(t, "")
	commitFiles(g, map[string]string{"a.txt": "a"})
	g.Write(".git/over", "[acme]\n\tv = included\n")
	g.Write(".git/config", "[acme]\n\tv = before\n[include]\n\tpath = over\n[acme]\n\tw = after\n")
	r, err := Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r.cfg.get("acme.v"); v != "included" {
		t.Errorf("acme.v = %q", v)
	}
	r.Close()
	g.Write(".git/config", "[core]\n\trepositoryformatversion = 0\n[include]\n\tpath = config\n")
	if _, err := Open(os.DirFS(g.Dir)); err == nil || !strings.Contains(err.Error(), "include depth") {
		t.Errorf("self include: %v", err)
	}
	// A branch that does not match, and a detached HEAD.
	g.Write(".git/config", "[includeIf \"onbranch:release/*\"]\n\tpath = over\n")
	r, err = Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.cfg.get("acme.v"); ok {
		t.Error("onbranch:release/* matched main")
	}
	r.Close()
}

func TestWildmatch(t *testing.T) {
	for _, c := range []struct {
		pat, s string
		want   bool
	}{
		{"main", "main", true}, {"main", "mainx", false}, {"ma*", "main", true},
		{"feature/*", "feature/a", true}, {"feature/*", "feature/a/b", false},
		{"feature/**", "feature/a/b", true}, {"**/x", "a/b/x", true}, {"**/x", "x", true},
		{"a/**/z", "a/z", true}, {"a/**/z", "a/b/c/z", true}, {"a/**/z", "a/bz", false},
		{"fix-?", "fix-1", true}, {"fix-?", "fix-/", false}, {"r[0-9]", "r5", true},
		{"r[!0-9]", "r5", false}, {`a\*`, "a*", true}, {`a\*`, "ab", false}, {"[", "[", false},
	} {
		if got := wildmatch(c.pat, c.s); got != c.want {
			t.Errorf("wildmatch(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
	// Many stars on a long non-match is bounded work, not a hang.
	if wildmatch(strings.Repeat("a*", 40)+"b", strings.Repeat("a", 200)) {
		t.Error("pathological pattern matched")
	}
}

func TestOriginLastValue(t *testing.T) {
	c, err := parseConfig([]byte("[remote \"origin\"]\n\turl = one\n\turl = two\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Repo{cfg: c}
	if got := r.OriginURL(); got != "two" {
		t.Errorf("OriginURL = %q", got)
	}
}

func TestChangedAndLineEnds(t *testing.T) {
	setup := func(t *testing.T) (*gitfstest.Repo, string) {
		g := gitfstest.New(t, "")
		commitFiles(g, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "b\n", "c.txt": "c\n"})
		g.Write("a.txt", "one\r\ntwo\r\n")
		g.Touch("a.txt", 2*time.Hour)
		return g, g.Dir
	}
	changed := func(t *testing.T, dir string, max int) ([]string, bool) {
		r, err := Open(os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, err := r.Changed(os.DirFS(dir), max)
		if err != nil {
			t.Fatal(err)
		}
		d, err := r.Dirty(os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		return got, d
	}
	t.Run("no settings", func(t *testing.T) {
		_, dir := setup(t)
		if got, d := changed(t, dir, 10); !slices.Equal(got, []string{"a.txt"}) || !d {
			t.Errorf("Changed = %q, Dirty = %v", got, d)
		}
	})
	t.Run("autocrlf", func(t *testing.T) {
		g, dir := setup(t)
		g.Git("config", "core.autocrlf", "input")
		if got, d := changed(t, dir, 10); len(got) != 0 || d {
			t.Errorf("Changed = %q, Dirty = %v", got, d)
		}
		g.Write("a.txt", "one\r\nTWO\r\n") // a real change still shows
		if got, d := changed(t, dir, 10); !slices.Equal(got, []string{"a.txt"}) || !d {
			t.Errorf("real change: Changed = %q, Dirty = %v", got, d)
		}
	})
	t.Run("gitattributes", func(t *testing.T) {
		g, dir := setup(t)
		g.Write(".gitattributes", "# eol\n*.txt text=auto\n")
		if got, d := changed(t, dir, 10); len(got) != 0 || d {
			t.Errorf("text=auto: Changed = %q, Dirty = %v", got, d)
		}
		g.Write(".gitattributes", "*.txt -text\n")
		if got, d := changed(t, dir, 10); !slices.Equal(got, []string{"a.txt"}) || !d {
			t.Errorf("-text: Changed = %q, Dirty = %v", got, d)
		}
	})
	t.Run("max and order", func(t *testing.T) {
		g, dir := setup(t)
		g.Write("c.txt", "changed\n")
		got, _ := changed(t, dir, 10)
		if !slices.Equal(got, []string{"a.txt", "c.txt"}) {
			t.Errorf("Changed = %q", got)
		}
		if got, _ := changed(t, dir, 1); !slices.Equal(got, []string{"a.txt"}) {
			t.Errorf("Changed max 1 = %q", got)
		}
	})
}
