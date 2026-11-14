package rules

import (
	"testing"

	"github.com/cernly/cernly/fact"
)

// testWf is a push-on-main job that starts a stack in step "start" (line 20)
// and runs the given commands in one step after it (line 22).
func testWf(job, jobName, stepName string, after ...map[string]any) map[string]any {
	w := wf([]string{"push"}, []string{"main"})
	w["job"], w["job_line"] = job, 9
	if jobName != "" {
		w["job_name"] = jobName
	}
	w["step"] = map[string]any{"line": 20, "name": stepName, "run": "docker compose up -d"}
	as := []any{}
	for _, a := range after {
		as = append(as, a)
	}
	w["steps_after"] = as
	return w
}

func run(line int, s string) map[string]any  { return map[string]any{"line": line, "run": s} }
func uses(line int, s string) map[string]any { return map[string]any{"line": line, "uses": s} }

func TestTestJob(t *testing.T) {
	for name, c := range map[string]struct {
		w      map[string]any
		by     string // "" = not a test job
		match  string
		line   int
		detail string
	}{
		"job id e2e_tests": {testWf("e2e_tests", "", "Start services"), "job_id", "e2e_tests", 9,
			"not production: started by test job e2e_tests (.github/workflows/cd.yml:9)"},
		"job id run-tests":      {testWf("run-tests", "", "up"), "job_id", "run-tests", 9, ""},
		"job name":              {testWf("pw", "Integration tests", "up"), "job_name", "Integration tests", 9, ""},
		"ci-check":              {testWf("ci-check", "", "up"), "job_id", "ci-check", 9, ""},
		"step name":             {testWf("build", "", "Start e2e services"), "step_name", "Start e2e services", 20, `not production: started by test step "Start e2e services" in job build (.github/workflows/cd.yml:20)`},
		"segment, not a prefix": {testWf("testing", "", "contest"), "", "", 0, ""},
		"ci alone":              {testWf("ci", "", "up"), "", "", 0, ""},
		"latest is no test":     {testWf("latest", "", "up"), "", "", 0, ""},
		"deploy word vetoes":    {testWf("test-and-deploy", "", "up", run(22, "curl -fsS https://example.com/health")), "", "", 0, ""},
		"runner only": {testWf("verify", "", "up", uses(21, "actions/setup-go@v5"), run(22, "go mod download\nCI=1 go test ./...\ndocker compose down")), "runner", "go test ./...", 22,
			"not production: job verify only runs tests after starting the stack (go test ./... at .github/workflows/cd.yml:22)"},
		"runners":             {testWf("j", "", "up", run(22, "npm ci && npx playwright test --project=x"), run(23, "pnpm run test:unit"), run(24, "python -m pytest -q")), "runner", "npx playwright test --project=x", 22, ""},
		"no runner":           {testWf("j", "", "up", run(22, "npm ci")), "", "", 0, ""},
		"nothing after":       {testWf("j", "", "up"), "", "", 0, ""},
		"deploy after test":   {testWf("j", "", "up", run(22, "npm test"), run(23, "./deploy.sh")), "", "", 0, ""},
		"other action":        {testWf("j", "", "up", run(22, "npm test"), uses(23, "appleboy/ssh-action@v1")), "", "", 0, ""},
		"runner, deploy name": {testWf("deploy", "", "up", run(22, "go test ./...")), "runner", "go test ./...", 22, ""},
	} {
		got := testJob(c.w)
		switch {
		case c.by == "" && got != nil:
			t.Errorf("%s: test job %+v, want none", name, got)
		case c.by != "" && (got == nil || got.By != c.by || got.Match != c.match || got.Line != c.line):
			t.Errorf("%s: %+v, want %s %q line %d", name, got, c.by, c.match, c.line)
		case c.detail != "" && got.detail(anyStr(c.w["job"]), anyStr(c.w["path"])) != c.detail:
			t.Errorf("%s: detail %q", name, got.detail(anyStr(c.w["job"]), anyStr(c.w["path"])))
		}
	}
	// A GitHub environment means a deploy: never a test job by name.
	w := testWf("e2e", "", "up")
	w["environment"] = "staging"
	if got := testJob(w); got != nil {
		t.Errorf("environment: %+v", got)
	}
}

// A test job gives none with its reason; a deploy job on the same stack still
// wins; a declared role still replaces the derived one.
func TestTestJobRoles(t *testing.T) {
	stack := func(name string, wfs ...any) fact.Fact {
		return fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + name, Attrs: map[string]any{"name": name, "tool": "compose", "workflows": wfs}}
	}
	deploy := wf([]string{"push"}, []string{"main"})
	s := &fact.Scan{
		Config: &fact.ScanConfig{Declared: &fact.Declared{Stacks: []fact.DeclaredStack{{Stack: "declared", Role: "production", By: "repo", Line: 4}}}},
		Facts: []fact.Fact{
			stack("e2e", testWf("e2e_tests", "", "up")),
			stack("both", testWf("e2e_tests", "", "up"), deploy),
			stack("declared", testWf("e2e_tests", "", "up")),
		},
	}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	roles := map[string]StackRole{}
	for _, st := range m.Stacks {
		roles[st.Stack] = st
	}
	if st := roles["stack:compose:e2e"]; st.Role != RoleNone || len(st.Reasons) != 1 || st.Reasons[0].TestJob == nil || st.Reasons[0].TestJob.Line != 9 {
		t.Errorf("e2e = %+v", st)
	}
	if st := roles["stack:compose:both"]; st.Role != RoleProduction {
		t.Errorf("both = %+v", st)
	}
	if st := roles["stack:compose:declared"]; st.Role != RoleProduction || st.DerivedRole != RoleNone || st.Declared == nil || st.Declared.Line != 4 {
		t.Errorf("declared = %+v", st)
	}
}

// A job that only renders or validates a stack never makes it production;
// scans without "starts" are judged as before.
func TestRendersOnly(t *testing.T) {
	stack := func(name string, wfs ...any) fact.Fact {
		return fact.Fact{Kind: fact.KindStack, Key: "stack:helm:" + name, Attrs: map[string]any{"name": name, "tool": "helm", "workflows": wfs}}
	}
	with := func(starts any) map[string]any {
		w := wf([]string{"push"}, []string{"main"})
		if starts != nil {
			w["starts"] = starts
		}
		return w
	}
	lint := with(false)
	lint["job"] = "lint"
	s := &fact.Scan{Facts: []fact.Fact{
		stack("rendered", with(false)),
		stack("deployed", with(true)),
		stack("old-scan", with(nil)),
		stack("both", lint, with(true)),
	}}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	got := map[string]string{}
	for _, st := range m.Stacks {
		got[st.Stack] = st.Role
		if st.Stack == "stack:helm:rendered" && (len(st.Reasons) != 1 || st.Reasons[0].Detail != RendersOnly) {
			t.Errorf("rendered reasons = %+v", st.Reasons)
		}
	}
	want := map[string]string{"stack:helm:rendered": RoleNone, "stack:helm:deployed": RoleProduction,
		"stack:helm:old-scan": RoleProduction, "stack:helm:both": RoleProduction}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
}
