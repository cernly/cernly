package rules

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
)

func wf(events []string, branches []string, conds ...string) map[string]any {
	w := map[string]any{"path": ".github/workflows/cd.yml", "job": "deploy", "events": fact.Strings(events), "branches": fact.Strings(branches)}
	var cs []any
	for _, c := range conds {
		cs = append(cs, map[string]any{"job": "deploy", "if": c})
	}
	if cs != nil {
		w["conditions"] = cs
	}
	return w
}

// with sets a filter field on w.
func with(w map[string]any, key string, patterns ...string) map[string]any {
	w[key] = fact.Strings(patterns)
	return w
}

func TestJudge(t *testing.T) {
	for name, c := range map[string]struct {
		w    map[string]any
		want string
	}{
		"push main":              {wf([]string{"push"}, []string{"main"}), RoleProduction},
		"push any branch":        {wf([]string{"push"}, []string{"*"}), RoleProduction},
		"push release/* only":    {wf([]string{"push"}, []string{"release/*"}), RoleNone},
		"push release/** only":   {wf([]string{"push"}, []string{"release/**"}), RoleNone},
		"push all but main":      {wf([]string{"push"}, []string{"**", "!main"}), RoleNone},
		"push main re-included":  {wf([]string{"push"}, []string{"**", "!ma*", "main"}), RoleProduction},
		"push ma?in":             {wf([]string{"push"}, []string{"mai?n"}), RoleProduction},
		"push filter unrecorded": {wf([]string{"push"}, nil), RoleUnconfirmed},
		"push tags only":         {with(wf([]string{"push"}, nil), "tags", "v*"), RoleNone},
		"push tags-ignore only":  {with(wf([]string{"push"}, nil), "tags_ignore", "v0*"), RoleNone},
		"push ignore main":       {with(wf([]string{"push"}, nil), "branches_ignore", "main"), RoleNone},
		"push ignore ma*":        {with(wf([]string{"push"}, nil), "branches_ignore", "dev", "ma*"), RoleNone},
		"push ignore dev":        {with(wf([]string{"push"}, nil), "branches_ignore", "dev"), RoleProduction},
		"push ignore and tags":   {with(with(wf([]string{"push"}, nil), "branches_ignore", "dev"), "tags", "v*"), RoleProduction},
		"push dev and tags":      {with(wf([]string{"push"}, []string{"dev"}), "tags", "v*"), RoleNone},
		"tags only, gated":       {with(wf([]string{"push"}, nil, "github.ref == 'refs/heads/main'"), "tags", "v*"), RoleNone},
		"ignore main, gated":     {with(wf([]string{"push"}, nil, "github.ref == 'refs/heads/main'"), "branches_ignore", "main"), RoleNone},
		"run ignore main":        {with(wf([]string{"workflow_run"}, nil, "github.event.workflow_run.head_branch == 'main'"), "after_branches_ignore", "main"), RoleNone},
		"run ignore dev":         {with(wf([]string{"workflow_run"}, nil), "after_branches_ignore", "dev"), RoleUnconfirmed},
		"run ignore dev, gated":  {with(wf([]string{"workflow_run"}, nil, "github.event.workflow_run.head_branch == 'main'"), "after_branches_ignore", "dev"), RoleProduction},
		"manual only":            {wf([]string{"workflow_dispatch"}, nil), RoleNone},
		"pull request":           {wf([]string{"pull_request"}, nil), RoleNone},
		"push gated on main":     {wf([]string{"push"}, []string{"*"}, "github.ref == 'refs/heads/main'"), RoleProduction},
		"push gated elsewhere":   {wf([]string{"push"}, []string{"*"}, "github.ref_name == 'develop'"), RoleNone},
		"gate cannot add runs":   {wf([]string{"push"}, []string{"develop"}, "github.ref_name == 'main'"), RoleNone},
		"unknown term":           {wf([]string{"push"}, []string{"main"}, "needs.plan.outputs.go == 'true'"), RoleUnconfirmed},
		"ignorable terms":        {wf([]string{"push"}, []string{"main"}, `${{ github.event_name == "workflow_dispatch" || success() == 'x' }}`), RoleUnconfirmed},
		"workflow_run, no gate":  {wf([]string{"workflow_run"}, nil), RoleUnconfirmed},
		// A gate settles only a trigger whose branch nobody filtered: push
		// facts without branches_ignore or tags may never run on main.
		"push tags or ignore, gated":      {wf([]string{"push"}, nil, "github.ref == 'refs/heads/main'"), RoleUnconfirmed},
		"push tags or ignore, other":      {wf([]string{"push"}, nil, "github.ref == 'refs/heads/dev'"), RoleNone},
		"schedule gated":                  {wf([]string{"schedule"}, nil, "github.ref == 'refs/heads/main'"), RoleUnconfirmed},
		"workflow_run gated, head_branch": {wf([]string{"workflow_run"}, nil, "github.event.workflow_run.head_branch == 'main'"), RoleProduction},
		// A CD workflow gated on head_branch, ignorable terms around it.
		"workflow_run gated": {wf([]string{"workflow_dispatch", "workflow_run"}, nil,
			"github.event_name == 'workflow_dispatch' || (github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && github.event.workflow_run.head_branch == 'main')"), RoleProduction},
	} {
		if got := judge(c.w, "main"); got.Role != c.want {
			t.Errorf("%s: role %q (%s, unreadable %v), want %q", name, got.Role, got.Detail, got.Unreadable, c.want)
		}
	}
	// after_branches on workflow_run count as a branch filter.
	w := wf([]string{"workflow_run"}, nil)
	w["after_branches"] = fact.Strings([]string{"main"})
	if got := judge(w, "main"); got.Role != RoleProduction {
		t.Errorf("workflow_run after main: %+v", got)
	}
	w["after_branches"] = fact.Strings([]string{"**", "!main"})
	if got := judge(w, "main"); got.Role != RoleNone {
		t.Errorf("workflow_run after all but main: %+v", got)
	}
}

