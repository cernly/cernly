package explain

import (
	"testing"

	"github.com/cernly/cernly/fact"
)

// Two tools' stacks with one name read apart by their tool (validation-1:
// microservices-demo's report said "helm-chart, gke-staging, helm-chart").
func TestKeyLabel(t *testing.T) {
	st := func(tool, name string) fact.Fact {
		return fact.Fact{Kind: fact.KindStack, Key: "stack:" + tool + ":" + name, Attrs: map[string]any{"name": name, "tool": tool}}
	}
	s := &fact.Scan{Facts: []fact.Fact{st("helm", "chart"), st("kubernetes", "chart"), st("compose", "prod")}}
	shared := SharedNames(s)
	labels := map[string]string{"prod": "deploy/prod"}
	for key, want := range map[string]string{
		"stack:helm:chart":       "chart (helm)",
		"stack:kubernetes:chart": "chart (kubernetes)",
		"stack:compose:prod":     "deploy/prod",
	} {
		if got := KeyLabel(labels, shared, key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}
