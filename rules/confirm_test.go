package rules

import (
	"encoding/json"
	"testing"

	"github.com/cernly/cernly/fact"
)

// confirmScan has an unconfirmed stack (edge), a plain stack (dev), a party on
// one signal (chift), and three unknown hosts.
func confirmScan(decl *fact.Declared) *fact.Scan {
	stack := func(name string, wfs ...any) fact.Fact {
		return fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + name, Attrs: map[string]any{"name": name, "workflows": wfs}}
	}
	env := func(name string, hosts ...string) fact.Fact {
		return fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + name, Attrs: map[string]any{"name": name, "host_hints": fact.Strings(hosts)},
			Evidence: []fact.Evidence{{Path: "config.go", Line: 1}}}
	}
	s := &fact.Scan{Facts: []fact.Fact{
		stack("edge", wf([]string{"push"}, []string{"main"}, "needs.plan.outputs.go == 'true'")),
		stack("dev"),
		env("CHIFT_CLIENT_ID"),
		env("PUBLIC_URL", "app.example.be", "api.acme.test"),
		env("DOCS_URL", "docs.acme.test"),
		env("TEST_URL", "mock.example.org"),
	}}
	if decl != nil {
		s.Config = &fact.ScanConfig{Declared: decl}
	}
	return s
}

func cf(id int64, kind, subject, value string) Confirmation {
	return Confirmation{ID: id, Kind: kind, Subject: subject, Value: value, Reason: "checked", By: "A. Person", At: "2026-11-01T10:00:00Z"}
}

func TestConfirmations(t *testing.T) {
	set := Settings{DefaultBranch: "main", Confirmations: []Confirmation{
		cf(5, ConfirmHost, "host:acme.test", ValueOwn),
		cf(1, ConfirmStackRole, "stack:compose:edge", RoleProduction),
		cf(2, ConfirmStackRole, "stack:compose:dev", RoleNone),
		cf(3, ConfirmParty, "party:chift", ValueNotSupplier),
		cf(4, ConfirmHost, "host:mock.example.org", ValueNotSupplier),
		cf(6, ConfirmHost, "host:app.example.be", ValueApproved),
		cf(7, ConfirmStackRole, "stack:compose:gone", RoleNone),
		cf(8, ConfirmParty, "party:chift", "maybe"),
	}}
	before := Derive(confirmScan(nil), "x", Settings{DefaultBranch: "main"})
	m := Derive(confirmScan(nil), "x", set)

	role := map[string]StackRole{}
	for _, st := range m.Stacks {
		role[st.Stack] = st
	}
	if e := role["stack:compose:edge"]; e.Role != RoleProduction || e.DerivedRole != RoleUnconfirmed || e.Confirmed == nil || e.Confirmed.ID != 1 {
		t.Errorf("edge = %+v", e)
	}
	for _, f := range m.Findings {
		if f.Rule == FindingProductionUnconfirmed || f.Rule == FindingPartyUnconfirmed {
			t.Errorf("finding left: %+v", f)
		}
	}
	for _, p := range m.Parties {
		if p.ID == "chift" {
			t.Error("dismissed party still listed")
		}
	}
	dis := map[string]bool{}
	for _, d := range m.Dismissed {
		dis[d.Subject] = true
	}
	if !dis["party:chift"] || !dis["host:mock.example.org"] || len(dis) != 2 {
		t.Errorf("dismissed = %+v", m.Dismissed)
	}
	own := map[string]OwnHost{}
	for _, o := range m.Own {
		own[o.Host] = o
	}
	if o := own["api.acme.test"]; o.Domain != "acme.test" || o.Confirmed == nil || o.Confirmed.ID != 5 || own["docs.acme.test"].Confirmed == nil {
		t.Errorf("own = %+v", m.Own)
	}
	if len(m.Unknown) != 1 || m.Unknown[0].Host != "app.example.be" || m.Unknown[0].Confirmed == nil {
		t.Errorf("unknown = %+v", m.Unknown)
	}
	status := map[int64]string{}
	for _, c := range m.Confirmations {
		status[c.ID] = c.Status
	}
	want := map[int64]string{1: StatusApplied, 2: StatusApplied, 3: StatusApplied, 4: StatusApplied, 5: StatusApplied, 6: StatusApplied, 7: StatusNoSubject, 8: StatusIgnored}
	for id, w := range want {
		if status[id] != w {
			t.Errorf("confirmation %d: %s, want %s", id, status[id], w)
		}
	}
	// Settings are recorded in id order, so the input order does not matter.
	if m.Settings.Confirmations[0].ID != 1 {
		t.Errorf("settings not in id order: %+v", m.Settings.Confirmations)
	}
	// Facts never change, and without confirmations the map has none of the new fields.
	b, _ := json.Marshal(before)
	var raw map[string]any
	json.Unmarshal(b, &raw)
	for _, k := range []string{"confirmations", "dismissed"} {
		if _, ok := raw[k]; ok {
			t.Errorf("map without confirmations has %q", k)
		}
	}
	if _, ok := raw["settings"].(map[string]any)["confirmations"]; ok {
		t.Error("settings without confirmations has the key")
	}
}

// A role declared in .cernly.yml wins over a confirmation, which is kept and
// marked overridden; the same for a host on a declared own domain.
func TestConfirmationOverridden(t *testing.T) {
	decl := &fact.Declared{
		Stacks:     []fact.DeclaredStack{{Stack: "edge", Role: RoleNone, By: "repo", Line: 3}},
		OwnDomains: []fact.DeclaredDomain{{Domain: "acme.test", By: "repo", Line: 2}},
	}
	m := Derive(confirmScan(decl), "x", Settings{DefaultBranch: "main", Confirmations: []Confirmation{
		cf(1, ConfirmStackRole, "stack:compose:edge", RoleProduction),
		cf(2, ConfirmHost, "host:api.acme.test", ValueNotSupplier),
	}})
	for _, st := range m.Stacks {
		if st.Stack == "stack:compose:edge" && (st.Role != RoleNone || st.Confirmed == nil || !st.Confirmed.Overridden) {
			t.Errorf("edge = %+v", st)
		}
	}
	for _, c := range m.Confirmations {
		if c.Status != StatusOverridden {
			t.Errorf("confirmation %d: %s", c.ID, c.Status)
		}
	}
	if len(m.Dismissed) != 0 {
		t.Errorf("dismissed = %+v", m.Dismissed)
	}
}

func TestValidConfirmation(t *testing.T) {
	for _, c := range []struct {
		kind, subject, value string
		ok                   bool
	}{
		{ConfirmStackRole, "stack:compose:a", "production", true},
		{ConfirmStackRole, "stack:compose:a", "production (unconfirmed)", false},
		{ConfirmStackRole, "service:compose:a:b", "none", false},
		{ConfirmParty, "party:stripe", "approved", true},
		{ConfirmParty, "party:", "approved", false},
		{ConfirmParty, "party:stripe", "own", false},
		{ConfirmHost, "host:x.example.org", "own", true},
		{"link", "service:compose:a:b", "ok", false},
	} {
		if got := ValidConfirmation(c.kind, c.subject, c.value); got != c.ok {
			t.Errorf("%v: %v", c, got)
		}
	}
}