// needs adds the if: of a job the deploy job needs.
func needs(w map[string]any, job, cond string) map[string]any {
	cs, _ := w["conditions"].([]any)
	w["conditions"] = append([]any{map[string]any{"job": job, "if": cond}}, cs...)
	return w
}

// step adds the if: of a deploy step, recorded as the scanner does.
func step(w map[string]any, name, cond string) map[string]any {
	cs, _ := w["conditions"].([]any)
	w["conditions"] = append(cs, map[string]any{"job": "deploy", "step": name, "if": cond})
	return w
}

func TestJudgeConditions(t *testing.T) {
	push := []string{"push"}
	all := []string{"*"}
	run := []string{"workflow_run"}
	for name, c := range map[string]struct {
		w          map[string]any
		want       string
		unreadable []string
	}{
		// Every job in the needs chain must run on main.
		"needed job on dev":  {needs(wf(push, all, "github.ref == 'refs/heads/main'"), "build", "github.ref == 'refs/heads/dev'"), RoleNone, nil},
		"needed job on main": {needs(wf(push, all, "github.ref_name == 'main'"), "build", "github.ref == 'refs/heads/main'"), RoleProduction, nil},
		"needed job unread":  {needs(wf(push, all), "build", "needs.plan.outputs.go == 'true'"), RoleUnconfirmed, []string{"needs.plan.outputs.go == 'true'"}},
		// A step's if: is recorded under its job beside the job's own.
		"step on dev":  {wf(push, all, "github.ref == 'refs/heads/main'", "github.ref_name == 'dev'"), RoleNone, nil},
		"step on main": {wf(push, all, "github.event_name == 'push'", "github.ref_name == 'main'"), RoleProduction, nil},
		// With step set, as the scanner records a step's if:.
		"named step on dev":    {step(wf(push, all, "github.ref == 'refs/heads/main'"), "Up", "github.ref_name == 'dev'"), RoleNone, nil},
		"named step on main":   {step(wf(push, all), "#2", "github.ref_name == 'main'"), RoleProduction, nil},
		"named step unread":    {step(wf(push, []string{"main"}), "Up", "needs.plan.outputs.go == 'true'"), RoleUnconfirmed, []string{"needs.plan.outputs.go == 'true'"}},
		"job main, step other": {step(needs(wf(push, all), "build", "github.ref_name == 'main'"), "Up", "github.ref_name != 'main'"), RoleNone, nil},
		// || and && as written, not a bag of terms.
		"or admits main":     {wf(push, []string{"main"}, "github.event_name == 'push' || github.ref == 'refs/heads/dev'"), RoleProduction, nil},
		"and excludes main":  {wf(push, all, "github.ref == 'refs/heads/main' && github.ref == 'refs/heads/dev'"), RoleNone, nil},
		"or with dev":        {wf(push, all, "github.ref == 'refs/heads/main' || github.ref == 'refs/heads/dev'"), RoleProduction, nil},
		"dispatch only job":  {wf(push, []string{"main"}, "github.event_name == 'workflow_dispatch'"), RoleNone, nil},
		"not equal":          {wf(push, all, "github.ref != 'refs/heads/main'"), RoleNone, nil},
		"negated":            {wf(push, all, "!(github.ref_name == 'main')"), RoleNone, nil},
		"negated other":      {wf(push, []string{"main"}, "${{ !(github.ref_name == 'dev') }}"), RoleProduction, nil},
		"case and quotes":    {wf(push, all, `github.ref == "refs/heads/MAIN"`), RoleProduction, nil},
		"tag ref":            {wf(push, all, "github.ref == 'refs/tags/v1'"), RoleNone, nil},
		"literal true":       {wf(push, []string{"main"}, "true"), RoleProduction, nil},
		"unknown and main":   {wf(push, all, "github.ref == 'refs/heads/main' && needs.plan.outputs.go == 'true'"), RoleUnconfirmed, []string{"needs.plan.outputs.go == 'true'"}},
		"unknown but dev":    {wf(push, all, "github.ref == 'refs/heads/dev' && needs.plan.outputs.go == 'true'"), RoleNone, []string{"needs.plan.outputs.go == 'true'"}},
		"function":           {wf(push, []string{"main"}, "startsWith(github.ref, 'refs/tags/')"), RoleUnconfirmed, []string{"startsWith(github.ref, 'refs/tags/')"}},
		"! before ==":        {wf(push, []string{"main"}, "!github.ref == 'x'"), RoleUnconfirmed, []string{"!github.ref == 'x'"}},
		"not parsed":         {wf(push, []string{"main"}, `github.ref ==   "refs/heads/main" &&`), RoleUnconfirmed, []string{"github.ref == 'refs/heads/main' &&"}},
		"run gated other":    {wf(run, nil, "github.event.workflow_run.head_branch == 'dev'"), RoleNone, nil},
		"run gated two":      {wf(run, nil, "github.event.workflow_run.head_branch == 'main' || github.event.workflow_run.head_branch == 'dev'"), RoleUnconfirmed, nil},
		"run gated negation": {wf(run, nil, "github.event.workflow_run.head_branch != 'dev'"), RoleUnconfirmed, nil},
		// workflow_run runs on the default branch whatever branch started
		// it, so github.ref is no gate there.
		"run gated on ref": {wf(run, nil, "github.ref == 'refs/heads/main'"), RoleUnconfirmed, nil},
		// The push run skips a job that needs a workflow_run; the
		// workflow_run is not gated.
		"push and run": {wf([]string{"push", "workflow_run"}, []string{"main"}, "github.event.workflow_run.conclusion == 'success'"), RoleUnconfirmed, nil},
	} {
		got := judge(c.w, "main")
		if got.Role != c.want || fmt.Sprint(got.Unreadable) != fmt.Sprint(c.unreadable) {
			t.Errorf("%s: role %q (%s), unreadable %q; want %q, %q", name, got.Role, got.Detail, got.Unreadable, c.want, c.unreadable)
		}
	}
}

