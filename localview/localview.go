// Package localview derives what a viewer shows for scans on the reader's own
// machine: the map, the findings' words and the stack labels for one scan, and
// what changed between two. It reads only the bytes it is given.
package localview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/cernly/cernly/drift"
	"github.com/cernly/cernly/explain"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/rules"
	"github.com/cernly/cernly/schema"
)

// The largest facts file the viewer reads, and the deepest nesting of objects
// and arrays in it. A real scan is a few megabytes and about ten levels deep;
// past these the page would freeze rather than show anything.
const (
	MaxBytes = 50 << 20
	MaxDepth = 64
)

// View is one scan as a viewer shows it.
type View struct {
	Scan        json.RawMessage   `json:"scan"`
	Map         *rules.Map        `json:"map"`
	Explain     []explain.Rule    `json:"explain"`
	StackLabels map[string]string `json:"stack_labels"`
}

// Changes is two scans of one repository, the older first, and what changed.
type Changes struct {
	From      View               `json:"from"`
	To        View               `json:"to"`
	Groups    []drift.DriftGroup `json:"groups"`
	MapGroups []drift.DriftGroup `json:"map_groups"`
	Drift     *drift.Drift       `json:"drift"`
}

var errNotScan = errors.New("not a Cernly scan")

// read checks a scan against scan.v1.schema.json and parses it. A file a
// newer Cernly wrote, with fact kinds this one does not know, is refused too:
// the page reads only what the schema promises.
func read(raw []byte) (*fact.Scan, error) {
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("%w: the file is %d MB, more than the %d MB this viewer reads", errNotScan, len(raw)>>20, MaxBytes>>20)
	}
	if err := shallow(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", errNotScan, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotScan, err)
	}
	if obj, ok := doc.(map[string]any); !ok || obj["schema"] == nil {
		return nil, fmt.Errorf("%w: missing schema", errNotScan)
	} else if obj["schema"] != fact.SchemaID {
		return nil, fmt.Errorf("%w: schema %v, want %s", errNotScan, obj["schema"], fact.SchemaID)
	}
	sch, err := scanSchema()
	if err != nil {
		return nil, err
	}
	if err := sch.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w this viewer can read (a newer Cernly may have written it): %s", errNotScan, reason(err))
	}
	var s fact.Scan
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", errNotScan, err)
	}
	return &s, nil
}

// shallow fails on JSON nested deeper than MaxDepth, before anything builds
// the tree.
func shallow(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			if depth++; depth > MaxDepth {
				return fmt.Errorf("nested more than %d levels deep", MaxDepth)
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
}

var scanSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ScanV1))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schema.ScanV1ID, doc); err != nil {
		return nil, err
	}
	return c.Compile(schema.ScanV1ID)
})

// reason is the most specific line of a jsonschema error.
func reason(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func view(raw []byte) (View, *fact.Scan, error) {
	s, err := read(raw)
	if err != nil {
		return View{}, nil, err
	}
	m, err := pipeline.Map(raw, rules.Settings{})
	if err != nil {
		return View{}, nil, fmt.Errorf("%w: %v", errNotScan, err)
	}
	return View{Scan: raw, Map: m, Explain: explain.All(), StackLabels: explain.StackLabels(s, m)}, s, nil
}

// Of derives one scan's view from its facts file.
func Of(facts []byte) ([]byte, error) {
	v, _, err := view(facts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Compare derives two scans' views and what changed between them, the older
// commit first whichever file comes first. Two commits with the same date are
// ordered by sha.
func Compare(a, b []byte) ([]byte, error) {
	va, sa, err := view(a)
	if err != nil {
		return nil, err
	}
	vb, sb, err := view(b)
	if err != nil {
		return nil, err
	}
	if sa.Repo != sb.Repo {
		return nil, fmt.Errorf("these are scans of two repositories: %s and %s", sa.Repo, sb.Repo)
	}
	if sb.Commit.CommittedAt < sa.Commit.CommittedAt || sb.Commit.CommittedAt == sa.Commit.CommittedAt && sb.Commit.SHA < sa.Commit.SHA {
		va, vb, sa, sb = vb, va, sb, sa
	}
	d := drift.Compare(sa, sb, va.Map, vb.Map)
	return json.Marshal(Changes{From: va, To: vb, Groups: drift.Groups(d), MapGroups: drift.MapGroups(d), Drift: d})
}
