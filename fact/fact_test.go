package fact

import (
	"fmt"
	"testing"
)

func TestMergeShared(t *testing.T) {
	s := Scan{Facts: []Fact{
		{Kind: KindEnvVar, Key: "env_var:DB_URL", Plugin: "env", Attrs: map[string]any{"name": "DB_URL", "origins": []string{"compose"}}, Evidence: []Evidence{{Path: "a", Line: 1}}},
		{Kind: KindEnvVar, Key: "env_var:DB_URL", Plugin: "java", Attrs: map[string]any{"name": "DB_URL", "origins": []any{"config", "compose"}}, Evidence: []Evidence{{Path: "b", Line: 2}}},
		{Kind: KindOutboundHost, Key: "outbound_host:api.example.com", Plugin: "outbound", Attrs: map[string]any{"host": "api.example.com", "via": "literal_url", "usage": "mention"}, Evidence: []Evidence{{Path: "a", Line: 3}}},
		{Kind: KindOutboundHost, Key: "outbound_host:api.example.com", Plugin: "java", Attrs: map[string]any{"host": "api.example.com", "via": "literal_url", "usage": "request"}, Evidence: []Evidence{{Path: "b", Line: 4}}},
	}}
	if clashes := s.Normalise(); len(clashes) > 0 {
		t.Fatal(clashes)
	}
	env, _ := Canonical(s.Facts[0].Attrs)
	host, _ := Canonical(s.Facts[1].Attrs)
	if string(env) != `{"name":"DB_URL","origins":["compose","config"]}` || len(s.Facts[0].Evidence) != 2 {
		t.Errorf("env %s", env)
	}
	if string(host) != `{"host":"api.example.com","usage":"request","via":"literal_url"}` {
		t.Errorf("host %s", host)
	}
	// One plugin emitting two different attrs for one key is still a clash.
	s = Scan{Facts: []Fact{
		{Kind: KindEnvVar, Key: "env_var:X", Plugin: "env", Attrs: map[string]any{"name": "X", "origins": []string{"compose"}}},
		{Kind: KindEnvVar, Key: "env_var:X", Plugin: "env", Attrs: map[string]any{"name": "X", "origins": []string{"code"}}},
	}}
	if len(s.Normalise()) != 1 {
		t.Error("same-plugin clash not reported")
	}
}

// Evidence differing only in EndLine or Unit sorts the same whatever the
// input order, and every duplicate goes.
func TestDedupeEvidence(t *testing.T) {
	a := Evidence{Path: "a", Line: 1, EndLine: 3}
	b := Evidence{Path: "a", Line: 1, EndLine: 2}
	c := Evidence{Path: "a", Line: 1, EndLine: 2, Unit: "u"}
	want := fmt.Sprint([]Evidence{b, c, a})
	for _, in := range [][]Evidence{
		{a, b, a, c, b},
		{c, a, b, b, a, c},
		{b, c, a},
	} {
		if got := fmt.Sprint(dedupeEvidence(in)); got != want {
			t.Errorf("dedupe = %s, want %s", got, want)
		}
	}
}