func TestParseIf(t *testing.T) {
	for in, want := range map[string]string{
		"a == 'b' && (c || !d)":    "a == 'b' && (c || !d)",
		"!(a == 'b') || !(c && d)": "!(a == 'b') || !(c && d)",
		`${{ x != "it's" }}`:       "x != 'it''s'",
		"f(a, 'b') || g()":         "f(a, 'b') || g()",
		"github.event.inputs['x']": "github.event.inputs['x']",
	} {
		n, err := parseIf(in)
		if err != nil || n.text() != want {
			t.Errorf("parseIf(%q) = %v, %v; want %q", in, n, err, want)
			continue
		}
	}
	for _, bad := range []string{"a ==", "(a", "'x", "a b", "a & b"} {
		if _, err := parseIf(bad); err == nil {
			t.Errorf("parseIf(%q): no error", bad)
		}
	}
}

func svc(key string, attrs map[string]any) fact.Fact {
	return fact.Fact{Kind: fact.KindService, Key: key, Attrs: attrs}
}

func recipe(df, target string, final bool) fact.Fact {
	return fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:" + df + ":" + target,
		Attrs: map[string]any{"dockerfile": df, "target": target, "final": final, "sources": fact.Strings([]string{"backend"})}}
}

func build(repo, df, target string) fact.Fact {
	a := map[string]any{"image_repo": repo, "dockerfile": df, "dockerfile_resolved": true}
	if target != "" {
		a["target"] = target
	}
	return fact.Fact{Kind: fact.KindImageBuild, Key: "image_build:" + repo + ":" + df + ":" + target, Attrs: a}
}

func TestLink(t *testing.T) {
	s := &fact.Scan{Facts: []fact.Fact{
		recipe("backend/Dockerfile", "api", false),
		recipe("backend/Dockerfile", "worker", true),
		build("api", "backend/Dockerfile", "api"),
		build("worker", "backend/Dockerfile", "web"),
		// Both paths agree.
		svc("service:compose:p:api", map[string]any{"image": "${REG}/acme/api:${TAG:-1}", "dockerfile": "backend/Dockerfile", "target": "api"}),
		// No target: the final stage.
		svc("service:compose:p:worker", map[string]any{"dockerfile": "backend/Dockerfile"}),
		// The build path points elsewhere than the compose build: conflict.
		svc("service:compose:p:mixed", map[string]any{"image": "acme/api", "dockerfile": "backend/Dockerfile", "target": "worker"}),
		// A build for this repo exists but names no recipe.
		svc("service:compose:p:broken", map[string]any{"image": "acme/worker:2"}),
		svc("service:compose:p:caddy", map[string]any{"image": "caddy:2"}),
	}}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	got := map[string]string{}
	for _, l := range m.Links {
		got[l.Service] += fmt.Sprint(l.Recipe, l.Via)
	}
	for k, want := range map[string]string{
		"service:compose:p:api":    "image_recipe:backend/Dockerfile:api[compose_build image_build]",
		"service:compose:p:worker": "image_recipe:backend/Dockerfile:worker[compose_build]",
		"service:compose:p:mixed":  "image_recipe:backend/Dockerfile:api[image_build]image_recipe:backend/Dockerfile:worker[compose_build]",
	} {
		if got[k] != want {
			t.Errorf("%s links = %q, want %q", k, got[k], want)
		}
	}
	findings := map[string]string{}
	for _, f := range m.Findings {
		findings[f.Subject] = f.Rule
	}
	if findings["service:compose:p:mixed"] != FindingLinkConflict || findings["service:compose:p:broken"] != FindingNoCodeLink || len(findings) != 2 {
		t.Errorf("findings = %v", findings)
	}
	if len(m.Images) != 1 || m.Images[0].Service != "service:compose:p:caddy" || m.Images[0].ImageRepo != "caddy" {
		t.Errorf("images = %+v", m.Images)
	}
}

