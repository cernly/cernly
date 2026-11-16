package gitfs

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

const (
	maxObject     = 64 << 20  // largest commit or tree read
	maxDeltaChain = 4095      // longest chain of deltas followed (git's deepest)
	maxWhole      = 256 << 20 // largest file read whole when the FS has no ReadAt
	cacheLimit    = 32 << 20  // bytes of decoded pack objects kept, all packs together
)

// maxWork bounds the bytes inflated or produced to resolve one packed object.
// A variable so a test can lower it.
var maxWork int64 = 1 << 30

// openAt opens name for random access. A file that is no io.ReaderAt is read
// whole, up to maxWhole.
func openAt(fsys fs.FS, name string) (io.ReaderAt, int64, io.Closer, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, 0, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, nil, err
	}
	if ra, ok := f.(io.ReaderAt); ok {
		return ra, fi.Size(), f, nil
	}
	defer f.Close()
	if fi.Size() > maxWhole {
		return nil, 0, nil, fmt.Errorf("%s: %d bytes, too large to read without random access", name, fi.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWhole+1))
	if err != nil {
		return nil, 0, nil, err
	}
	return bytes.NewReader(data), int64(len(data)), io.NopCloser(nil), nil
}

func readAt(ra io.ReaderAt, buf []byte, off int64) error {
	n, err := ra.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func (r *Repo) readFile(name string, max int64) ([]byte, error) {
	f, err := r.fsys.Open(name)
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

var errNoObject = errors.New("object not found")

// object returns the type ("commit", "tree", "blob", "tag") and content of an
// object, from a pack or a loose file.
func (r *Repo) object(id string) (string, []byte, error) {
	raw, err := r.rawID(id)
	if err != nil {
		return "", nil, err
	}
	p, off, ok, err := r.findPacked(raw)
	if err != nil {
		return "", nil, err
	}
	if ok {
		t, data, err := p.read(r, off)
		if err != nil {
			return "", nil, fmt.Errorf("object %s: %w", id, err)
		}
		return typeName(t), data, nil
	}
	return r.loose(id)
}

// findPacked looks an id up in every pack.
func (r *Repo) findPacked(raw []byte) (*pack, int64, bool, error) {
	if err := r.loadPacks(); err != nil {
		return nil, 0, false, err
	}
	for _, p := range r.packs {
		off, ok, err := p.find(raw)
		if err != nil {
			return nil, 0, false, err
		}
		if ok {
			return p, off, true, nil
		}
	}
	return nil, 0, false, nil
}

func (r *Repo) rawID(id string) ([]byte, error) {
	if len(id) != 2*r.hashLen {
		return nil, fmt.Errorf("object id %q: want %d hex digits", id, 2*r.hashLen)
	}
	raw, err := hex.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("object id %q: not hex", id)
	}
	return raw, nil
}

func typeName(t int) string {
	switch t {
	case 1:
		return "commit"
	case 2:
		return "tree"
	case 3:
		return "blob"
	case 4:
		return "tag"
	}
	return ""
}

func (r *Repo) loose(id string) (string, []byte, error) {
	f, err := r.fsys.Open(path.Join(".git/objects", id[:2], id[2:]))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("%w: %s", errNoObject, id)
		}
		return "", nil, err
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("object %s: %w", id, err)
	}
	defer zr.Close()
	br := bufio.NewReaderSize(zr, 4096)
	hdr, err := br.ReadSlice(0)
	if err != nil {
		return "", nil, fmt.Errorf("object %s: bad header", id)
	}
	typ, sz, ok := strings.Cut(string(hdr[:len(hdr)-1]), " ")
	size, perr := strconv.ParseInt(sz, 10, 64)
	if !ok || perr != nil || size < 0 {
		return "", nil, fmt.Errorf("object %s: bad header", id)
	}
	if size > maxObject {
		return "", nil, fmt.Errorf("object %s: %d bytes, over the %d limit", id, size, maxObject)
	}
	data, err := io.ReadAll(io.LimitReader(br, size+1))
	if err != nil {
		return "", nil, fmt.Errorf("object %s: %w", id, err)
	}
	if int64(len(data)) != size {
		return "", nil, fmt.Errorf("object %s: size does not match its header", id)
	}
	return typ, data, nil
}

