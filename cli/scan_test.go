package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/rules"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	fixtureSHA = "0123456789abcdef0123456789abcdef01234567"
	fixtureAt  = "2026-10-14T16:02:11Z"
)

func scanFixture(t *testing.T) []byte { return scanDir(t, "fixture") }

// goldenFixtures are scanned and compared byte for byte with
// testdata/<name>.golden.json.
var goldenFixtures = []string{"fixture", "fixture-commands", "fixture-dockerfile", "fixture-caddy", "fixture-prisma", "fixture-migrations", "fixture-ci", "fixture-k8s", "fixture-sourced", "fixture-dotnet"}

func scanDir(t *testing.T, name string) []byte {
	t.Helper()
	s, warnings, err := pipeline.ScanWorkTree(filepath.Join("testdata", name), "local/"+name, fixtureSHA, fixtureAt, plugin.All(), config.Operator{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	data, err := fact.Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// Same input, same binary: byte-identical output. Run it a few times so map
// iteration order would show up.
func TestDeterministic(t *testing.T) {
	for _, name := range goldenFixtures {
		first := scanDir(t, name)
		for i := 0; i < 20; i++ {
			if !bytes.Equal(first, scanDir(t, name)) {
				t.Fatalf("%s: run %d differs from run 0", name, i+1)
			}
		}
	}
}

func TestGolden(t *testing.T) {
	for _, name := range goldenFixtures {
		got := scanDir(t, name)
		golden := filepath.Join("testdata", name+".golden.json")
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run go test -update once)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("output differs from %s; run go test -update and review the diff", golden)
		}
	}
}

func TestFacts(t *testing.T) {
	var s fact.Scan
	if err := json.Unmarshal(scanFixture(t), &s); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]fact.Fact{}
	for _, f := range s.Facts {
		if len(f.Evidence) == 0 {
			t.Errorf("%s has no evidence", f.Key)
		}
		byKey[f.Key] = f
	}

	// No invocation: convention stacks, the bare `docker compose up` one and
	// base + override.
	if st := byKey["stack:compose:prod"]; st.Attrs["source"] != "convention" || fmt.Sprint(st.Attrs["files"]) != "[docker-compose.yml docker-compose.prod.yml]" {
		t.Errorf("prod stack: %v", st.Attrs)
	}
	// Data stores are recognised from the image, version from the tag.
	pg := byKey["datastore:compose:prod:postgres"]
	if pg.Attrs["engine"] != "postgres" || pg.Attrs["version"] != "16" || pg.Attrs["stack"] != "prod" {
		t.Errorf("postgres: %v", pg.Attrs)
	}
	// Every file of the stack that defines the service is evidence.
	if len(pg.Evidence) != 2 {
		t.Errorf("postgres evidence: want base + override, got %v", pg.Evidence)
	}
	// YAML merge keys resolve: api gets its build context from the anchor.
	// The override's image applies to its own stack only.
	api := byKey["service:compose:prod:api"]
	if api.Attrs["build_context"] != "backend" || api.Attrs["image"] != "ghcr.io/example/api:1.4.0" {
		t.Errorf("prod api: %v", api.Attrs)
	}
	if def := byKey["service:compose:default:api"]; def.Attrs["image"] != nil || def.Attrs["build_context"] != "backend" {
		t.Errorf("default api: the prod override leaked: %v", def.Attrs)
	}
	// An image without build that is not a data store is a service.
	if byKey["service:compose:default:caddy"].Attrs["image"] != "caddy:2" {
		t.Errorf("caddy: %v", byKey["service:compose:default:caddy"])
	}
	// Indirect Go requirements are transitive dependencies.
	if f, ok := byKey["dependency:go:backend:golang.org/x/crypto"]; !ok || f.Attrs["direct"] != false || f.Evidence[0].Unit != "" {
		t.Errorf("indirect go dependency: %v", f)
	}
	if f := byKey["dependency:go:backend:github.com/sashabaranov/go-openai"]; f.Evidence[0].Line != 8 || f.Evidence[0].Unit != "backend" {
		t.Errorf("go-openai evidence: %v", f.Evidence)
	}
	// npm: exact version from the lockfile, declared range when the lock lacks it.
	if v := byKey["dependency:npm:frontend:next"].Attrs["version"]; v != "16.4.1" {
		t.Errorf("next version: %v", v)
	}
	if v := byKey["dependency:npm:frontend:@mollie/api-client"].Attrs["version"]; v != "^4.1.0" {
		t.Errorf("mollie version: %v", v)
	}
	if f := byKey["dependency:npm:frontend:typescript"]; f.Attrs["scope"] != "dev" || f.Evidence[0].Line != 13 {
		t.Errorf("typescript: %v", f)
	}
}