// Sources are written relative to the build context; a dependency is attached
// when its manifest lies in them, whatever its scope.
func TestAttach(t *testing.T) {
	dep := func(key, path, scope string) fact.Fact {
		return fact.Fact{Kind: fact.KindDependency, Key: key, Attrs: map[string]any{"scope": scope},
			Evidence: []fact.Evidence{{Path: path, Line: 3}}}
	}
	r := fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:web/Dockerfile:_final",
		Attrs: map[string]any{"dockerfile": "web/Dockerfile", "target": "_final", "final": true,
			"sources": fact.Strings([]string{"package.json", "src"})}}
	s := &fact.Scan{Facts: []fact.Fact{r,
		svc("service:compose:p:web", map[string]any{"build_context": "web", "dockerfile": "web/Dockerfile"}),
		dep("dependency:npm:web:next", "web/package.json", "prod"),
		dep("dependency:npm:web:typescript", "web/package.json", "dev"),
		dep("dependency:npm:docs:astro", "docs/package.json", "prod"),
	}}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	if len(m.Links) != 1 || fmt.Sprint(m.Links[0].Sources) != "[web/package.json web/src]" {
		t.Fatalf("links = %+v", m.Links)
	}
	var got []string
	for _, a := range m.Attached {
		got = append(got, a.Fact+"/"+a.Scope+"/"+a.Source)
	}
	if fmt.Sprint(got) != "[dependency:npm:web:next/prod/web/package.json dependency:npm:web:typescript/dev/web/package.json]" {
		t.Errorf("attached = %v", got)
	}
}

func TestPins(t *testing.T) {
	ref := func(raw, pin string) map[string]any { return map[string]any{"raw": raw, "pinning": pin} }
	r := fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:b/Dockerfile:api", Attrs: map[string]any{
		"dockerfile": "b/Dockerfile", "target": "api", "base_images": []any{
			map[string]any{"ref": "alpine:3.21", "role": "from", "stage": "api", "artifact": ref("alpine:3.21", "floating")},
			map[string]any{"ref": "golang:1.27.3", "role": "from", "stage": "builder", "artifact": ref("golang:1.27.3", "exact")},
		}}}
	builder := fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:b/Dockerfile:builder", Attrs: map[string]any{
		"dockerfile": "b/Dockerfile", "target": "builder", "base_images": []any{
			map[string]any{"ref": "golang:1.27.3", "role": "from", "stage": "builder", "artifact": ref("golang:1.27.3", "exact")},
		}}}
	s := &fact.Scan{Facts: []fact.Fact{r, builder,
		{Kind: fact.KindDatastore, Key: "datastore:compose:p:db", Attrs: map[string]any{"image_ref": ref("postgres:17", "floating")}},
		{Kind: fact.KindService, Key: "service:compose:p:x", Attrs: map[string]any{"image_ref": ref("x@sha256:1", "digest")}},
	}}
	var got []string
	for _, f := range Derive(s, "x", Settings{DefaultBranch: "main"}).Findings {
		got = append(got, f.Subject+" "+f.Grade)
	}
	if fmt.Sprint(got) != "[datastore:compose:p:db floating image_recipe:b/Dockerfile:api floating image_recipe:b/Dockerfile:builder exact]" {
		t.Errorf("findings = %v", got)
	}
}

// Compose env evidence goes on the service its unit names, in each stack that
// uses that compose file; example-file evidence goes nowhere.
func TestAttachCompose(t *testing.T) {
	stack := func(name string, files ...string) fact.Fact {
		return fact.Fact{Kind: fact.KindStack, Key: "stack:compose:" + name, Attrs: map[string]any{"name": name, "tool": "compose", "files": fact.Strings(files)}}
	}
	s := &fact.Scan{Facts: []fact.Fact{
		stack("prod", "docker-compose.yml", "docker-compose.prod.yml"),
		stack("local", "docker-compose.yml"),
		svc("service:compose:prod:api", map[string]any{"name": "api", "stack": "prod"}),
		svc("service:compose:local:api", map[string]any{"name": "api", "stack": "local"}),
		{Kind: fact.KindEnvVar, Key: "env_var:SENTRY_DSN", Attrs: map[string]any{"name": "SENTRY_DSN"},
			Evidence: []fact.Evidence{{Path: "docker-compose.prod.yml", Line: 4, Unit: "api"}, {Path: ".env.example", Line: 2}}},
		{Kind: fact.KindEnvVar, Key: "env_var:DB_URL", Attrs: map[string]any{"name": "DB_URL"},
			Evidence: []fact.Evidence{{Path: "docker-compose.yml", Line: 9, Unit: "api"}}},
	}}
	var got []string
	for _, a := range Derive(s, "x", Settings{DefaultBranch: "main"}).Attached {
		got = append(got, a.Service+" "+a.Fact+" "+a.Source)
	}
	want := "[service:compose:local:api env_var:DB_URL docker-compose.yml service:compose:prod:api env_var:DB_URL docker-compose.yml service:compose:prod:api env_var:SENTRY_DSN docker-compose.prod.yml]"
	if fmt.Sprint(got) != want {
		t.Errorf("attached = %v", got)
	}
}