// pack is one .pack with its v2 .idx, both read with ReadAt.
type pack struct {
	idx, data         io.ReaderAt
	idxSize, dataSize int64
	closers           []io.Closer
	hl                int
	fan               [256]uint32
	n                 uint32
	cache             map[int64]cached
	cacheBytes        int
	budget            *cacheBudget
}

// cacheBudget is the decode cache all packs of one repository share, so the
// number of packs does not multiply cacheLimit.
type cacheBudget struct {
	used  int
	packs []*pack
}

type cached struct {
	typ  int
	data []byte
}

func (r *Repo) loadPacks() error {
	if r.packsLoaded {
		return nil
	}
	r.packsLoaded = true
	ents, err := fs.ReadDir(r.fsys, ".git/objects/pack")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".idx") {
			continue
		}
		p, err := openPack(r.fsys, ".git/objects/pack/"+strings.TrimSuffix(name, ".idx"), r.hashLen)
		if err != nil {
			return err
		}
		if r.cache == nil {
			r.cache = &cacheBudget{}
		}
		p.budget = r.cache
		r.cache.packs = append(r.cache.packs, p)
		r.packs = append(r.packs, p)
	}
	return nil
}

func openPack(fsys fs.FS, base string, hl int) (*pack, error) {
	p := &pack{hl: hl, cache: map[int64]cached{}}
	var err error
	var c io.Closer
	if p.idx, p.idxSize, c, err = openAt(fsys, base+".idx"); err != nil {
		return nil, err
	}
	p.closers = append(p.closers, c)
	if p.data, p.dataSize, c, err = openAt(fsys, base+".pack"); err != nil {
		p.close()
		return nil, err
	}
	p.closers = append(p.closers, c)
	fail := func(msg string) (*pack, error) {
		p.close()
		return nil, fmt.Errorf("%s: %s", base, msg)
	}
	var hdr [8 + 1024]byte
	if p.idxSize < int64(len(hdr)) {
		return fail("idx too short")
	}
	if err := readAt(p.idx, hdr[:], 0); err != nil {
		p.close()
		return nil, err
	}
	if string(hdr[:4]) != "\377tOc" || binary.BigEndian.Uint32(hdr[4:8]) != 2 {
		return fail("only version 2 pack indexes are supported")
	}
	for i := range p.fan {
		p.fan[i] = binary.BigEndian.Uint32(hdr[8+4*i:])
	}
	p.n = p.fan[255]
	if p.idxSize < int64(len(hdr))+int64(p.n)*int64(hl+8) {
		return fail("idx too short for its object count")
	}
	var ph [12]byte
	if p.dataSize < 12+int64(hl) {
		return fail("pack too short")
	}
	if err := readAt(p.data, ph[:], 0); err != nil {
		p.close()
		return nil, err
	}
	if v := binary.BigEndian.Uint32(ph[4:8]); string(ph[:4]) != "PACK" || (v != 2 && v != 3) {
		return fail("not a version 2 or 3 pack")
	}
	return p, nil
}

func (p *pack) close() {
	for _, c := range p.closers {
		if c != nil {
			c.Close()
		}
	}
}

