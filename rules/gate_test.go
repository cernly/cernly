package rules

import (
	"fmt"
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
)

func gateScan(gate ...fact.DeclaredGate) *fact.Scan {
	s := &fact.Scan{Repo: "local/x", Facts: []fact.Fact{}}
	if len(gate) > 0 {
		s.Config = &fact.ScanConfig{File: config.FileName, Declared: &fact.Declared{Gate: gate}}
	}
	return s
}

func levels(ps []GatePolicy) string {
	out := ""
	for _, p := range ps {
		out += fmt.Sprintf("%s=%s/%s ", p.Rule, p.Level, p.By)
	}
	return out
}

// The stricter side wins; the repo alone may set any level; default warn.
func TestGatePolicy(t *testing.T) {
	s := gateScan(
		fact.DeclaredGate{Rule: config.GateNewDependency, Level: config.LevelOff, By: config.ByRepo},
		fact.DeclaredGate{Rule: config.GateNewDependency, Level: config.LevelBlock, By: config.ByOperator},
		fact.DeclaredGate{Rule: config.GateNewOutsideParty, Level: config.LevelBlock, By: config.ByRepo},
		fact.DeclaredGate{Rule: config.GateNewOutsideParty, Level: config.LevelWarn, By: config.ByOperator},
		fact.DeclaredGate{Rule: config.GateNewUnpinnedImage, Level: config.LevelOff, By: config.ByRepo},
	)
	got := levels(Policy(s))
	want := "new_outside_party=block/repo new_sensitive_column=warn/default new_unpinned_image=off/repo " +
		"new_production_stack=warn/default new_dependency=block/operator new_transitive_dependency=off/default " +
		"licence_not_allowed=warn/default new_anonymous_public_endpoint=warn/default gate_loosened=warn/default "
	if got != want {
		t.Errorf("policy = %s\nwant     %s", got, want)
	}
}

func dep(name string, direct bool) fact.Fact {
	return fact.Fact{Kind: fact.KindDependency, Key: "dependency:npm:.:" + name, Plugin: "deps-npm",
		Attrs:    map[string]any{"ecosystem": "npm", "name": name, "version": "1.0.0", "direct": direct},
		Evidence: []fact.Evidence{{Path: "package.json", Line: 3}}}
}

// New direct dependencies only; off silences a rule; block blocks; a lowered
// level and a new repo exclusion are gate_loosened, with the hint file line.
func TestGate(t *testing.T) {
	base := gateScan(fact.DeclaredGate{Rule: config.GateNewUnpinnedImage, Level: config.LevelBlock, By: config.ByRepo, Line: 3})
	base.Facts = []fact.Fact{dep("left-pad", true)}
	head := gateScan(
		fact.DeclaredGate{Rule: config.GateNewDependency, Level: config.LevelBlock, By: config.ByRepo, Line: 3},
		fact.DeclaredGate{Rule: config.GateNewUnpinnedImage, Level: config.LevelWarn, By: config.ByRepo, Line: 4},
	)
	head.Config.Exclusions = []fact.Exclusion{{Pattern: "vendor", By: config.ByRepo, Line: 7, Files: 12}}
	head.Facts = []fact.Fact{dep("left-pad", true), dep("express", true), dep("qs", false)}
	r := Gate(base, head, &Map{}, &Map{})
	var got []string
	for _, f := range r.Findings {
		got = append(got, fmt.Sprintf("%s/%s/%s/%v", f.Rule, f.Level, f.Subject, f.Evidence))
	}
	want := "[new_dependency/block/dependency:npm:.:express/[{package.json 3 0 }] " +
		"gate_loosened/warn/exclude:vendor/[{.cernly.yml 7 0 }] gate_loosened/warn/gate:new_unpinned_image/[{.cernly.yml 4 0 }]]"
	if fmt.Sprint(got) != want {
		t.Errorf("findings = %v\nwant       %s", got, want)
	}
	if !r.Blocked {
		t.Error("a block finding blocks")
	}

	// Off silences the rule, and is itself lower than the base's default warn.
	head.Config.Declared.Gate[0].Level = config.LevelOff
	if r := Gate(base, head, &Map{}, &Map{}); r.Blocked || len(r.Findings) != 3 || r.Findings[1].Subject != "gate:new_dependency" {
		t.Errorf("off: blocked %v, findings %+v", r.Blocked, r.Findings)
	}
}