func TestCatalogue(t *testing.T) {
	c := LoadCatalogue()
	ids := map[string]bool{}
	for _, e := range c.Entries {
		if ids[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		ids[e.ID] = true
		if e.Name == "" || e.Category == "" || e.Checked == "" || e.Review == "" || (e.Kind != "provider" && e.Kind != "protocol") {
			t.Errorf("%s: incomplete entry %+v", e.ID, e)
		}
		if len(e.Sources) < len(e.SDKs) || len(e.Sources) == 0 {
			t.Errorf("%s: every SDK and host needs its source", e.ID)
		}
		for _, s := range e.SDKs {
			if s.Ecosystem != "go" && s.Ecosystem != "npm" && s.Ecosystem != "pypi" && s.Ecosystem != "nuget" {
				t.Errorf("%s: ecosystem %s", e.ID, s.Ecosystem)
			}
		}
	}
	for _, e := range c.Entries {
		for _, r := range e.ResolvedBy {
			if !ids[r] {
				t.Errorf("%s: resolved_by %s is not an entry", e.ID, r)
			}
		}
	}
	if c.Version == "" || len(c.Entries) < 15 {
		t.Errorf("catalogue %s with %d entries", c.Version, len(c.Entries))
	}
}

// Two signal kinds confirm a party, one leaves it single (and a finding); a
// protocol served by a data store in the stack is internal, one a provider
// names folds into it; unclaimed host hints are unknown.
func TestParties(t *testing.T) {
	dep := func(eco, name string) fact.Fact {
		return fact.Fact{Kind: fact.KindDependency, Key: "dependency:" + eco + ":backend:" + name, Attrs: map[string]any{"ecosystem": eco, "name": name},
			Evidence: []fact.Evidence{{Path: "backend/go.mod", Line: 3}}}
	}
	env := func(name string, hosts ...string) fact.Fact {
		a := map[string]any{"name": name}
		if hosts != nil {
			a["host_hints"] = fact.Strings(hosts)
		}
		return fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + name, Attrs: a, Evidence: []fact.Evidence{{Path: "backend/config.go", Line: 1}}}
	}
	recipe := fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:backend/Dockerfile:_final",
		Attrs: map[string]any{"dockerfile": "backend/Dockerfile", "target": "_final", "final": true, "sources": fact.Strings([]string{"."})}}
	api := func(stack string) fact.Fact {
		return svc("service:compose:"+stack+":api", map[string]any{"name": "api", "stack": stack, "build_context": "backend", "dockerfile": "backend/Dockerfile"})
	}
	s := &fact.Scan{Facts: []fact.Fact{recipe, api("prod"), api("aws"),
		{Kind: fact.KindDatastore, Key: "datastore:compose:prod:minio", Attrs: map[string]any{"name": "minio", "stack": "prod", "engine": "minio"}},
		svc("service:compose:prod:tunnel", map[string]any{"name": "tunnel", "stack": "prod", "image_ref": map[string]any{"repository": "cloudflare/cloudflared"}}),
		dep("go", "github.com/getsentry/sentry-go"), env("SENTRY_DSN"),
		dep("go", "github.com/aws/aws-sdk-go-v2/service/s3"), env("S3_ENDPOINT"), env("AWS_REGION", "s3.eu-west-1.amazonaws.com"),
		env("CHIFT_CLIENT_ID"), env("SMTP_HOST"), env("PUBLIC_URL", "app.example.be"),
		dep("npm", "stripe-mock"), // not stripe: npm names match exactly
	}}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	got := map[string]string{}
	for _, p := range m.Parties {
		got[p.ID] = p.Grade + " " + fmt.Sprint(p.Services)
	}
	for id, want := range map[string]string{
		"sentry":     "confirmed [service:compose:aws:api service:compose:prod:api]",
		"chift":      "single [service:compose:aws:api service:compose:prod:api]",
		"smtp":       "single [service:compose:aws:api service:compose:prod:api]",
		"aws":        "confirmed [service:compose:aws:api service:compose:prod:api]", // SDK and env: two facts
		"cloudflare": "single [service:compose:prod:tunnel]",
	} {
		if got[id] != want {
			t.Errorf("%s = %q, want %q", id, got[id], want)
		}
	}
	// S3 on prod is MinIO; on aws the AWS match names it: no s3 party at all.
	if _, ok := got["s3"]; ok {
		t.Errorf("s3 listed as outside party: %v", got)
	}
	if _, ok := got["stripe"]; ok {
		t.Error("stripe-mock matched stripe")
	}
	// The MinIO data store is not a user of the S3 it serves, and an S3 name
	// on no service adds nothing once S3 resolved inside.
	m2 := Derive(&fact.Scan{Facts: append(append([]fact.Fact(nil), s.Facts...),
		fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:S3_ONLY_IN_EXAMPLE", Attrs: map[string]any{"name": "S3_ONLY_IN_EXAMPLE"},
			Evidence: []fact.Evidence{{Path: ".env.example", Line: 9}}})}, "x", Settings{DefaultBranch: "main"})
	for _, p := range m2.Parties {
		if p.ID == "s3" {
			t.Errorf("s3 party from a fact on no service: %+v", p)
		}
	}
	if len(m.Internal) != 1 || m.Internal[0].Service != "service:compose:prod:api" || m.Internal[0].Datastore != "datastore:compose:prod:minio" {
		t.Errorf("internal = %+v", m.Internal)
	}
	if len(m.Unknown) != 1 || m.Unknown[0].Host != "app.example.be" {
		t.Errorf("unknown = %+v", m.Unknown)
	}
	n := 0
	for _, f := range m.Findings {
		if f.Rule == FindingPartyUnconfirmed {
			n++
		}
	}
	if n != 3 {
		t.Errorf("party_unconfirmed findings = %d, want 3 (chift, smtp, cloudflare)", n)
	}
}