// Moving a dependency to another line changes evidence, never the fact's
// identity. This is what keeps refactors out of the drift feed.
func TestLineMoveKeepsIdentity(t *testing.T) {
	a := fact.Fact{Kind: "dependency", Key: "k", Attrs: map[string]any{"version": "1"}}
	b := a
	ida, _ := fact.Canonical(map[string]any{"kind": a.Kind, "key": a.Key, "attrs": a.Attrs})
	b.Evidence = []fact.Evidence{{Path: "go.mod", Line: 99}}
	idb, _ := fact.Canonical(map[string]any{"kind": b.Kind, "key": b.Key, "attrs": b.Attrs})
	if !bytes.Equal(ida, idb) {
		t.Fatal("identity depends on evidence")
	}
}

// Every built-in plugin is registered once, in name order, with source
// "builtin" in the output.
func TestRegistry(t *testing.T) {
	var s fact.Scan
	if err := json.Unmarshal(scanFixture(t), &s); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range s.Plugins {
		if p.Source != fact.SourceBuiltin {
			t.Errorf("%s: source %q", p.Name, p.Source)
		}
		names = append(names, p.Name)
	}
	if got := fmt.Sprint(names); got != "[agent-config caddy commands compose deps-go deps-npm deps-nuget deps-python dockerfile dotnet env kubernetes migrations-sql nginx outbound ownership prisma size tests traefik]" {
		t.Errorf("plugins = %s", got)
	}
	if s.Config != nil {
		t.Errorf("no hint file and no operator flags must leave config out, got %+v", s.Config)
	}
}

