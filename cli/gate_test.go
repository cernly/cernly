package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/archdoc"
	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
)

// writeScan writes a scan as scan does, after edit changes it.
func writeScan(t *testing.T, data []byte, name string, edit func(*fact.Scan)) string {
	t.Helper()
	var s fact.Scan
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	edit(&s)
	out, err := fact.Canonical(&s)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Against an empty base every watched thing in fixture-ci is new; the repo's
// block level blocks; JSON is deterministic; SARIF carries the evidence.
func TestGateCommand(t *testing.T) {
	data := scanDir(t, "fixture-ci")
	base := writeScan(t, data, "base.json", func(s *fact.Scan) { s.Facts = []fact.Fact{}; s.Commit.SHA = strings.Repeat("1", 40) })
	head := writeScan(t, data, "head.json", func(s *fact.Scan) {
		if s.Config == nil {
			s.Config = &fact.ScanConfig{}
		}
		s.Config.Declared = &fact.Declared{Gate: []fact.DeclaredGate{{Rule: config.GateNewOutsideParty, Level: config.LevelBlock, By: config.ByRepo, Line: 2}}}
	})
	r, err := RunGate(base, head, "main")
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, f := range r.Findings {
		count[f.Rule+"/"+f.Level]++
		if len(f.Evidence) == 0 {
			t.Errorf("%s %s: no evidence", f.Rule, f.Subject)
		}
	}
	want := map[string]int{"new_outside_party/block": 1, "new_unpinned_image/warn": 9, "new_production_stack/warn": 2, "new_dependency/warn": 3}
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%s = %d, want %d (all: %v)", k, count[k], n, count)
		}
	}
	if !r.Blocked {
		t.Error("a new party at level block blocks")
	}
	a, _ := RenderGate(r, "json")
	r2, _ := RunGate(base, head, "main")
	if b, _ := RenderGate(r2, "json"); string(a) != string(b) {
		t.Error("json output differs between runs")
	}
	sarifOut, err := RenderGate(r, "sarif")
	if err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(sarifOut, &log); err != nil || log.Version != "2.1.0" || len(log.Runs[0].Results) != len(r.Findings) {
		t.Fatalf("sarif: %v %+v", err, log)
	}
	if res := log.Runs[0].Results[0]; res.Level != "error" || res.Locations[0].PhysicalLocation.ArtifactLocation.URI != "apps/api/src/server.js" {
		t.Errorf("first sarif result = %+v", res)
	}

	// The same scan as base: nothing new.
	if r, _ := RunGate(head, head, "main"); len(r.Findings) != 0 || r.Blocked {
		t.Errorf("same scan: %+v", r.Findings)
	}
	// Two repos are not compared.
	other := writeScan(t, data, "other.json", func(s *fact.Scan) { s.Repo = "local/other" })
	if _, err := RunGate(other, head, "main"); err == nil {
		t.Error("base and head of different repos must fail")
	}
}

// The open core reads only the current schema id: a facts file with the
// earlier id is refused by every command that derives a map.
func TestLegacySchemaRefused(t *testing.T) {
	data := scanDir(t, "fixture")
	cur := fact.SchemaID
	legacy := writeScan(t, data, "legacy.json", func(s *fact.Scan) { s.Schema = "architecture-observability.scan/v1" })
	current := writeScan(t, data, "current.json", func(s *fact.Scan) { s.Schema = cur })
	if _, err := RunGate(legacy, current, "main"); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("gate with a legacy base: %v", err)
	}
	if _, err := BuildDocs(legacy, "main", archdoc.Options{}); err == nil {
		t.Error("docs accepted a legacy facts file")
	}
}