// An outbound host the code requests confirms a party with another signal; a
// mere mention never makes one and goes to the unknown bucket as a mention.
func TestOutboundSignals(t *testing.T) {
	host := func(h, usage string) fact.Fact {
		return fact.Fact{Kind: fact.KindOutboundHost, Key: "outbound_host:" + h, Attrs: map[string]any{"host": h, "usage": usage, "via": "literal_url"},
			Evidence: []fact.Evidence{{Path: "backend/x.go", Line: 3}}}
	}
	s := &fact.Scan{Facts: []fact.Fact{
		host("api.chift.eu", "default"),
		{Kind: fact.KindEnvVar, Key: "env_var:CHIFT_CLIENT_ID", Attrs: map[string]any{"name": "CHIFT_CLIENT_ID"}, Evidence: []fact.Evidence{{Path: ".env.example", Line: 1}}},
		host("api.stripe.com", "mention"),
		host("www.example-news.com", "request"),
	}}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	var parties, unknown []string
	for _, p := range m.Parties {
		parties = append(parties, p.ID+" "+p.Grade)
	}
	for _, u := range m.Unknown {
		unknown = append(unknown, u.Host+" "+u.Usage)
	}
	if fmt.Sprint(parties) != "[chift confirmed]" {
		t.Errorf("parties = %v", parties)
	}
	if fmt.Sprint(unknown) != "[api.stripe.com mention www.example-news.com request]" {
		t.Errorf("unknown = %v", unknown)
	}
}

// Declared own domains take hosts out of the unknown bucket; a declared stack
// role replaces the derived one, keeps it beside, and ends the unconfirmed
// finding.
func TestDeclared(t *testing.T) {
	s := &fact.Scan{
		Config: &fact.ScanConfig{Declared: &fact.Declared{
			OwnDomains: []fact.DeclaredDomain{{Domain: "acme-widgets.be", By: "repo", Line: 3}},
			Stacks:     []fact.DeclaredStack{{Stack: "prod", Role: "production", Name: "Live", By: "operator"}},
		}},
		Facts: []fact.Fact{
			{Kind: fact.KindEnvVar, Key: "env_var:URL", Attrs: map[string]any{"name": "URL", "host_hints": fact.Strings([]string{"app.acme-widgets.be", "app.acme-labs.be"})}},
			{Kind: fact.KindStack, Key: "stack:compose:prod", Attrs: map[string]any{"name": "prod", "tool": "compose", "workflows": []any{
				wf([]string{"push"}, []string{"main"}, "needs.plan.outputs.go == 'true'")}}},
		},
	}
	m := Derive(s, "x", Settings{DefaultBranch: "main"})
	if len(m.Own) != 1 || m.Own[0].Host != "app.acme-widgets.be" || m.Own[0].Declared.Line != 3 {
		t.Errorf("own = %+v", m.Own)
	}
	if len(m.Unknown) != 1 || m.Unknown[0].Host != "app.acme-labs.be" {
		t.Errorf("unknown = %+v", m.Unknown)
	}
	st := m.Stacks[0]
	if st.Role != RoleProduction || st.DerivedRole != RoleUnconfirmed || st.Name != "Live" || st.Declared == nil || st.Declared.By != "operator" {
		t.Errorf("stack = %+v", st)
	}
	for _, f := range m.Findings {
		if f.Rule == FindingProductionUnconfirmed {
			t.Error("a declared role still raised production_unconfirmed")
		}
	}
}

// A route from container labels is placed in each stack whose compose files
// hold it, through the service running the proxy image it names.
func TestLabelRoutes(t *testing.T) {
	svc := func(stack, name, repo string) fact.Fact {
		return fact.Fact{Kind: fact.KindService, Key: "service:compose:" + stack + ":" + name,
			Attrs: map[string]any{"name": name, "stack": stack, "image_ref": map[string]any{"repository": repo}}}
	}
	facts := []fact.Fact{
		{Kind: fact.KindStack, Key: "stack:compose:edge", Attrs: map[string]any{"name": "edge", "files": []any{"compose.yaml", "compose.edge.yaml"}}},
		{Kind: fact.KindStack, Key: "stack:compose:dev", Attrs: map[string]any{"name": "dev", "files": []any{"compose.yaml"}}},
		svc("edge", "proxy", "library/traefik"), svc("edge", "api", ""), svc("dev", "api", ""),
		{Kind: fact.KindRoute, Key: "route:traefik:compose.edge.yaml:api", Attrs: map[string]any{"file": "compose.edge.yaml", "action": "proxy",
			"labels_on": "api", "proxy_image": "traefik", "addresses": []any{map[string]any{"address": "api.example.net", "host": "api.example.net"}},
			"upstreams": []any{map[string]any{"address": "api:8000", "host": "api", "port": "8000"}}}},
	}
	m := &Map{}
	routes(m, facts)
	if len(m.Routes) != 1 || m.Routes[0].Via != "service:compose:edge:proxy" || m.Routes[0].To != "service:compose:edge:api" {
		t.Errorf("routes = %+v", m.Routes)
	}
}

