package gitfs

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cernly/cernly/repo/gitfs/gitfstest"
)

func TestConfig(t *testing.T) {
	c, err := parseConfig([]byte(`# comment
[Core]
	Bare = false ; trailing
	repositoryformatversion = 1
[remote "Origin"]
	URL = "https://example.com/acme/app.git"
	url = second
	odd = a \" b \\ c\n\td # not part
	spaced =   keep  inner   spaces  
	flag
	long = one \
two
[url "ssh://git@example.com/"]
	insteadOf = ex:
[Sub.Legacy]
	k = v
[x] y = z
`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]string{
		"core.bare":                            {"false"},
		"core.repositoryformatversion":         {"1"},
		"remote.Origin.url":                    {"https://example.com/acme/app.git", "second"},
		"remote.origin.url":                    nil, // the subsection is case-sensitive
		"remote.Origin.odd":                    {"a \" b \\ c\n\td"},
		"remote.Origin.spaced":                 {"keep  inner   spaces"},
		"remote.Origin.flag":                   {"true"},
		"remote.Origin.long":                   {"one two"},
		"url.ssh://git@example.com/.insteadof": {"ex:"},
		"sub.legacy.k":                         {"v"},
		"x.y":                                  {"z"},
	} {
		if !reflect.DeepEqual(c[key], want) && !(len(want) == 0 && len(c[key]) == 0) {
			t.Errorf("%s = %q, want %q", key, c[key], want)
		}
	}
	for _, bad := range []string{"[core\nx=1", "x = 1\n", "[a]\nb = \"open\n", "[a]\nb = \\q\n"} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("config %q parsed", bad)
		}
	}
}

func TestOffsetVarint(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want uint64
	}{
		{[]byte{0}, 0}, {[]byte{0x7f}, 127}, {[]byte{0x80, 0x00}, 128},
		{[]byte{0xff, 0x7f}, 16511}, {[]byte{0x80, 0x80, 0x00}, 16512},
	} {
		got, n, err := offsetVarint(append(c.in, 'x'))
		if err != nil || got != c.want || n != len(c.in) {
			t.Errorf("offsetVarint(%x) = %d, %d, %v; want %d, %d", c.in, got, n, err, c.want, len(c.in))
		}
	}
	if _, _, err := offsetVarint([]byte{0x80}); err == nil {
		t.Error("truncated varint read")
	}
}

func TestEWAH(t *testing.T) {
	words := []uint64{
		1 | 1<<1 | 1<<33, // one run word of ones, one literal
		0b101,            // bits 64 and 66
		0 | 1<<1 | 1<<33, // one run word of zeros, one literal
		1 << 63,          // bit 255
	}
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(256))
	binary.Write(&b, binary.BigEndian, uint32(len(words)))
	for _, w := range words {
		binary.Write(&b, binary.BigEndian, w)
	}
	binary.Write(&b, binary.BigEndian, uint32(2))
	b.WriteString("rest")
	set := map[int]bool{64: true, 66: true, 255: true}
	for i := 0; i < 64; i++ {
		set[i] = true
	}
	for _, limit := range []int{300, 100} {
		bs, n, err := readEWAH(b.Bytes(), limit)
		if err != nil || n != b.Len()-4 {
			t.Fatalf("readEWAH = %d, %v", n, err)
		}
		for i := 0; i < 320; i++ {
			if want := set[i] && i < limit; bs.has(i) != want {
				t.Errorf("limit %d: bit %d = %v, want %v", limit, i, bs.has(i), want)
			}
		}
	}
	for _, bad := range [][]byte{nil, b.Bytes()[:20]} {
		if _, _, err := readEWAH(bad, 10); err == nil {
			t.Error("truncated bitmap read")
		}
	}
}