func scanConfigFixture(t *testing.T, op config.Operator) fact.Scan {
	t.Helper()
	s, _, err := pipeline.ScanWorkTree("testdata/fixture-config", "local/fixture-config", fixtureSHA, fixtureAt, plugin.All(), op)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fact.Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	var out fact.Scan
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func services(s fact.Scan) string {
	var names []string
	for _, f := range s.Facts {
		if f.Kind == fact.KindService {
			names = append(names, f.Attrs["name"].(string))
		}
	}
	sort.Strings(names)
	return fmt.Sprint(names)
}

// A repo exclusion removes its files and is recorded with its line and how
// many files it took out.
func TestRepoExclusionIsVisible(t *testing.T) {
	s := scanConfigFixture(t, config.Operator{})
	if got := services(s); got != "[app]" {
		t.Errorf("services = %s, want [app]", got)
	}
	if s.Config == nil || s.Config.File != config.FileName || len(s.Config.Exclusions) != 1 {
		t.Fatalf("config = %+v", s.Config)
	}
	ex := s.Config.Exclusions[0]
	if ex.Pattern != "examples" || ex.By != config.ByRepo || ex.Line != 3 || ex.Files != 1 || ex.Reason != "demo apps" {
		t.Errorf("exclusion = %+v", ex)
	}
}

// The operator wins: --include brings excluded files back, --no-repo-config
// drops the file, and both are recorded.
func TestOperatorOverridesRepo(t *testing.T) {
	s := scanConfigFixture(t, config.Operator{Include: []string{"examples"}})
	if got := services(s); got != "[app demo]" {
		t.Errorf("include: services = %s", got)
	}
	if ex := s.Config.Exclusions[0]; ex.Files != 0 || ex.Reincluded != 1 {
		t.Errorf("include: exclusion = %+v", ex)
	}
	s = scanConfigFixture(t, config.Operator{NoRepoConfig: true, Exclude: []string{"docker-compose.yml"}})
	if got := services(s); got != "[demo]" {
		t.Errorf("no-repo-config: services = %s", got)
	}
	if !s.Config.IgnoredByOperator || len(s.Config.Exclusions) != 1 || s.Config.Exclusions[0].By != config.ByOperator {
		t.Errorf("no-repo-config: config = %+v", s.Config)
	}
}

func factsByKey(t *testing.T, data []byte) map[string]fact.Fact {
	t.Helper()
	var s fact.Scan
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	m := map[string]fact.Fact{}
	for _, f := range s.Facts {
		m[f.Key] = f
	}
	return m
}

func hasEvidence(f fact.Fact, path string, line int) bool {
	for _, e := range f.Evidence {
		if e.Path == path && e.Line == line {
			return true
		}
	}
	return false
}

// Builds and stacks are found in every command source and keep the chain of
// calls from the workflow that reaches them.
func TestCommands(t *testing.T) {
	m := factsByKey(t, scanDir(t, "fixture-commands"))
	const wf = ".github/workflows/cd.yml"

	api := m["image_build:api:backend/Dockerfile:api"]
	if api.Attrs["ci_invoked"] != true || !hasEvidence(api, "deploy/build.sh", 4) || !hasEvidence(api, wf, 14) {
		t.Errorf("api: workflow -> script chain: %+v", api)
	}
	if api.Evidence[1].EndLine != 5 {
		t.Errorf("api: continuation line should end the evidence at 5: %+v", api.Evidence)
	}
	worker := m["image_build:worker:backend/Dockerfile:worker"]
	if fmt.Sprint(worker.Attrs["tags"]) != "[${REGISTRY}/acme/worker:${tag} acme/worker:${TAG}]" || !hasEvidence(worker, "Makefile", 5) {
		t.Errorf("worker: script and Makefile builds merge: %+v", worker)
	}
	web := m["image_build:web:web/Dockerfile:_"]
	if web.Attrs["tool"] != "build-push-action" || fmt.Sprint(web.Attrs["tags"]) != "[ghcr.io/acme/web:${{ github.sha }}]" {
		t.Errorf("web: build-push-action keeps the expression as written: %+v", web)
	}
	if docs := m["image_build:docs:docs/Dockerfile:_"]; docs.Attrs["tool"] != "bake" || docs.Attrs["ci_invoked"] != false {
		t.Errorf("docs: %+v", docs)
	}

	prod := m["stack:compose:production"]
	if fmt.Sprint(prod.Attrs["files"]) != "[docker-compose.prod.yml deploy/docker-compose.vps.yml]" ||
		prod.Attrs["starts"] != true || !hasEvidence(prod, wf, 23) {
		t.Errorf("production: compose inside an ssh string, named after the job environment: %+v", prod)
	}
	if !hasEvidence(prod, "deploy/uat.sh", 9) || !hasEvidence(prod, "deploy/uat.sh", 10) || fmt.Sprint(prod.Attrs["env_files"]) != "[prod.env uat.env]" {
		t.Errorf("production: a bash array of -f flags inside a wrapper function resolves to the same stack: %+v", prod)
	}
	wf0 := prod.Attrs["workflows"].([]any)[0].(map[string]any)
	if wf0["workflow"] != "cd" || fmt.Sprint(wf0["events"]) != "[push workflow_dispatch]" {
		t.Errorf("production: workflow ref = %+v", wf0)
	}
	if local := m["stack:compose:local"]; local.Attrs["ci_invoked"] != false || !hasEvidence(local, "Taskfile.yml", 5) {
		t.Errorf("local: %+v", local)
	}
	if def := m["stack:compose:default"]; def.Attrs["default_files"] != true || fmt.Sprint(def.Attrs["files"]) != "[docker-compose.yml]" {
		t.Errorf("default: compose without -f uses the default file: %+v", def)
	}
}

// The compose plugin merges each invoked stack as Compose merges -f files and
// keeps the invocation as evidence.
func TestComposeStacks(t *testing.T) {
	m := factsByKey(t, scanDir(t, "fixture-commands"))
	api := m["service:compose:production:api"]
	// Image from the first file, build from the second, paths relative to the
	// first file's folder, depends_on merged.
	if api.Attrs["image"] != "ghcr.io/acme/api:${TAG:-latest}" || api.Attrs["build_context"] != "backend" ||
		fmt.Sprint(api.Attrs["depends_on"]) != "[cache db]" {
		t.Errorf("api = %v", api.Attrs)
	}
	if !hasEvidence(api, "docker-compose.prod.yml", 2) || !hasEvidence(api, "deploy/docker-compose.vps.yml", 2) ||
		!hasEvidence(api, ".github/workflows/cd.yml", 22) {
		t.Errorf("api evidence = %v", api.Evidence)
	}
	if v := m["datastore:compose:production:db"].Attrs["version"]; v != "17.2" {
		t.Errorf("db version: the later file wins, got %v", v)
	}
	for k, f := range m {
		if f.Kind == fact.KindStack && f.Attrs["source"] == "convention" {
			t.Errorf("%s: every compose file is invoked, no convention stack expected", k)
		}
	}
}

// A target only gets the builds whose output it copies out of a shared
// builder stage, and its paths map back to the repo through the COPY lines.
func TestDockerfileRecipes(t *testing.T) {
	m := factsByKey(t, scanDir(t, "fixture-dockerfile"))
	entry := func(f fact.Fact) string {
		var out []string
		for _, e := range f.Attrs["entrypoints"].([]any) {
			em := e.(map[string]any)
			if p, ok := em["path"]; ok {
				out = append(out, fmt.Sprint(p))
			} else {
				out = append(out, "npm:"+fmt.Sprint(em["workspace"]))
			}
		}
		return fmt.Sprint(out)
	}
	api := m["image_recipe:backend/Dockerfile:api"]
	if got := entry(api); got != "[backend/cmd/api]" {
		t.Errorf("api entrypoints = %s", got)
	}
	if got := fmt.Sprint(api.Attrs["sources"]); got != "[backend backend/go.mod backend/go.sum]" {
		t.Errorf("api sources = %s", got)
	}
	if got := fmt.Sprint(api.Attrs["remote_sources"]); got != "[https://example.com/ca.pem]" {
		t.Errorf("api remote = %s", got)
	}
	base := api.Attrs["base_images"].([]any)
	if len(base) != 2 || base[1].(map[string]any)["ref"] != "golang:1.27.3-alpine" || base[1].(map[string]any)["raw"] != "golang:${GO_VERSION}-alpine" {
		t.Errorf("api base images = %v", base)
	}
	worker := m["image_recipe:backend/Dockerfile:worker"]
	if got := entry(worker); got != "[backend/cmd/worker]" || worker.Attrs["final"] != true {
		t.Errorf("worker = %s final=%v", got, worker.Attrs["final"])
	}
	if got := fmt.Sprint(worker.Attrs["base_images"]); !strings.Contains(got, "busybox:1.36") {
		t.Errorf("worker copies from an outside image: %s", got)
	}
	if got := entry(m["image_recipe:backend/Dockerfile:builder"]); got != "[backend/cmd/api backend/cmd/worker]" {
		t.Errorf("builder entrypoints = %s", got)
	}
	web := m["image_recipe:frontend/Dockerfile:_final"]
	if got := entry(web); got != "[npm:frontend]" || web.Attrs["final"] != true || web.Attrs["target"] != "_final" {
		t.Errorf("frontend final = %s target=%v", got, web.Attrs["target"])
	}
	if got := fmt.Sprint(web.Attrs["sources"]); got != "[frontend package-lock.json package.json packages/web-ui packages/web-ui/package.json]" {
		t.Errorf("frontend sources = %s", got)
	}
	if _, ok := m["image_recipe:frontend/Dockerfile:#1"]; ok {
		t.Error("an unnamed stage that is not final is not a buildable target")
	}
	// scratch is Docker's empty base, not an image to pin or link.
	scratch := factsByKey(t, scanDir(t, "fixture-commands"))["image_recipe:web/Dockerfile:_final"]
	if got := fmt.Sprint(scratch.Attrs["base_images"]); got != "[]" {
		t.Errorf("scratch base images = %s", got)
	}
}

// A CD job runs a remote script over ssh after a cd on the host; the script
// sources a file whose function builds the -f list, copies it, and starts the
// stack through two wrapper functions. One stack from the three certain
// files, the PITR overlay recorded as conditional, services linked to code,
// and the production role unconfirmed while the job's if: is unreadable.
func TestSourcedCompose(t *testing.T) {
	data := scanDir(t, "fixture-sourced")
	m := factsByKey(t, data)
	st := m["stack:compose:edge.vps"]
	if fmt.Sprint(st.Attrs["files"]) != "[docker-compose.prod.yml deploy/docker-compose.vps.yml deploy/docker-compose.edge.vps.yml]" ||
		st.Attrs["source"] != "invocation" || st.Attrs["starts"] != true || st.Attrs["ci_invoked"] != true {
		t.Fatalf("stack = %+v", st)
	}
	for _, e := range []struct {
		p string
		l int
	}{{".github/workflows/cd.yml", 21}, {"deploy/scripts/remote-up.sh", 18}, {"deploy/scripts/remote-up.sh", 11}, {"deploy/scripts/remote-up.sh", 12},
		{"deploy/scripts/lib/compose-files.sh", 5},
		{"deploy/scripts/remote-up.sh", 3}, // compose exec in a function defined before the files are set, called after
		{"deploy/scripts/compose.sh", 8}} { // a standalone wrapper script with the same files
		if !hasEvidence(st, e.p, e.l) {
			t.Errorf("stack evidence lacks %s:%d: %v", e.p, e.l, st.Evidence)
		}
	}
	cond := st.Attrs["conditional_files"].([]any)
	if len(cond) != 1 || cond[0].(map[string]any)["file"] != "deploy/docker-compose.pitr.yml" || cond[0].(map[string]any)["condition"] != "shop_pitr_enabled || return 0" {
		t.Errorf("conditional_files = %v", cond)
	}
	if as, _ := st.Attrs["assumptions"].([]any); len(as) != 1 {
		t.Errorf("assumptions = %v", st.Attrs["assumptions"])
	}
	for k, f := range m {
		if f.Kind != fact.KindStack {
			continue
		}
		if f.Attrs["source"] == "convention" && k != "stack:compose:default" {
			t.Errorf("%s: no convention stack expected besides the local default", k)
		}
		// compose_files_test.sh only sets the files under a condition: no
		// stack, and never compose's default file in their place.
		if fs, _ := f.Attrs["files"].([]any); len(fs) == 0 {
			t.Errorf("%s: a stack without certain files", k)
		}
		for _, e := range f.Evidence {
			if e.Path == "deploy/scripts/compose_files_test.sh" {
				t.Errorf("%s: the test script's conditional files formed a stack: %v", k, f.Evidence)
			}
		}
	}
	// The pull under `if shop_pitr_enabled` (a guard above everything) is no
	// variant: exactly one stack carries the vps files.
	withVps := 0
	for _, f := range m {
		if fs, _ := f.Attrs["files"].([]any); f.Kind == fact.KindStack && strings.Contains(fmt.Sprint(fs), "deploy/docker-compose.vps.yml") {
			withVps++
		}
	}
	if withVps != 1 || !hasEvidence(st, "deploy/scripts/remote-up.sh", 15) {
		t.Errorf("want one stack with the vps files, its evidence with the pitr pull: %d, %v", withVps, st.Evidence)
	}
	if def := m["stack:compose:default"]; def.Attrs["source"] != "convention" || def.Attrs["ci_invoked"] != false {
		t.Errorf("default = %+v", def)
	}
	mp, err := pipeline.Map(data, rules.Settings{DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Stacks []struct{ Stack, Role string }
		Links  []struct{ Service string }
	}
	raw, _ := json.Marshal(mp)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, s := range out.Stacks {
		roles[s.Stack] = s.Role
	}
	if roles["stack:compose:edge.vps"] != "production (unconfirmed)" || roles["stack:compose:default"] != "none" {
		t.Errorf("roles = %v", roles)
	}
	linked := map[string]bool{}
	for _, l := range out.Links {
		linked[l.Service] = true
	}
	if !linked["service:compose:edge.vps:api"] || !linked["service:compose:edge.vps:web"] {
		t.Errorf("links = %v", out.Links)
	}
}

// The map is derived from the facts alone and compared byte for byte with
// testdata/<name>.map.golden.json.
func TestMapGolden(t *testing.T) {
	for _, name := range []string{"fixture", "fixture-commands", "fixture-caddy", "fixture-ci", "fixture-ci.confirmed", "fixture-migrations", "fixture-prisma", "fixture-k8s", "fixture-sourced", "fixture-dotnet"} {
		set := rules.Settings{DefaultBranch: "main"}
		dir := name
		if base, ok := strings.CutSuffix(name, ".confirmed"); ok {
			// The same facts with confirmations passed by the caller.
			cs, err := ReadConfirmations(filepath.Join("testdata", base+".confirmations.json"))
			if err != nil {
				t.Fatal(err)
			}
			set.Confirmations, dir = cs, base
		}
		m, err := pipeline.Map(scanDir(t, dir), set)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fact.Canonical(m)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		golden := filepath.Join("testdata", name+".map.golden.json")
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run go test -update once)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("map differs from %s; run go test -update and review the diff", golden)
		}
	}
}

