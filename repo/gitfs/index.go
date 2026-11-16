package gitfs

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// IndexEntry is one entry of .git/index.
type IndexEntry struct {
	Path         string
	Mode         uint32
	ID           string // hex object id
	Size         uint32 // file size at staging time, modulo 2^32
	MtimeSec     uint32
	MtimeNsec    uint32
	Stage        int // 0, or 1-3 in a merge conflict
	AssumeValid  bool
	SkipWorktree bool
	IntentToAdd  bool
}

// Index is the parsed .git/index, a split index already merged. Entries are
// sorted by path, then stage.
type Index struct {
	Version int
	Entries []IndexEntry
}

const maxIndexBytes = 128 << 20

// TooManyEntriesError is returned by Index when its header counts more
// entries than SetMaxEntries allows; nothing is allocated for them.
type TooManyEntriesError struct{ Count, Max int }

func (e *TooManyEntriesError) Error() string {
	return fmt.Sprintf("the index lists %d entries, more than the %d allowed", e.Count, e.Max)
}

// Index reads .git/index (versions 2, 3 and 4, and the split index).
func (r *Repo) Index() (*Index, error) {
	data, err := r.readFile(".git/index", maxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	p, err := parseIndexMax(data, r.hashLen, r.maxEntries)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	if p.link != nil {
		if p.entries, err = r.mergeSplit(p); err != nil {
			return nil, fmt.Errorf("index: %w", err)
		}
	}
	if r.maxEntries > 0 && len(p.entries) > r.maxEntries {
		return nil, &TooManyEntriesError{Count: len(p.entries), Max: r.maxEntries}
	}
	sort.SliceStable(p.entries, func(i, j int) bool {
		a, b := p.entries[i], p.entries[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Stage < b.Stage
	})
	return &Index{Version: p.version, Entries: p.entries}, nil
}

type parsedIndex struct {
	version int
	entries []IndexEntry
	link    []byte // the link extension's content (split index)
}

func parseIndex(data []byte, hl int) (*parsedIndex, error) { return parseIndexMax(data, hl, 0) }

// parseIndexMax is parseIndex that refuses a header count over max (0: none)
// before it allocates the entries.
func parseIndexMax(data []byte, hl, max int) (*parsedIndex, error) {
	if len(data) < 12+hl || string(data[:4]) != "DIRC" {
		return nil, errors.New("not a git index")
	}
	version := int(binary.BigEndian.Uint32(data[4:8]))
	if version < 2 || version > 4 {
		return nil, fmt.Errorf("index version %d is not supported", version)
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	end := len(data) - hl // the trailing checksum is not verified
	minEntry := 40 + hl + 2 + 1
	if count < 0 || count > (end-12)/minEntry {
		return nil, errors.New("entry count does not fit the file")
	}
	if max > 0 && count > max {
		return nil, &TooManyEntriesError{Count: count, Max: max}
	}
	p := &parsedIndex{version: version, entries: make([]IndexEntry, 0, count)}
	off := 12
	var prev []byte
	for i := 0; i < count; i++ {
		start := off
		if off+40+hl+2 > end {
			return nil, errors.New("truncated entry")
		}
		b := data[off:]
		e := IndexEntry{
			MtimeSec:  binary.BigEndian.Uint32(b[8:]),
			MtimeNsec: binary.BigEndian.Uint32(b[12:]),
			Mode:      binary.BigEndian.Uint32(b[24:]),
			Size:      binary.BigEndian.Uint32(b[36:]),
			ID:        hex.EncodeToString(b[40 : 40+hl]),
		}
		flags := binary.BigEndian.Uint16(b[40+hl:])
		e.Stage = int(flags>>12) & 3
		e.AssumeValid = flags&0x8000 != 0
		off += 40 + hl + 2
		if flags&0x4000 != 0 {
			if version < 3 || off+2 > end {
				return nil, errors.New("extended flags in a version 2 index")
			}
			ext := binary.BigEndian.Uint16(data[off:])
			e.SkipWorktree = ext&0x4000 != 0
			e.IntentToAdd = ext&0x2000 != 0
			off += 2
		}
		if version == 4 {
			strip, n, err := offsetVarint(data[off:end])
			if err != nil {
				return nil, err
			}
			off += n
			if strip > uint64(len(prev)) {
				return nil, errors.New("bad path prefix length")
			}
			nul := bytes.IndexByte(data[off:end], 0)
			if nul < 0 {
				return nil, errors.New("unterminated path")
			}
			path := append(append([]byte(nil), prev[:len(prev)-int(strip)]...), data[off:off+nul]...)
			off += nul + 1
			prev = path
			e.Path = string(path)
		} else {
			nul := bytes.IndexByte(data[off:end], 0)
			if nul < 0 {
				return nil, errors.New("unterminated path")
			}
			e.Path = string(data[off : off+nul])
			off = start + (off-start+nul+8)&^7
			if off > end {
				return nil, errors.New("truncated entry")
			}
		}
		p.entries = append(p.entries, e)
	}
	for off+8 <= end {
		sig := string(data[off : off+4])
		n := int(binary.BigEndian.Uint32(data[off+4:]))
		off += 8
		if n < 0 || off+n > end {
			return nil, fmt.Errorf("extension %q does not fit the file", sig)
		}
		body := data[off : off+n]
		off += n
		switch {
		case sig == "sdir":
			return nil, errors.New("sparse index not supported; run git sparse-checkout disable or scan with the CLI")
		case sig == "link":
			p.link = body
		case sig[0] >= 'A' && sig[0] <= 'Z':
			// optional extension (TREE, REUC, UNTR, ...): not needed
		default:
			return nil, fmt.Errorf("required index extension %q is not supported", sig)
		}
	}
	return p, nil
}

// offsetVarint is git's varint.c encoding: each continuation adds one before
// the shift, so no value has two encodings.
func offsetVarint(b []byte) (uint64, int, error) {
	if len(b) == 0 {
		return 0, 0, errors.New("truncated varint")
	}
	i := 0
	c := b[i]
	i++
	v := uint64(c & 0x7f)
	for c&0x80 != 0 {
		v++
		if i >= len(b) || v > 1<<56 {
			return 0, 0, errors.New("bad varint")
		}
		c = b[i]
		i++
		v = v<<7 + uint64(c&0x7f)
	}
	return v, i, nil
}

// mergeSplit applies a split index (the link extension) to its shared index,
// as Documentation/gitformat-index.txt describes.
func (r *Repo) mergeSplit(p *parsedIndex) ([]IndexEntry, error) {
	hl := r.hashLen
	if len(p.link) < hl {
		return nil, errors.New("short link extension")
	}
	id := p.link[:hl]
	if allZero(id) {
		return p.entries, nil
	}
	data, err := r.readFile(".git/sharedindex."+hex.EncodeToString(id), maxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("shared index: %w", err)
	}
	shared, err := parseIndexMax(data, hl, r.maxEntries)
	if err != nil {
		return nil, fmt.Errorf("shared index: %w", err)
	}
	if shared.link != nil {
		return nil, errors.New("shared index is itself a split index")
	}
	var del, repl bitset
	rest := p.link[hl:]
	if len(rest) > 0 {
		var n int
		if del, n, err = readEWAH(rest, len(shared.entries)); err != nil {
			return nil, fmt.Errorf("delete bitmap: %w", err)
		}
		if repl, _, err = readEWAH(rest[n:], len(shared.entries)); err != nil {
			return nil, fmt.Errorf("replace bitmap: %w", err)
		}
	}
	var replaced, added []IndexEntry
	for _, e := range p.entries {
		if e.Path == "" {
			replaced = append(replaced, e)
		} else {
			added = append(added, e)
		}
	}
	out := make([]IndexEntry, 0, len(shared.entries)+len(added))
	next := 0
	for i, s := range shared.entries {
		e := s
		if repl.has(i) {
			if next >= len(replaced) {
				return nil, errors.New("fewer replacement entries than replace bits")
			}
			e = replaced[next]
			e.Path = s.Path
			next++
		}
		if del.has(i) {
			continue
		}
		out = append(out, e)
	}
	if next != len(replaced) {
		return nil, errors.New("more replacement entries than replace bits")
	}
	return append(out, added...), nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

type bitset []uint64

func (b bitset) has(i int) bool { return i>>6 < len(b) && b[i>>6]>>(uint(i)&63)&1 == 1 }

// readEWAH decodes git's EWAH bitmap (ewah/ewah_io.c): a bit count, a word
// count, big-endian 64-bit words, and the position of the last run-length
// word. Bits at or past limit are dropped. It returns the bytes consumed.
func readEWAH(data []byte, limit int) (bitset, int, error) {
	if len(data) < 8 {
		return nil, 0, errors.New("truncated EWAH bitmap")
	}
	bits := uint64(binary.BigEndian.Uint32(data))
	words := uint64(binary.BigEndian.Uint32(data[4:]))
	total := 8 + 8*words + 4
	if uint64(len(data)) < total {
		return nil, 0, errors.New("truncated EWAH bitmap")
	}
	max := uint64(limit)
	if bits < max {
		max = bits
	}
	out := make(bitset, max/64+1)
	set := func(i uint64) {
		if i < max {
			out[i>>6] |= 1 << (i & 63)
		}
	}
	word := func(i uint64) uint64 { return binary.BigEndian.Uint64(data[8+8*i:]) }
	var pos uint64
	for i := uint64(0); i < words; {
		rlw := word(i)
		runBit := rlw&1 == 1
		run := rlw >> 1 & 0xffffffff
		lit := rlw >> 33
		if i+1+lit > words {
			return nil, 0, errors.New("EWAH literal words past the end")
		}
		if runBit {
			for b := pos; b < pos+run*64 && b < max; b++ {
				set(b)
			}
		}
		pos += run * 64
		for k := uint64(0); k < lit; k++ {
			w := word(i + 1 + k)
			for b := uint64(0); b < 64 && w != 0; b++ {
				if w>>b&1 == 1 {
					set(pos + b)
				}
			}
			pos += 64
		}
		i += 1 + lit
	}
	return out, int(total), nil
}