// find looks an id up in the idx: fan-out table, then binary search.
func (p *pack) find(id []byte) (int64, bool, error) {
	first := int(id[0])
	lo, hi := uint32(0), p.fan[first]
	if first > 0 {
		lo = p.fan[first-1]
	}
	if lo > hi || hi > p.n {
		return 0, false, errors.New("corrupt pack index fan-out")
	}
	names := int64(8 + 1024)
	buf := make([]byte, p.hl)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if err := readAt(p.idx, buf, names+int64(mid)*int64(p.hl)); err != nil {
			return 0, false, err
		}
		switch c := bytes.Compare(buf, id); {
		case c == 0:
			offs := names + int64(p.n)*int64(p.hl+4)
			var w [8]byte
			if err := readAt(p.idx, w[:4], offs+int64(mid)*4); err != nil {
				return 0, false, err
			}
			v := binary.BigEndian.Uint32(w[:4])
			if v&0x80000000 == 0 {
				return int64(v), true, nil
			}
			large := offs + int64(p.n)*4 + int64(v&0x7fffffff)*8
			if large+8 > p.idxSize {
				return 0, false, errors.New("corrupt pack index: large offset out of range")
			}
			if err := readAt(p.idx, w[:], large); err != nil {
				return 0, false, err
			}
			return int64(binary.BigEndian.Uint64(w[:]) & (1<<63 - 1)), true, nil
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false, nil
}

// packHeader is the header of one pack entry: a whole object (typ 1-4), an
// OFS_DELTA (6, base at baseOff) or a REF_DELTA (7, base is baseID).
type packHeader struct {
	typ     int
	size    int64 // inflated size of the entry's own data
	pos     int64 // where its zlib stream starts
	baseOff int64
	baseID  []byte
}

// header parses the entry at off without inflating anything.
func (p *pack) header(off int64) (packHeader, error) {
	var h packHeader
	if off < 12 || off >= p.dataSize-int64(p.hl) {
		return h, errors.New("pack offset out of range")
	}
	var hb [64]byte
	n := int64(len(hb))
	if off+n > p.dataSize {
		n = p.dataSize - off
	}
	if err := readAt(p.data, hb[:n], off); err != nil {
		return h, err
	}
	i := 0
	c := hb[i]
	i++
	h.typ = int(c>>4) & 7
	size := uint64(c & 15)
	shift := uint(4)
	for c&0x80 != 0 {
		if i >= int(n) || shift > 56 {
			return h, errors.New("bad pack object header")
		}
		c = hb[i]
		i++
		size |= uint64(c&0x7f) << shift
		shift += 7
	}
	if size > maxObject {
		return h, fmt.Errorf("object of %d bytes, over the %d limit", size, maxObject)
	}
	h.size = int64(size)
	h.pos = off + int64(i)
	switch h.typ {
	case 1, 2, 3, 4:
	case 6:
		if i >= int(n) {
			return h, errors.New("bad pack object header")
		}
		c = hb[i]
		i++
		rel := int64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= int(n) {
				return h, errors.New("bad pack object header")
			}
			c = hb[i]
			i++
			rel = (rel+1)<<7 | int64(c&0x7f)
			if rel < 0 {
				return h, errors.New("bad delta offset")
			}
		}
		h.pos = off + int64(i)
		if rel <= 0 || rel > off {
			return h, errors.New("bad delta offset")
		}
		h.baseOff = off - rel
	case 7:
		if i+p.hl > int(n) {
			return h, errors.New("bad pack object header")
		}
		h.baseID = append([]byte(nil), hb[i:i+p.hl]...)
		h.pos = off + int64(i+p.hl)
	default:
		return h, fmt.Errorf("bad pack object type %d", h.typ)
	}
	return h, nil
}

// deltaLink is one delta of a chain, with the pack that holds it.
type deltaLink struct {
	p   *pack
	off int64
	h   packHeader
}

func typeNumber(name string) int {
	return map[string]int{"commit": 1, "tree": 2, "blob": 3, "tag": 4}[name]
}

// put keeps a decoded object for later reads, within cacheLimit for all the
// repository's packs together: past it every pack's cache is emptied.
func (p *pack) put(off int64, typ int, data []byte) {
	if len(data) > cacheLimit {
		return
	}
	b := p.budget
	if b == nil { // a pack opened on its own, as in tests
		b = &cacheBudget{packs: []*pack{p}}
		p.budget = b
	}
	if b.used+len(data) > cacheLimit {
		for _, q := range b.packs {
			q.cache, q.cacheBytes = map[int64]cached{}, 0
		}
		b.used = 0
	}
	p.cache[off] = cached{typ, data}
	p.cacheBytes += len(data)
	b.used += len(data)
}