var (
	catalogueID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	catalogueEnv  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)
	catalogueHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
	// Prefixes that name no provider: any repo has variables starting with them.
	genericEnv = map[string]bool{"API_": true, "APP_": true, "AUTH_": true, "CLOUD_": true, "DB_": true, "LOG_": true,
		"MAIL_": true, "PAY_": true, "SMS_": true, "SECRET_": true, "TOKEN_": true, "URL_": true}
	catalogueCategories = map[string]bool{"AI": true, "payments": true, "email": true, "messaging": true, "identity": true,
		"observability": true, "error tracking": true, "analytics": true, "storage": true, "cloud": true, "hosting": true,
		"edge": true, "database": true, "search": true, "maps": true, "accounting": true, "code hosting": true,
		"captcha": true, "CMS": true, "feature flags": true, "support": true, "CRM": true, "e-signature": true, "tax": true,
		"banking": true, "notifications": true, "video": true, "scheduling": true, "other": true}
)

// The catalogue is data a customer's map depends on: entries must be well
// formed, and no two may claim the same signal, or one fact would name two
// parties. A protocol may share signals with a provider that resolves it.
func TestCatalogueNoOverlap(t *testing.T) {
	c := LoadCatalogue()
	shares := func(a, b Entry) bool {
		return slices.Contains(a.ResolvedBy, b.ID) || slices.Contains(b.ResolvedBy, a.ID)
	}
	for _, e := range c.Entries {
		if !catalogueID.MatchString(e.ID) || !catalogueCategories[e.Category] {
			t.Errorf("%s: id or category %q", e.ID, e.Category)
		}
		for _, h := range e.Hosts {
			if !catalogueHost.MatchString(h) {
				t.Errorf("%s: host %q is not a bare lower-case domain", e.ID, h)
			}
		}
		for _, p := range e.EnvPrefixes {
			if !catalogueEnv.MatchString(p) || genericEnv[p] {
				t.Errorf("%s: env prefix %q", e.ID, p)
			}
		}
	}
	for i, a := range c.Entries {
		for _, b := range c.Entries[i+1:] {
			if shares(a, b) {
				continue
			}
			for _, x := range a.Hosts {
				for _, y := range b.Hosts {
					if x == y || strings.HasSuffix(x, "."+y) || strings.HasSuffix(y, "."+x) {
						t.Errorf("%s and %s share host %s / %s", a.ID, b.ID, x, y)
					}
				}
			}
			for _, x := range a.EnvPrefixes {
				for _, y := range b.EnvPrefixes {
					if strings.HasPrefix(x, y) || strings.HasPrefix(y, x) {
						t.Errorf("%s and %s share env prefix %s / %s", a.ID, b.ID, x, y)
					}
				}
			}
			for _, x := range a.SDKs {
				for _, y := range b.SDKs {
					if x.Ecosystem != y.Ecosystem {
						continue
					}
					n, m := x.Name, y.Name
					same := n == m
					switch x.Ecosystem {
					case "go":
						same = same || strings.HasPrefix(n, m+"/") || strings.HasPrefix(m, n+"/")
					case "npm":
						same = same || strings.HasSuffix(n, "/") && strings.HasPrefix(m, n) || strings.HasSuffix(m, "/") && strings.HasPrefix(n, m)
					case "pypi":
						same = pypiName(n) == pypiName(m)
					}
					if same {
						t.Errorf("%s and %s share SDK %s %s / %s", a.ID, b.ID, x.Ecosystem, n, m)
					}
				}
			}
		}
	}
}

