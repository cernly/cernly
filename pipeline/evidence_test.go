package pipeline

import (
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type noEvidence struct{}

func (noEvidence) Name() string          { return "no-evidence" }
func (noEvidence) Version() string       { return "0.0.1" }
func (noEvidence) Watches(p string) bool { return false }
func (noEvidence) Run(*repo.Repo) plugin.Result {
	return plugin.Result{Facts: []fact.Fact{
		{Kind: fact.KindEnvVar, Key: "BARE", Attrs: map[string]any{"name": "BARE"}},
		{Kind: fact.KindEnvVar, Key: "KEPT", Attrs: map[string]any{"name": "KEPT"}, Evidence: []fact.Evidence{{Path: "a.txt", Line: 1}}},
	}}
}

// A fact without evidence is dropped and the plugin's run says so.
func TestFactWithoutEvidence(t *testing.T) {
	s, _, err := Scan(t.TempDir(), ScanOptions{RepoID: "local/x", Commit: strings.Repeat("a", 40), CommittedAt: "2026-01-01T00:00:00Z", Plugins: []plugin.Plugin{noEvidence{}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Facts) != 1 || s.Facts[0].Key != "KEPT" {
		t.Errorf("facts = %v", s.Facts)
	}
	run := s.Plugins[0]
	if run.Status != "partial" || len(run.Errors) != 1 || !strings.Contains(run.Errors[0].Message, "fact without evidence dropped: env_var BARE") {
		t.Errorf("run = %+v", run)
	}
}