// read decodes the object at off, resolving deltas without recursion. It first
// walks the chain reading only headers, then inflates the base and applies
// each delta in turn, so only the base, one delta and the result are live at
// once, and the bytes inflated for one object stay within maxWork.
func (p *pack) read(r *Repo, off int64) (int, []byte, error) {
	var chain []deltaLink // outermost delta first
	var full *deltaLink   // the whole object that ends the chain
	var base []byte
	baseTyp := 0
	cur, curOff := p, off
walk:
	for {
		if c, ok := cur.cache[curOff]; ok {
			baseTyp, base = c.typ, c.data
			break
		}
		h, err := cur.header(curOff)
		if err != nil {
			return 0, nil, err
		}
		switch h.typ {
		case 6:
			chain = append(chain, deltaLink{cur, curOff, h})
			curOff = h.baseOff
		case 7:
			chain = append(chain, deltaLink{cur, curOff, h})
			bp, boff, ok, err := r.findPacked(h.baseID)
			if err != nil {
				return 0, nil, err
			}
			if ok {
				cur, curOff = bp, boff
				break
			}
			id := hex.EncodeToString(h.baseID)
			t, data, err := r.loose(id)
			if err != nil {
				return 0, nil, err
			}
			if baseTyp = typeNumber(t); baseTyp == 0 {
				return 0, nil, fmt.Errorf("delta base %s is a %q object", id, t)
			}
			base = data
			break walk
		default:
			full = &deltaLink{cur, curOff, h}
			baseTyp = h.typ
			break walk
		}
		if len(chain) > maxDeltaChain {
			return 0, nil, errors.New("delta chain too long")
		}
	}
	var work int64
	spend := func(n int) error {
		if work += int64(n); work > maxWork {
			return fmt.Errorf("delta chain needs more than %d bytes of work", maxWork)
		}
		return nil
	}
	if full != nil {
		if err := spend(int(full.h.size)); err != nil {
			return 0, nil, err
		}
		data, err := full.p.inflate(full.h.pos, full.h.size)
		if err != nil {
			return 0, nil, err
		}
		base = data
		full.p.put(full.off, baseTyp, base)
	} else if len(chain) > 0 {
		if err := spend(len(base)); err != nil {
			return 0, nil, err
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		l := chain[i]
		if err := spend(int(l.h.size)); err != nil {
			return 0, nil, err
		}
		delta, err := l.p.inflate(l.h.pos, l.h.size)
		if err != nil {
			return 0, nil, err
		}
		out, err := applyDelta(base, delta)
		if err != nil {
			return 0, nil, err
		}
		if err := spend(len(out)); err != nil {
			return 0, nil, err
		}
		base = out // the previous base and the delta are garbage now
		l.p.put(l.off, baseTyp, base)
	}
	return baseTyp, base, nil
}

func (p *pack) inflate(pos, size int64) ([]byte, error) {
	end := p.dataSize - int64(p.hl)
	zr, err := zlib.NewReader(io.NewSectionReader(p.data, pos, end-pos))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errors.New("inflated size does not match the pack header")
	}
	return data, nil
}

func deltaVarint(d []byte, i *int) (uint64, error) {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if *i >= len(d) || shift > 56 {
			return 0, errors.New("bad delta header")
		}
		c := d[*i]
		*i++
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, nil
		}
	}
}