// Caddyfile routes: snippets, named matchers, file imports and {$VAR}
// addresses; the caddy service that mounts the file puts the routes in its
// stack, upstreams that name a service become edges, and served hosts are
// own. Global options, which hold the ACME email address, are not read.
func TestCaddyRoutes(t *testing.T) {
	raw := scanDir(t, "fixture-caddy")
	if bytes.Contains(raw, []byte("ops@")) {
		t.Error("the global options' email address reached the scan")
	}
	m := factsByKey(t, raw)
	edge := m["service:compose:default:edge"]
	if got := fmt.Sprint(edge.Attrs["mounts"]); got != "[map[source:deploy/Caddyfile target:/etc/caddy/Caddyfile]]" {
		t.Errorf("mounts = %s (named volumes, directories and untracked files are left out)", got)
	}
	hooks := m["route:caddy:deploy/Caddyfile:shop.acme-widgets.be:POST /hooks/*"]
	if fmt.Sprint(hooks.Attrs["upstreams"]) != "[map[address:h2c://api:9090 host:api port:9090 scheme:h2c]]" {
		t.Errorf("hooks = %+v", hooks)
	}
	api := m["route:caddy:deploy/Caddyfile:shop.acme-widgets.be:/api/*"]
	if fmt.Sprint(api.Evidence) != "[{deploy/Caddyfile 9 0 } {deploy/Caddyfile 13 0 } {deploy/Caddyfile 14 0 }]" {
		t.Errorf("snippet route evidence = %v", api.Evidence)
	}
	blog := m["route:caddy:deploy/Caddyfile:blog.acme-widgets.be:*"]
	if blog.Attrs["action"] != "file_server" || blog.Evidence[0].Path != "deploy/sites/blog.caddy" {
		t.Errorf("imported site = %+v", blog)
	}
	if _, ok := m["route:caddy:deploy/sites/blog.caddy:blog.acme-widgets.be:*"]; ok {
		t.Error("an imported file was also read as a root Caddyfile")
	}
	ops := m["route:caddy:deploy/Caddyfile:{$OPS_HOST:ops.localhost}:/health"]
	if ops.Attrs["status"] != float64(200) || fmt.Sprint(ops.Attrs["addresses"]) != "[map[address:{$OPS_HOST:ops.localhost} host_env:OPS_HOST]]" {
		t.Errorf("ops = %+v", ops)
	}

	mp, err := pipeline.Map(raw, rules.Settings{DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var edges []string
	for _, e := range mp.Routes {
		edges = append(edges, e.Match+" "+e.Upstream+">"+e.To)
	}
	want := "[* > * web2:3000> * web:3000>service:compose:default:web /api/* api:8080>service:compose:default:api /old/* > @internal > " +
		"POST /hooks/* h2c://api:9090>service:compose:default:api * host.docker.internal:3010> /health >]"
	if got := fmt.Sprint(edges); got != want {
		t.Errorf("edges = %s\nwant %s", got, want)
	}
	var own []string
	for _, o := range mp.Own {
		own = append(own, o.Host)
	}
	if fmt.Sprint(own) != "[blog.acme-widgets.be shop.acme-widgets.be www.acme-widgets.be]" || len(mp.Unknown) != 0 {
		t.Errorf("own = %v, unknown = %+v (the served host the frontend calls is own, not unknown)", own, mp.Unknown)
	}
}

// Prisma schemas: the.prisma files of one folder are one schema; @@map, @map
// and @@schema name tables and columns; relation fields, composite types and
// views are not columns or tables; rls is not claimed.
func TestPrismaTables(t *testing.T) {
	m := factsByKey(t, scanDir(t, "fixture-prisma"))
	var keys []string
	for k, f := range m {
		if f.Plugin == "prisma" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	want := "[db_column:prisma:db/schema:billing.Invoice.customerId db_column:prisma:db/schema:billing.Invoice.number " +
		"db_column:prisma:db/schema:billing.Invoice.total db_column:prisma:db/schema:public.customers.email " +
		"db_column:prisma:db/schema:public.customers.full_name db_column:prisma:db/schema:public.customers.id " +
		"db_column:prisma:db/schema:public.customers.note db_column:prisma:db/schema:public.customers.search " +
		"db_column:prisma:db/schema:public.customers.status db_column:prisma:db/schema:public.customers.tags " +
		"db_table:prisma:db/schema:billing.Invoice db_table:prisma:db/schema:public.customers]"
	if got := fmt.Sprint(keys); got != want {
		t.Errorf("keys\n got %s\nwant %s", got, want)
	}
	if _, ok := m["db_table:prisma:db/schema:public.customers"].Attrs["rls"]; ok {
		t.Error("rls reported from a Prisma schema, which cannot say")
	}
	if c := m["db_column:prisma:db/schema:billing.Invoice.number"]; c.Attrs["primary_key"] != true {
		t.Errorf("composite @@id: %+v", c)
	}
	if c := m["db_column:prisma:db/schema:public.customers.search"]; c.Attrs["type"] != `Unsupported("tsvector")` || c.Attrs["nullable"] != true {
		t.Errorf("Unsupported: %+v", c)
	}
}

// Config files read env names when their tool loads them: a Caddyfile's
// {$VAR} and {env.VAR}, a Prisma schema's env("VAR"), origin config; a name
// in a comment is not read. SQL migrations: found by content in any folder
// (billing/sql), not in test folders, not queries; each set replayed in order
// (Flyway V2 before V10, repeatable last); down files and goose's Down half
// are never applied; function bodies are not read.
func TestSQLMigrations(t *testing.T) {
	var got []string
	for _, f := range factsByKey(t, scanDir(t, "fixture-migrations")) {
		if f.Plugin != "migrations-sql" {
			continue
		}
		switch f.Kind {
		case fact.KindDBTable:
			var at []string
			for _, e := range f.Evidence {
				at = append(at, fmt.Sprintf("%s:%d", e.Path, e.Line))
			}
			got = append(got, fmt.Sprintf("%s rls=%v @%s", f.Key, f.Attrs["rls"], strings.Join(at, " ")))
		case fact.KindDBColumn:
			flags := ""
			for _, k := range []string{"primary_key", "unique", "enum"} {
				if f.Attrs[k] == true {
					flags += "," + k
				}
			}
			got = append(got, fmt.Sprintf("%s %v null=%v%s", f.Key, f.Attrs["type"], f.Attrs["nullable"], flags))
		}
	}
	sort.Strings(got)
	want := []string{
		"db_column:sql:api/migrations:audit.events.id bigint null=false,primary_key",
		"db_column:sql:api/migrations:audit.events.payload jsonb null=true",
		"db_column:sql:api/migrations:public.sessions.expires_at timestamptz null=false",
		"db_column:sql:api/migrations:public.sessions.token_sha256 bytea null=false,primary_key",
		"db_column:sql:api/migrations:public.sessions.user_id bigint null=false",
		"db_column:sql:db/migrations:public.accounts.created_at timestamp with time zone null=false",
		"db_column:sql:db/migrations:public.accounts.email varchar(320) null=false,unique",
		"db_column:sql:db/migrations:public.accounts.id uuid null=false,primary_key",
		"db_column:sql:db/migrations:public.accounts.state account_state null=false,enum",
		"db_column:sql:db/migrations:public.notes.account_id uuid null=true",
		"db_column:sql:db/migrations:public.notes.content text null=false",
		"db_column:sql:db/migrations:public.notes.id bigserial null=false,primary_key",
		// No PostgreSQL signal in the SQL: names as written, no rls.
		"db_column:sql:billing/sql:invoices.id bigint null=false,primary_key",
		"db_column:sql:billing/sql:invoices.total numeric(12, 2) null=false",
		"db_column:sql:billing/sql:invoices.vat_number text null=true",
		"db_column:sql:legacy:customers.id int null=false,primary_key",
		"db_column:sql:legacy:customers.name varchar(255) null=true,unique",
		"db_column:sql:prisma/migrations:public.Organisation.createdAt timestamp(3) null=false",
		"db_column:sql:prisma/migrations:public.Organisation.id text null=false,primary_key",
		"db_column:sql:prisma/migrations:public.Organisation.name text null=true",
		"db_column:sql:prisma/migrations:public.Organisation.plan Plan null=false,enum",
		"db_column:sql:shop/src/main/resources/db/hsqldb:pets.birth_date date null=true",
		"db_column:sql:shop/src/main/resources/db/hsqldb:pets.id integer null=false,primary_key",
		"db_column:sql:shop/src/main/resources/db/hsqldb:pets.name varchar(30) null=true",
		"db_column:sql:worker/migrations:public.jobs.id bigint null=false,primary_key",
		"db_column:sql:worker/migrations:public.jobs.note text null=true",
		"db_column:sql:worker/migrations:public.jobs.state text null=false",
		"db_table:sql:api/migrations:audit.events rls=false @api/migrations/000002_audit.up.sql:1",
		"db_table:sql:api/migrations:public.sessions rls=false @api/migrations/000001_init.up.sql:1",
		"db_table:sql:db/migrations:public.accounts rls=false @db/migrations/V1__init.sql:4",
		"db_table:sql:db/migrations:public.notes rls=true @db/migrations/V10__rls.sql:1 db/migrations/V10__rls.sql:2 db/migrations/V10__rls.sql:3 db/migrations/V2__notes.sql:1 db/migrations/V2__notes.sql:2",
		"db_table:sql:billing/sql:invoices rls=<nil> @billing/sql/0001_invoices.sql:2 billing/sql/0002_vat.sql:1",
		"db_table:sql:legacy:customers rls=<nil> @legacy/structure.sql:1",
		"db_table:sql:prisma/migrations:public.Organisation rls=false @prisma/migrations/20240101000000_init/migration.sql:5 prisma/migrations/20240301000000_name/migration.sql:2",
		// An HSQLDB schema file: no dialect signal, so no public. and no rls.
		"db_table:sql:shop/src/main/resources/db/hsqldb:pets rls=<nil> @shop/src/main/resources/db/hsqldb/schema.sql:3",
		"db_table:sql:worker/migrations:public.jobs rls=false @worker/migrations/00001_jobs.sql:3",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestConfigEnvNames(t *testing.T) {
	for fixture, want := range map[string]string{
		"fixture-caddy":  "[BLOG_ROOT:[config]:deploy/sites/blog.caddy:3 OPS_HOST:[config]:deploy/Caddyfile:34]",
		"fixture-prisma": "[DATABASE_URL:[config]:db/schema/base.prisma:4 DIRECT_URL:[config]:db/schema/base.prisma:6]",
	} {
		var got []string
		for _, f := range factsByKey(t, scanDir(t, fixture)) {
			if f.Kind == fact.KindEnvVar {
				got = append(got, fmt.Sprintf("%s:%v:%s:%d", f.Attrs["name"], f.Attrs["origins"], f.Evidence[0].Path, f.Evidence[0].Line))
			}
		}
		sort.Strings(got)
		if fmt.Sprint(got) != want {
			t.Errorf("%s: env = %v\nwant %s", fixture, got, want)
		}
	}
}

// A caller that renamed its hint file passes the old name as a fallback
// for older commits: it is read only when passed and .cernly.yml is absent, and
// recorded under its own name. When both are tracked, .cernly.yml wins and the
// scan says the fallback was ignored.
func TestHintFallbacks(t *testing.T) {
	const old = ".example-hints.yml"
	scan := func(files map[string]string, fallbacks ...string) *fact.Scan {
		t.Helper()
		dir := t.TempDir()
		for name, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "x"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		s, _, err := pipeline.Scan(dir, pipeline.ScanOptions{RepoID: "local/fallback", Plugins: plugin.All(), Operator: config.Operator{HintFallbacks: fallbacks}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	hint := "version: 1\nexclude: [examples]\n"
	if s := scan(map[string]string{old: hint, "examples/a.txt": "a"}); s.Config != nil {
		t.Errorf("fallback not passed: config = %+v, want none", s.Config)
	}
	s := scan(map[string]string{old: hint, "examples/a.txt": "a"}, old)
	if s.Config == nil || s.Config.File != old || len(s.Config.Exclusions) != 1 || len(s.Config.Errors) != 0 {
		t.Errorf("fallback only: config = %+v", s.Config)
	}
	s = scan(map[string]string{config.FileName: hint, old: "version: 1\nexclude: [other]\n", "examples/a.txt": "a"}, old)
	if s.Config == nil || s.Config.File != config.FileName || len(s.Config.Exclusions) != 1 || s.Config.Exclusions[0].Pattern != "examples" {
		t.Fatalf("both: config = %+v", s.Config)
	}
	if len(s.Config.Errors) != 1 || s.Config.Errors[0].Path != old {
		t.Errorf("both: errors = %+v, want the fallback reported as ignored", s.Config.Errors)
	}
}

// Tags are recorded under config.declared with their line.
func TestTagsAreRecorded(t *testing.T) {
	s := scanConfigFixture(t, config.Operator{File: []byte("version: 1\ntags:\n  team: platform\n"), FileName: "op.yml"})
	if s.Config == nil || s.Config.Declared == nil {
		t.Fatalf("config = %+v", s.Config)
	}
	var got []string
	for _, tg := range s.Config.Declared.Tags {
		got = append(got, fmt.Sprintf("%s=%s/%s:%d", tg.Key, tg.Value, tg.By, tg.Line))
	}
	if fmt.Sprint(got) != "[product=webshop/repo:7 team=platform/operator:3]" {
		t.Errorf("tags = %v", got)
	}
}
