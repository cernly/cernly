package localview

import (
	"encoding/json"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/redact"
)

// Upload is what a customer saves to send in: the facts file with paths,
// names, hosts and the repo id replaced by tokens, the token list that
// stays behind, how many values each category holds, and every string the
// upload keeps readable (outside the schema id and the commit), so the whole
// file can be checked, not only its first lines. Facts and Map are the files'
// text, each ending in a newline.
type Upload struct {
	Facts    string         `json:"facts"`
	Map      string         `json:"map"`
	Counts   map[string]int `json:"counts"`
	Readable []string       `json:"readable"`
}

// Redact checks a facts file as the viewer does and redacts it.
func Redact(facts []byte) ([]byte, error) {
	s, err := read(facts)
	if err != nil {
		return nil, err
	}
	out, m, readable := redact.ScanReadable(*s)
	f, err := fact.Canonical(out)
	if err != nil {
		return nil, err
	}
	tokens, err := fact.Canonical(m)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Upload{Facts: string(f) + "\n", Map: string(tokens) + "\n", Counts: m.Counts(), Readable: readable})
}