func TestApplyDelta(t *testing.T) {
	base := []byte("hello world\n")
	good := []byte{12, 12, 0x90, 6, 6, 't', 'h', 'e', 'r', 'e', '\n'}
	if got, err := applyDelta(base, good); err != nil || string(got) != "hello there\n" {
		t.Errorf("applyDelta = %q, %v", got, err)
	}
	for name, bad := range map[string][]byte{
		"wrong base size": {11, 12, 0x90, 6, 6, 't', 'h', 'e', 'r', 'e', '\n'},
		"copy past base":  {12, 12, 0x91, 10, 6, 6, 't', 'h', 'e', 'r', 'e', '\n'},
		"insert past end": {12, 12, 0x90, 6, 9, 't'},
		"zero opcode":     {12, 12, 0x90, 6, 0},
		"short result":    {12, 12, 0x90, 6},
		"huge target":     {12, 0xff, 0xff, 0xff, 0xff, 0x0f},
	} {
		if _, err := applyDelta(base, bad); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
}

func deflate(b []byte) []byte {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(b)
	w.Close()
	return z.Bytes()
}

// A REF_DELTA in a pack, which git writes only in thin packs, built by hand.
func TestRefDelta(t *testing.T) {
	g := gitfstest.New(t, "")
	base, target := "hello world\n", "hello there\n"
	blobID := func(s string) []byte {
		h := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(s), s)))
		return h[:]
	}
	delta := []byte{12, 12, 0x90, 6, 6, 't', 'h', 'e', 'r', 'e', '\n'}
	var pack bytes.Buffer
	pack.WriteString("PACK")
	binary.Write(&pack, binary.BigEndian, uint32(2))
	binary.Write(&pack, binary.BigEndian, uint32(2))
	pack.WriteByte(0x30 | byte(len(base)))
	pack.Write(deflate([]byte(base)))
	pack.WriteByte(0x70 | byte(len(delta)))
	pack.Write(blobID(base))
	pack.Write(deflate(delta))
	sum := sha1.Sum(pack.Bytes())
	pack.Write(sum[:])
	dir := filepath.Join(g.Dir, ".git/objects/pack")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "tmp.pack")
	os.WriteFile(p, pack.Bytes(), 0o644)
	g.Git("index-pack", p)
	r, err := Open(os.DirFS(g.Dir))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, s := range []string{base, target} {
		id := fmt.Sprintf("%x", blobID(s))
		typ, data, err := r.object(id)
		if err != nil || typ != "blob" || string(data) != s {
			t.Errorf("object(%s) = %s, %q, %v", id, typ, data, err)
		}
	}
	if _, _, err := r.object(fmt.Sprintf("%x", blobID("absent"))); err == nil {
		t.Error("absent object read")
	}
}

// Every commit and tree in the repository reads back as git has it.
func TestObjectsMatchGit(t *testing.T) {
	for _, name := range []string{"loose", "packed", "sha256-packed"} {
		for _, list := range [][]gitfstest.Case{gitfstest.Cases, gitfstest.SHA256Cases} {
			for _, c := range list {
				if c.Name != name {
					continue
				}
				t.Run(name, func(t *testing.T) {
					format := "sha1"
					if name == "sha256-packed" {
						format = "sha256"
					}
					g := gitfstest.New(t, format)
					c.Setup(g)
					r, err := Open(os.DirFS(g.Dir))
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					n := 0
					for _, l := range splitLines(g.Git("cat-file", "--batch-all-objects", "--batch-check")) {
						var id, typ string
						var size int
						fmt.Sscanf(l, "%s %s %d", &id, &typ, &size)
						if typ != "commit" && typ != "tree" {
							continue
						}
						n++
						gt, data, err := r.object(id)
						if err != nil || gt != typ || string(data) != g.Git("cat-file", typ, id) {
							t.Fatalf("object %s (%s): %v, type %q", id, typ, err, gt)
						}
					}
					if n == 0 {
						t.Fatal("no objects compared")
					}
				})
			}
		}
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range bytes.Split([]byte(s), []byte("\n")) {
		if len(l) > 0 {
			out = append(out, string(l))
		}
	}
	return out
}

// A damaged index or pack index is an error, never a panic or a huge allocation.
func TestCorruptIndexNoPanic(t *testing.T) {
	for _, name := range []string{"loose", "index-v4", "split-index"} {
		for _, c := range gitfstest.Cases {
			if c.Name != name {
				continue
			}
			g := gitfstest.New(t, "")
			c.Setup(g)
			data, err := os.ReadFile(filepath.Join(g.Dir, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < len(data); n++ {
				parseIndex(data[:n], 20)
			}
			for i := 0; i < len(data); i++ {
				bad := append([]byte(nil), data...)
				bad[i] ^= 0xff
				parseIndex(bad, 20)
			}
		}
	}
}
