package localview

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cernly/cernly/depview"
	"github.com/cernly/cernly/explain"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/rules"
)

func example(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../schema/example.facts.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// edit returns the example scan changed by f.
func edit(t *testing.T, f func(s map[string]any)) []byte {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal(example(t), &s); err != nil {
		t.Fatal(err)
	}
	f(s)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// later is the example one commit on, with its first fact removed.
func later(t *testing.T) []byte {
	return edit(t, func(s map[string]any) {
		s["commit"] = map[string]any{"sha": "b000000000000000000000000000000000000000", "committed_at": "2026-10-21T09:00:00Z"}
		s["facts"] = s["facts"].([]any)[1:]
	})
}

func TestView(t *testing.T) {
	raw := example(t)
	out, err := Of(raw)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Map         struct{ Schema string } `json:"map"`
		Explain     []explain.Rule          `json:"explain"`
		StackLabels map[string]string       `json:"stack_labels"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Map.Schema != rules.SchemaID {
		t.Errorf("map schema %q", v.Map.Schema)
	}
	if len(v.Explain) == 0 {
		t.Error("no words")
	}
	var s fact.Scan
	_ = json.Unmarshal(raw, &s)
	m, _ := pipeline.Map(raw, rules.Settings{})
	if want := explain.StackLabels(&s, m); !reflect.DeepEqual(v.StackLabels, want) && !(len(want) == 0 && len(v.StackLabels) == 0) {
		t.Errorf("labels %v, want %v", v.StackLabels, want)
	}
}

func TestViewRejectsNonScan(t *testing.T) {
	mapDoc, err := pipeline.Map(example(t), rules.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(mapDoc)
	for name, raw := range map[string][]byte{"empty": nil, "object": []byte("{}"), "map": m, "text": []byte("x")} {
		if _, err := Of(raw); err == nil || !strings.Contains(err.Error(), "not a Cernly scan") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestViewRefusesWhatTheSchemaRefuses(t *testing.T) {
	for name, raw := range map[string][]byte{
		"no committed_at":  edit(t, func(s map[string]any) { delete(s["commit"].(map[string]any), "committed_at") }),
		"no facts":         edit(t, func(s map[string]any) { delete(s, "facts") }),
		"facts not a list": edit(t, func(s map[string]any) { s["facts"] = map[string]any{} }),
		"unknown kind": edit(t, func(s map[string]any) {
			s["facts"] = append(s["facts"].([]any), map[string]any{"kind": "future_kind", "key": "future_kind:x", "attrs": map[string]any{},
				"plugin": "future", "evidence": []any{map[string]any{"path": "x", "line": 1}}})
		}),
	} {
		if _, err := Of(raw); err == nil || !strings.Contains(err.Error(), "not a Cernly scan") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestViewLimits(t *testing.T) {
	big := append(example(t), bytes.Repeat([]byte(" "), MaxBytes)...)
	if _, err := Of(big); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("big: %v", err)
	}
	deep := []byte(strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1))
	if _, err := Of(deep); err == nil || !strings.Contains(err.Error(), "levels deep") {
		t.Errorf("deep: %v", err)
	}
	if _, err := Of(example(t)); err != nil {
		t.Errorf("example within the limits: %v", err)
	}
}

func changes(t *testing.T, a, b []byte) Changes {
	t.Helper()
	out, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		From  struct{ Scan fact.Scan } `json:"from"`
		Drift struct {
			Facts []struct{ Change, Key string } `json:"facts"`
		} `json:"drift"`
	}
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatal(err)
	}
	var full Changes
	_ = json.Unmarshal(out, &full)
	if c.From.Scan.Commit.SHA == "" {
		t.Fatal("no from scan")
	}
	return full
}

func TestCompareOrdersByDate(t *testing.T) {
	old := example(t)
	c := changes(t, later(t), old)
	var s fact.Scan
	_ = json.Unmarshal(c.From.Scan, &s)
	if s.Commit.SHA != "a91f3c2e8b7d4f6a1c0e9b8d7f6a5c4b3e2d1f0a" {
		t.Errorf("from %s, want the older scan", s.Commit.SHA)
	}
}

func TestCompareSameDateOrdersBySha(t *testing.T) {
	var s fact.Scan
	_ = json.Unmarshal(example(t), &s)
	same := edit(t, func(m map[string]any) {
		m["commit"] = map[string]any{"sha": "0000000000000000000000000000000000000001", "committed_at": s.Commit.CommittedAt}
	})
	for _, pair := range [][2][]byte{{example(t), same}, {same, example(t)}} {
		var from fact.Scan
		_ = json.Unmarshal(changes(t, pair[0], pair[1]).From.Scan, &from)
		if from.Commit.SHA != "0000000000000000000000000000000000000001" {
			t.Errorf("from %s, want the lower sha", from.Commit.SHA)
		}
	}
}

func TestCompareShowsRemoval(t *testing.T) {
	var s fact.Scan
	_ = json.Unmarshal(example(t), &s)
	c := changes(t, example(t), later(t))
	for _, ch := range c.Drift.Facts {
		if ch.Change == "removed" && ch.Key == s.Facts[0].Key {
			return
		}
	}
	t.Errorf("%s not removed in %+v", s.Facts[0].Key, c.Drift.Facts)
}

func TestCompareDifferentRepos(t *testing.T) {
	other := edit(t, func(s map[string]any) { s["repo"] = "example.org/other" })
	var s fact.Scan
	_ = json.Unmarshal(example(t), &s)
	_, err := Compare(example(t), other)
	if err == nil || !strings.Contains(err.Error(), "two repositories") || !strings.Contains(err.Error(), s.Repo) || !strings.Contains(err.Error(), "example.org/other") {
		t.Fatalf("err %v", err)
	}
}

func TestViewDependencies(t *testing.T) {
	raw := example(t)
	out, err := Of(raw)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Dependencies depview.Summary `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	var s fact.Scan
	_ = json.Unmarshal(raw, &s)
	m, _ := pipeline.Map(raw, rules.Settings{})
	want := depview.Of(&s, m)
	if !reflect.DeepEqual(v.Dependencies, want) {
		t.Errorf("dependencies %+v, want %+v", v.Dependencies, want)
	}
	if n := v.Dependencies.Counts["direct"] + v.Dependencies.Counts["transitive"]; n == 0 {
		t.Error("the example scan's dependency is not listed")
	}
}