func TestLicenceDenied(t *testing.T) {
	deny := []string{"GPL-3.0-only", "AGPL-3.0-only"}
	for expr, want := range map[string]string{
		"MIT":                   "[] true",
		"gpl-3.0-only":          "[gpl-3.0-only] true",
		"GPL-3.0-only+":         "[GPL-3.0-only] true",
		"(MIT OR GPL-3.0-only)": "[] true",
		"MIT AND GPL-3.0-only":  "[GPL-3.0-only] true",
		"GPL-3.0-only WITH Classpath-exception-2.0": "[GPL-3.0-only] true",
		"(GPL-3.0-only OR AGPL-3.0-only) AND MIT":   "[GPL-3.0-only] true",
		"GPL-3.0-only AND AGPL-3.0-only":            "[GPL-3.0-only AGPL-3.0-only] true",
		"(MIT":                                      "[] false",
		"MIT OR":                                    "[] false",
	} {
		hit, ok := LicenceDenied(expr, deny)
		if hit == nil {
			hit = []string{}
		}
		if got := fmt.Sprint(hit, " ", ok); got != want {
			t.Errorf("%s: %s, want %s", expr, got, want)
		}
	}
}

func licDep(name, licence string, direct bool) fact.Fact {
	f := dep(name, direct)
	if !direct {
		f.Key = "dependency:npm:.:lock:node_modules/" + name
	}
	if licence != "" {
		f.Attrs["license"] = licence
	}
	return f
}

// new_transitive_dependency is off by default; licence_not_allowed matches
// declared licences only, on new facts or a changed licence, and a shorter
// list is gate_loosened.
func TestGateDependencyRules(t *testing.T) {
	list := fact.DeclaredGate{Rule: config.GateLicenceNotAllowed, Level: config.LevelBlock, By: config.ByOperator,
		Licences: []string{"GPL-3.0-only", "AGPL-3.0-only"}}
	base := gateScan(list)
	base.Facts = []fact.Fact{licDep("old-gpl", "GPL-3.0-only", true), licDep("relicensed", "MIT", true)}
	head := gateScan(list)
	head.Facts = []fact.Fact{licDep("old-gpl", "GPL-3.0-only", true), licDep("relicensed", "AGPL-3.0-only", true),
		licDep("undeclared", "", true), licDep("dual", "MIT OR GPL-3.0-only", false), licDep("deep-gpl", "GPL-3.0-only", false)}
	summary := func(r *GateResult) string {
		var got []string
		for _, f := range r.Findings {
			got = append(got, f.Rule+"/"+f.Level+"/"+f.Subject)
		}
		return fmt.Sprint(got)
	}
	want := "[new_dependency/warn/dependency:npm:.:undeclared " +
		"licence_not_allowed/block/dependency:npm:.:lock:node_modules/deep-gpl " +
		"licence_not_allowed/block/dependency:npm:.:relicensed]"
	if got := summary(Gate(base, head, &Map{}, &Map{})); got != want {
		t.Errorf("findings = %s\nwant       %s", got, want)
	}

	// Transitive on, and the repo list (without an operator) drops an id.
	repoList := list
	repoList.By, repoList.Licences, repoList.Line = config.ByRepo, []string{"GPL-3.0-only"}, 5
	base = gateScan(fact.DeclaredGate{Rule: config.GateLicenceNotAllowed, Level: config.LevelBlock, By: config.ByRepo, Licences: list.Licences})
	head = gateScan(repoList, fact.DeclaredGate{Rule: config.GateNewTransitive, Level: config.LevelWarn, By: config.ByRepo})
	head.Facts = []fact.Fact{licDep("deep", "", false)}
	want = "[new_transitive_dependency/warn/dependency:npm:.:lock:node_modules/deep " +
		"gate_loosened/warn/gate:licence_not_allowed:AGPL-3.0-only]"
	if got := summary(Gate(base, head, &Map{}, &Map{})); got != want {
		t.Errorf("findings = %s\nwant       %s", got, want)
	}
}

