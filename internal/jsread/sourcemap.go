package jsread

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// sourceMap holds, per generated line, the segments that map a generated
// column back to an original line (source map v3, single source).
type sourceMap struct {
	lines [][]segment
}

type segment struct {
	genCol, origLine int // origLine 0-based
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func parseSourceMap(raw []byte) (*sourceMap, error) {
	var m struct {
		Version  int    `json:"version"`
		Mappings string `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("source map: %w", err)
	}
	sm := &sourceMap{}
	origLine := 0
	for _, line := range strings.Split(m.Mappings, ";") {
		var segs []segment
		genCol := 0
		for _, s := range strings.Split(line, ",") {
			if s == "" {
				continue
			}
			v, err := vlq(s)
			if err != nil {
				return nil, fmt.Errorf("source map: %w", err)
			}
			genCol += v[0]
			if len(v) >= 4 {
				origLine += v[2]
				segs = append(segs, segment{genCol: genCol, origLine: origLine})
			}
		}
		sm.lines = append(sm.lines, segs)
	}
	return sm, nil
}

// vlq decodes one segment of base64 VLQ values.
func vlq(s string) ([]int, error) {
	var out []int
	val, shift := 0, 0
	for _, c := range s {
		d := strings.IndexRune(b64, c)
		if d < 0 {
			return nil, fmt.Errorf("bad VLQ character %q", c)
		}
		val += (d & 31) << shift
		if d&32 != 0 {
			shift += 5
			continue
		}
		if val&1 != 0 {
			out = append(out, -(val >> 1))
		} else {
			out = append(out, val>>1)
		}
		val, shift = 0, 0
	}
	return out, nil
}

// original returns the 1-based original line of a generated position
// (0-based line and column): the last segment at or before the column, else
// the nearest earlier line that has one.
func (m *sourceMap) original(genLine, genCol int) int {
	for l := genLine; l >= 0; l-- {
		if l >= len(m.lines) {
			continue
		}
		segs := m.lines[l]
		if len(segs) == 0 {
			continue
		}
		i := sort.Search(len(segs), func(k int) bool { return segs[k].genCol > genCol })
		if l < genLine {
			i = len(segs)
		}
		if i > 0 {
			return segs[i-1].origLine + 1
		}
		if l == genLine {
			return segs[0].origLine + 1
		}
	}
	return 1
}

// lineIndex maps a byte offset in code to its 0-based line and column.
func lineIndex(code []byte) func(int) (int, int) {
	starts := []int{0}
	for i, b := range code {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return func(off int) (int, int) {
		l := sort.Search(len(starts), func(k int) bool { return starts[k] > off }) - 1
		return l, off - starts[l]
	}
}
