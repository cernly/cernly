package config

import (
	"fmt"
	"path"
	"strings"
	"testing"
)

func TestMatches(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"examples", "examples/docker-compose.yml", true},
		{"examples", "examples/a/b/go.mod", true},
		{"examples", "src/examples/go.mod", false},
		{"**/testdata", "cmd/x/testdata/fixture/go.mod", true},
		{"**/testdata", "testdata/go.mod", true},
		{"cmd/*/testdata/**", "cmd/x/testdata/fixture/go.mod", true},
		{"docker-compose.*.yml", "docker-compose.dev.yml", true},
		{"docker-compose.*.yml", "deploy/docker-compose.dev.yml", false},
		{"**/docker-compose.*.yml", "deploy/docker-compose.dev.yml", true},
		{"backend/", "backend/go.mod", true},
		{"back", "backend/go.mod", false},
	}
	for _, c := range cases {
		if got := Matches(c.pattern, c.file); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

// naiveMatches is the earlier backtracking matcher, kept as the reference
// for the semantics of Matches.
func naiveMatches(pattern, file string) bool {
	var segs func(p, n []string) bool
	segs = func(p, n []string) bool {
		for len(p) > 0 {
			if p[0] == "**" {
				for i := 0; i <= len(n); i++ {
					if segs(p[1:], n[i:]) {
						return true
					}
				}
				return false
			}
			if len(n) == 0 {
				return false
			}
			if ok, err := path.Match(p[0], n[0]); err != nil || !ok {
				return false
			}
			p, n = p[1:], n[1:]
		}
		return len(n) == 0
	}
	p := strings.Split(strings.TrimSuffix(pattern, "/"), "/")
	n := strings.Split(file, "/")
	for i := 1; i <= len(n); i++ {
		if segs(p, n[:i]) {
			return true
		}
	}
	return false
}

func TestMatchesSameAsNaive(t *testing.T) {
	pats := []string{"**", "a", "b", "*", "a*", "**/a", "a/**", "**/**", "a/**/b", "**/b/**", "*/a", "a/b/", "**/**/a", "a/*/**/b"}
	files := []string{"a", "b", "a/a", "a/b", "b/a", "a/b/a", "b/b/b", "a/x/y/b", "ab/a/b/c", "x/a"}
	for _, p := range pats {
		for _, f := range files {
			if got, want := Matches(p, f), naiveMatches(p, f); got != want {
				t.Errorf("Matches(%q, %q) = %v, naive %v", p, f, got, want)
			}
		}
	}
}

// Many `**` against a deep path was exponential before.
func TestMatchesManyDoubleStars(t *testing.T) {
	pattern := strings.Repeat("**/", 30) + "zz"
	file := strings.Repeat("d/", 40) + "go.mod"
	for i := 0; i < 100; i++ {
		if Matches(pattern, file) {
			t.Fatal("unexpected match")
		}
	}
	if ValidPattern(strings.Repeat("a/", 65)+"a") == nil {
		t.Error("ValidPattern accepted 66 segments")
	}
}

func TestValidPattern(t *testing.T) {
	for _, bad := range []string{"", "/abs", "../up", "a/../b", `a\b`, "a//b", "[", "./x"} {
		if ValidPattern(bad) == nil {
			t.Errorf("ValidPattern(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"examples", "**/testdata", "cmd/*/testdata/**", "backend/"} {
		if err := ValidPattern(ok); err != nil {
			t.Errorf("ValidPattern(%q): %v", ok, err)
		}
	}
}

func TestParseRepo(t *testing.T) {
	rc := ParseRepo([]byte("version: 1\nexclude:\n  - path: examples\n    reason: demo apps\n  - \"**/testdata\"\n  - /abs\nsurprise: true\n"))
	if len(rc.Rules) != 2 {
		t.Fatalf("rules: %+v", rc.Rules)
	}
	if rc.Rules[0].Pattern != "examples" || rc.Rules[0].Reason != "demo apps" || rc.Rules[0].Line != 3 {
		t.Errorf("rule 0: %+v", rc.Rules[0])
	}
	if rc.Rules[1].Pattern != "**/testdata" || rc.Rules[1].Line != 5 {
		t.Errorf("rule 1: %+v", rc.Rules[1])
	}
	if len(rc.Errors) != 2 { // the absolute path and the unknown key
		t.Errorf("errors: %+v", rc.Errors)
	}
}

func TestParseRepoWrongVersion(t *testing.T) {
	rc := ParseRepo([]byte("version: 2\nexclude: [examples]\n"))
	if len(rc.Rules) != 0 || len(rc.Errors) != 1 {
		t.Errorf("want file ignored with one error, got %+v", rc)
	}
}

func TestOperatorWins(t *testing.T) {
	files := []string{FileName, "examples/a/compose.yml", "examples/keep/compose.yml", "secret/x.yml", "src/go.mod"}
	pl, err := Build([]byte("version: 1\nexclude: [examples, .cernly.yml]\n"), Operator{
		Exclude: []string{"secret"},
		Include: []string{"examples/keep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	keep := pl.Apply(files)
	want := []string{FileName, "examples/keep/compose.yml", "src/go.mod"}
	if len(keep) != len(want) {
		t.Fatalf("keep = %v, want %v", keep, want)
	}
	for i := range want {
		if keep[i] != want[i] {
			t.Fatalf("keep = %v, want %v", keep, want)
		}
	}
	ex := pl.Record.Exclusions
	if len(ex) != 3 || ex[0].Files != 1 || ex[0].Reincluded != 1 || ex[1].Files != 0 || ex[2].By != ByOperator || ex[2].Files != 1 {
		t.Errorf("record: %+v", ex)
	}
}

func TestNoRepoConfigIsRecorded(t *testing.T) {
	pl, err := Build([]byte("version: 1\nexclude: [src]\n"), Operator{NoRepoConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	keep := pl.Apply([]string{"src/go.mod"})
	if len(keep) != 1 || !pl.Record.IgnoredByOperator {
		t.Errorf("keep %v, record %+v", keep, pl.Record)
	}
}

// Declarations: own domains from both sides, per stack the operator's entry
// wins, bad entries are reported and skipped.
func TestDeclared(t *testing.T) {
	repo := []byte("version: 1\ndomains:\n  own: [Acme-Widgets.be, 'https://bad', acme-labs.be]\nstacks:\n  default:\n    role: production\n    name: UAT\n  edge:\n    role: maybe\n")
	op := Operator{File: []byte("version: 1\ndomains:\n  own: [widgets-shop.be]\nstacks:\n  default:\n    role: none\n"), FileName: "acme.yml"}
	pl, err := Build(repo, op)
	if err != nil {
		t.Fatal(err)
	}
	d := pl.Record.Declared
	if d == nil {
		t.Fatal("nothing declared")
	}
	var domains []string
	for _, x := range d.OwnDomains {
		domains = append(domains, x.Domain+"/"+x.By)
	}
	if fmt.Sprint(domains) != "[acme-labs.be/repo acme-widgets.be/repo widgets-shop.be/operator]" {
		t.Errorf("domains = %v", domains)
	}
	if len(d.Stacks) != 1 || d.Stacks[0].Role != "none" || d.Stacks[0].By != ByOperator || d.Stacks[0].Name != "" {
		t.Errorf("stacks = %+v (the operator's entry replaces the repo's)", d.Stacks)
	}
	var msgs []string
	for _, e := range pl.Record.Errors {
		msgs = append(msgs, fmt.Sprintf("%s:%d", e.Path, e.Line))
	}
	if fmt.Sprint(msgs) != "[.cernly.yml:3 .cernly.yml:9]" {
		t.Errorf("errors = %v %+v", msgs, pl.Record.Errors)
	}
	if pl.Empty() {
		t.Error("a plan with declarations is not empty")
	}
	// --no-repo-config drops the repo's declarations too.
	pl2, _ := Build(repo, Operator{NoRepoConfig: true})
	if pl2.Record.Declared != nil {
		t.Errorf("declared = %+v", pl2.Record.Declared)
	}
}

// Gate levels: both sides kept, unknown rules and levels reported and
// skipped.
func TestGateDeclared(t *testing.T) {
	repo := []byte("version: 1\ngate:\n  new_dependency: block\n  new_outside_party: off\n  no_such_rule: block\n  new_unpinned_image: maybe\n")
	op := Operator{File: []byte("version: 1\ngate:\n  new_outside_party: warn\n"), FileName: "acme.yml"}
	pl, err := Build(repo, op)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range pl.Record.Declared.Gate {
		got = append(got, fmt.Sprintf("%s=%s/%s:%d", g.Rule, g.Level, g.By, g.Line))
	}
	want := "[new_dependency=block/repo:3 new_outside_party=off/repo:4 new_outside_party=warn/operator:3]"
	if fmt.Sprint(got) != want {
		t.Errorf("gate = %v, want %s", got, want)
	}
	var msgs []string
	for _, e := range pl.Record.Errors {
		msgs = append(msgs, fmt.Sprintf("%s:%d", e.Path, e.Line))
	}
	if fmt.Sprint(msgs) != "[.cernly.yml:5 .cernly.yml:6]" {
		t.Errorf("errors = %v", msgs)
	}
}

// licence_not_allowed takes a level and a list; a bad id is an error and is
// skipped, the rest of the entry stays.
func TestGateLicences(t *testing.T) {
	repo := []byte("version: 1\ngate:\n  licence_not_allowed:\n    level: block\n    licences: [GPL-3.0-only, \"MIT OR X\", GPL-3.0-only, AGPL-3.0-only]\n    extra: 1\n  new_transitive_dependency: warn\n")
	pl, err := Build(repo, Operator{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range pl.Record.Declared.Gate {
		got = append(got, fmt.Sprintf("%s=%s:%d%v", g.Rule, g.Level, g.Line, g.Licences))
	}
	want := "[licence_not_allowed=block:3[GPL-3.0-only AGPL-3.0-only] new_transitive_dependency=warn:7[]]"
	if fmt.Sprint(got) != want {
		t.Errorf("gate = %v, want %s", got, want)
	}
	if len(pl.Record.Errors) != 2 {
		t.Errorf("errors = %+v", pl.Record.Errors)
	}
}

// Tags: a mapping or a list of key:value; per key the operator's entry
// replaces the repo's; bad keys and values are reported and skipped.
func TestTagsDeclared(t *testing.T) {
	repo := []byte("version: 1\ntags:\n  team: payments\n  criticality: high\n  Bad Key: x\n  empty: \"\"\n  product: webshop\n")
	op := Operator{File: []byte("version: 1\ntags:\n  - criticality:low\n  - domain: commerce\n"), FileName: "acme.yml"}
	pl, err := Build(repo, op)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range pl.Record.Declared.Tags {
		got = append(got, fmt.Sprintf("%s=%s/%s:%d", g.Key, g.Value, g.By, g.Line))
	}
	want := "[criticality=low/operator:3 domain=commerce/operator:4 product=webshop/repo:7 team=payments/repo:3]"
	if fmt.Sprint(got) != want {
		t.Errorf("tags = %v, want %s", got, want)
	}
	var msgs []string
	for _, e := range pl.Record.Errors {
		msgs = append(msgs, fmt.Sprintf("%s:%d", e.Path, e.Line))
	}
	if fmt.Sprint(msgs) != "[.cernly.yml:5 .cernly.yml:6]" {
		t.Errorf("errors = %v %+v", msgs, pl.Record.Errors)
	}
	if r := ParseRepo([]byte("version: 1\ntags: [a, b]\n")); len(r.Errors) != 2 {
		t.Errorf("list without colons: %+v", r.Errors)
	}
	if r := ParseRepo([]byte("version: 1\ntags: team\n")); len(r.Errors) != 1 {
		t.Errorf("scalar: %+v", r.Errors)
	}
}

// The operator's --config must be right: any error fails the build. The
// repo's own file only gets recorded errors.
func TestOperatorFileErrors(t *testing.T) {
	for _, bad := range []string{
		"version: 2\n",
		"version: 1\nexclud: [x]\n",
		"version: 1\nexclude: ['../x']\n",
		"version: 1\ndomains:\n  own: ['https://bad']\n",
		"[not a mapping\n",
		"version: 1\ntags:\n  - nocolon\n",
		"version: 1\ngate:\n  new_outside_party: maybe\n",
	} {
		_, err := Build(nil, Operator{File: []byte(bad), FileName: "acme.yml"})
		if err == nil || !strings.Contains(err.Error(), "acme.yml:") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	pl, err := Build([]byte("version: 1\nexclud: [x]\n"), Operator{File: []byte("version: 1\n"), FileName: "acme.yml"})
	if err != nil || len(pl.Record.Errors) != 1 {
		t.Errorf("repo file errors: %v, %+v", err, pl.Record.Errors)
	}
}