// Confirmed needs two signal kinds from two facts: one env var whose example
// value names the provider's host is still one fact.
func TestGrade(t *testing.T) {
	env := func(name string, hosts ...string) fact.Fact {
		a := map[string]any{"name": name}
		if hosts != nil {
			a["host_hints"] = fact.Strings(hosts)
		}
		return fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + name, Attrs: a, Evidence: []fact.Evidence{{Path: ".env.example", Line: 1}}}
	}
	sdk := fact.Fact{Kind: fact.KindDependency, Key: "dependency:go:backend:github.com/getsentry/sentry-go",
		Attrs: map[string]any{"ecosystem": "go", "name": "github.com/getsentry/sentry-go"}, Evidence: []fact.Evidence{{Path: "backend/go.mod", Line: 3}}}
	host := fact.Fact{Kind: fact.KindOutboundHost, Key: "outbound_host:o1.ingest.sentry.io",
		Attrs: map[string]any{"host": "o1.ingest.sentry.io", "usage": "request", "via": "literal_url"}, Evidence: []fact.Evidence{{Path: "backend/x.go", Line: 3}}}
	for name, c := range map[string]struct {
		facts   []fact.Fact
		grade   string
		signals string
		detail  string
	}{
		"env with host hint alone": {[]fact.Fact{env("SENTRY_DSN", "o1.ingest.sentry.io")}, GradeSingle,
			"[env host]", "Sentry rests on one fact (env_var:SENTRY_DSN); confirm once"},
		"env alone":         {[]fact.Fact{env("SENTRY_DSN")}, GradeSingle, "[env]", "Sentry rests on one kind of signal (env); confirm once"},
		"two env facts":     {[]fact.Fact{env("SENTRY_DSN"), env("SENTRY_ENV")}, GradeSingle, "[env env]", "Sentry rests on one kind of signal (env); confirm once"},
		"sdk and env":       {[]fact.Fact{sdk, env("SENTRY_DSN")}, GradeConfirmed, "[env sdk]", ""},
		"env hint and sdk":  {[]fact.Fact{sdk, env("SENTRY_DSN", "o1.ingest.sentry.io")}, GradeConfirmed, "[env host sdk]", ""},
		"env and requested": {[]fact.Fact{env("SENTRY_DSN"), host}, GradeConfirmed, "[env host]", ""},
	} {
		m := Derive(&fact.Scan{Facts: c.facts}, "x", Settings{DefaultBranch: "main"})
		if len(m.Parties) != 1 || m.Parties[0].ID != "sentry" {
			t.Errorf("%s: parties = %+v", name, m.Parties)
			continue
		}
		p := m.Parties[0]
		var kinds []string
		for _, s := range p.Signals {
			kinds = append(kinds, s.Kind)
		}
		detail := ""
		for _, f := range m.Findings {
			if f.Rule == FindingPartyUnconfirmed {
				detail = f.Detail
			}
		}
		if p.Grade != c.grade || fmt.Sprint(kinds) != c.signals || detail != c.detail {
			t.Errorf("%s: grade %s signals %v finding %q, want %s %s %q", name, p.Grade, kinds, detail, c.grade, c.signals, c.detail)
		}
	}
}

// NuGet ids are case-insensitive: a lower-cased id still names its engine.
func TestDriversNuget(t *testing.T) {
	dep := fact.Fact{Kind: fact.KindDependency, Key: "dependency:nuget:src/Api:microsoft.entityframeworkcore.sqlserver",
		Attrs:    map[string]any{"ecosystem": "nuget", "name": "microsoft.entityframeworkcore.sqlserver", "scope": "prod"},
		Evidence: []fact.Evidence{{Path: "src/Api/Api.csproj", Line: 3}}}
	count := func(facts ...fact.Fact) (n int, detail string) {
		m := Derive(&fact.Scan{Facts: facts}, "x", Settings{DefaultBranch: "main"})
		for _, f := range m.Findings {
			if f.Rule == FindingDriverWithoutStore {
				n++
				detail = f.Detail
			}
		}
		return
	}
	n, detail := count(dep)
	if n != 1 || !strings.Contains(detail, "sqlserver") {
		t.Errorf("without a store: %d findings, %q", n, detail)
	}
	store := fact.Fact{Kind: fact.KindDatastore, Key: "datastore:compose:prod:db",
		Attrs: map[string]any{"name": "db", "stack": "prod", "engine": "sqlserver"}}
	if n, _ := count(dep, store); n != 0 {
		t.Errorf("with a sqlserver store: %d findings", n)
	}
}

// NuGet ids are case-insensitive and a name ending in ".*" is a family.
func TestPartyNuget(t *testing.T) {
	dep := func(eco, name string) fact.Fact {
		return fact.Fact{Kind: fact.KindDependency, Key: "dependency:" + eco + ":app:" + name, Attrs: map[string]any{"ecosystem": eco, "name": name},
			Evidence: []fact.Evidence{{Path: "app/App.csproj", Line: 3}}}
	}
	has := func(facts ...fact.Fact) map[string]bool {
		m := Derive(&fact.Scan{Facts: facts}, "x", Settings{DefaultBranch: "main"})
		got := map[string]bool{}
		for _, p := range m.Parties {
			got[p.ID] = true
		}
		return got
	}
	if got := has(dep("nuget", "mollie.api")); !got["mollie"] {
		t.Errorf("mollie.api did not give mollie: %v", got)
	}
	if got := has(dep("nuget", "AWSSDK.S3")); !got["aws"] {
		t.Errorf("AWSSDK.S3 did not match the AWSSDK.* family: %v", got)
	}
	if got := has(dep("nuget", "awssdk.core")); !got["aws"] {
		t.Errorf("awssdk.core did not match the AWSSDK.* family: %v", got)
	}
	if got := has(dep("nuget", "AWSSDKX")); got["aws"] {
		t.Errorf("AWSSDKX matched the AWSSDK.* family: %v", got)
	}
	// Only nuget names get family matching: an npm name ending in "*" is exact.
	e := Entry{SDKs: []SDK{{Ecosystem: "npm", Name: "stripe*"}}}
	if e.matchSDK(dep("npm", "stripe-mock")) || e.matchSDK(dep("npm", "stripe")) {
		t.Error("an npm SDK ending in * got family matching")
	}
}