// A new tag of an image repository the service already ran unpinned is an
// upgrade; a newly unpinned repository, or one pinned by digest in the
// base, fires.
func TestGateImageUpgrade(t *testing.T) {
	svc := func(name, raw, repo, pinning string) fact.Fact {
		return fact.Fact{Kind: fact.KindService, Key: "service:compose:prod:" + name, Plugin: "compose",
			Attrs: map[string]any{"name": name, "stack": "prod", "image": raw,
				"image_ref": map[string]any{"raw": raw, "registry": "docker.io", "repository": repo, "pinning": pinning}},
			Evidence: []fact.Evidence{{Path: "compose.yml", Line: 3}}}
	}
	base := gateScan()
	base.Facts = []fact.Fact{svc("web", "nginx:1.26", "library/nginx", "exact"), svc("db", "postgres@sha256:abc", "library/postgres", "digest")}
	head := gateScan()
	head.Facts = []fact.Fact{svc("web", "nginx:1.27", "library/nginx", "exact"), svc("db", "postgres:16", "library/postgres", "floating"),
		svc("cache", "redis:7", "library/redis", "floating")}
	bm, hm := Derive(base, "b", Settings{DefaultBranch: "main"}), Derive(head, "h", Settings{DefaultBranch: "main"})
	r := Gate(base, head, bm, hm)
	var got []string
	for _, f := range r.Findings {
		if f.Rule == config.GateNewUnpinnedImage {
			got = append(got, f.Subject)
		}
	}
	if want := "[service:compose:prod:cache service:compose:prod:db]"; fmt.Sprint(got) != want {
		t.Errorf("new_unpinned_image on %v, want %s", got, want)
	}
}

func TestGateNewAnonymousEndpoint(t *testing.T) {
	f := Finding{Rule: FindingEndpointAnonymousPublic, Subject: "endpoint:a", Detail: "route:r (AllowAnonymous)", From: []string{"endpoint:a"}}
	base, head := gateScan(), gateScan()
	r := Gate(base, head, &Map{}, &Map{Findings: []Finding{f}})
	if len(r.Findings) != 1 || r.Findings[0].Rule != config.GateNewAnonymousEndpoint || r.Findings[0].Level != config.LevelWarn ||
		r.Findings[0].Subject != "endpoint:a" || r.Blocked {
		t.Fatalf("%+v", r)
	}
	if r := Gate(base, head, &Map{Findings: []Finding{f}}, &Map{Findings: []Finding{f}}); len(r.Findings) != 0 {
		t.Errorf("in both: %+v", r.Findings)
	}
	head = gateScan(fact.DeclaredGate{Rule: config.GateNewAnonymousEndpoint, Level: config.LevelBlock, By: config.ByRepo, Line: 2})
	if r := Gate(base, head, &Map{}, &Map{Findings: []Finding{f}}); !r.Blocked {
		t.Errorf("block: %+v", r)
	}
}

// A base map from before route rewrites has no way to have had the finding.
func TestGateEndpointOldBase(t *testing.T) {
	f := Finding{Rule: FindingEndpointAnonymousPublic, Subject: "endpoint:a", Detail: "route:r", From: []string{"endpoint:a"}}
	old := &Map{Routes: []RouteEdge{{Route: "route:r"}}}
	if r := Gate(gateScan(), gateScan(), old, &Map{Findings: []Finding{f}}); len(r.Findings) != 0 {
		t.Errorf("old base: %+v", r.Findings)
	}
	cur := &Map{Routes: []RouteEdge{{Route: "route:r", Rewrite: &Rewrite{Kind: "none"}}}}
	if r := Gate(gateScan(), gateScan(), cur, &Map{Findings: []Finding{f}}); len(r.Findings) != 1 {
		t.Errorf("current base: %+v", r.Findings)
	}
}