// applyDelta runs git's delta instructions (copy from base, insert literal).
func applyDelta(base, d []byte) ([]byte, error) {
	i := 0
	src, err := deltaVarint(d, &i)
	if err != nil {
		return nil, err
	}
	dst, err := deltaVarint(d, &i)
	if err != nil {
		return nil, err
	}
	if src != uint64(len(base)) {
		return nil, errors.New("delta does not fit its base")
	}
	if dst > maxObject {
		return nil, errors.New("delta result over the size limit")
	}
	out := make([]byte, 0, dst)
	for i < len(d) {
		op := d[i]
		i++
		switch {
		case op&0x80 != 0:
			var off, sz uint64
			for b := uint(0); b < 4; b++ {
				if op&(1<<b) != 0 {
					if i >= len(d) {
						return nil, errors.New("bad delta copy")
					}
					off |= uint64(d[i]) << (8 * b)
					i++
				}
			}
			for b := uint(0); b < 3; b++ {
				if op&(0x10<<b) != 0 {
					if i >= len(d) {
						return nil, errors.New("bad delta copy")
					}
					sz |= uint64(d[i]) << (8 * b)
					i++
				}
			}
			if sz == 0 {
				sz = 0x10000
			}
			if off+sz > uint64(len(base)) || uint64(len(out))+sz > dst {
				return nil, errors.New("delta copy out of range")
			}
			out = append(out, base[off:off+sz]...)
		case op != 0:
			l := int(op)
			if i+l > len(d) || uint64(len(out))+uint64(l) > dst {
				return nil, errors.New("bad delta insert")
			}
			out = append(out, d[i:i+l]...)
			i += l
		default:
			return nil, errors.New("bad delta instruction")
		}
	}
	if uint64(len(out)) != dst {
		return nil, errors.New("delta result has the wrong size")
	}
	return out, nil
}

// TreeEntry is one non-directory entry of a recursive tree listing.
type TreeEntry struct {
	Mode uint32
	ID   string
}

const (
	maxTreeDepth       = 1024
	defaultTreeEntries = 1_000_000 // files and folders visited, without a limit set
	maxTreeBytes       = 512 << 20 // tree bytes inflated by one walk
)

// Tree lists every non-directory entry below a tree, by slash path. A walk is
// bounded: it visits at most 2*maxEntries entries (1 000 000 without a limit),
// files and folders together, and inflates at most maxTreeBytes of trees.
func (r *Repo) Tree(id string) (map[string]TreeEntry, error) {
	w := &treeWalk{r: r, out: map[string]TreeEntry{}, limit: defaultTreeEntries}
	if r.maxEntries > 0 {
		w.limit = 2 * r.maxEntries
	}
	if err := w.walk(id, "", 0); err != nil {
		return nil, err
	}
	return w.out, nil
}

type treeWalk struct {
	r       *Repo
	out     map[string]TreeEntry
	entries int
	limit   int
	bytes   int64
}

type treeDir struct{ name, id string }

func (w *treeWalk) walk(id, prefix string, depth int) error {
	if depth > maxTreeDepth {
		return errors.New("tree nested too deep")
	}
	dirs, err := w.read(id, prefix)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := w.walk(d.id, prefix+d.name+"/", depth+1); err != nil {
			return err
		}
	}
	return nil
}

// read parses one tree, adds its files to out and returns its folders; the
// tree's data is dropped before the caller descends.
func (w *treeWalk) read(id, prefix string) ([]treeDir, error) {
	typ, data, err := w.r.object(id)
	if err != nil {
		return nil, err
	}
	if typ != "tree" {
		return nil, fmt.Errorf("object %s is a %s, not a tree", id, typ)
	}
	if w.bytes += int64(len(data)); w.bytes > maxTreeBytes {
		return nil, fmt.Errorf("trees are over %d bytes in all", maxTreeBytes)
	}
	var dirs []treeDir
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp < 0 || nul < sp || len(data) < nul+1+w.r.hashLen {
			return nil, fmt.Errorf("tree %s: bad entry", id)
		}
		mode, err := strconv.ParseUint(string(data[:sp]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("tree %s: bad mode", id)
		}
		if w.entries++; w.entries > w.limit {
			return nil, errors.New("tree has too many entries")
		}
		name := string(data[sp+1 : nul])
		eid := hex.EncodeToString(data[nul+1 : nul+1+w.r.hashLen])
		data = data[nul+1+w.r.hashLen:]
		if mode == 0o40000 {
			dirs = append(dirs, treeDir{name, eid})
			continue
		}
		w.out[prefix+name] = TreeEntry{Mode: uint32(mode), ID: eid}
	}
	return dirs, nil
}
