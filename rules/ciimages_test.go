package rules

import (
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
)

func ciRecipe(df, target string, final bool, sources ...string) fact.Fact {
	return fact.Fact{Kind: fact.KindImageRecipe, Key: "image_recipe:" + df + ":" + target,
		Attrs: map[string]any{"dockerfile": df, "target": target, "final": final, "sources": fact.Strings(sources)}}
}

func ciBuild(repo, df string, push any, tags []string, wfs ...map[string]any) fact.Fact {
	a := map[string]any{"image_repo": repo, "dockerfile": df, "dockerfile_resolved": true, "tags": fact.Strings(tags),
		"contexts": fact.Strings([]string{"."}), "ci_invoked": len(wfs) > 0}
	if push != nil {
		a["push"] = push
	}
	var ws []any
	for _, w := range wfs {
		ws = append(ws, w)
	}
	if ws != nil {
		a["workflows"] = ws
	}
	return fact.Fact{Kind: fact.KindImageBuild, Key: "image_build:" + repo + ":" + df + ":_", Attrs: a}
}

// buildWf is a push-on-main job whose step at line 20 builds the image.
func buildWf(job string, after ...map[string]any) map[string]any {
	w := testWf(job, "", "Build")
	w["step"] = map[string]any{"line": 20, "name": "Build", "run": "docker build ."}
	return withAfter(w, after...)
}

func withAfter(w map[string]any, after ...map[string]any) map[string]any {
	as := []any{}
	for _, a := range after {
		as = append(as, a)
	}
	w["steps_after"] = as
	return w
}

func TestCIImages(t *testing.T) {
	svc := fact.Fact{Kind: fact.KindService, Key: "service:compose:prod:web",
		Attrs: map[string]any{"name": "web", "stack": "prod", "image": "ghcr.io/acme/web:latest"}}
	dep := fact.Fact{Kind: fact.KindDependency, Key: "dependency:npm:apps/api:express",
		Evidence: []fact.Evidence{{Path: "apps/api/package.json", Line: 4}}}
	s := &fact.Scan{Facts: []fact.Fact{
		svc, dep,
		ciRecipe("apps/web/Dockerfile", "_final", true, "apps/web"),
		ciRecipe("apps/api/Dockerfile", "_final", true, "apps/api"),
		ciRecipe("tests/Dockerfile", "_final", true, "tests"),
		ciRecipe("apps/job/Dockerfile", "_final", true, "apps/job"),
		// Built in CI and run by a Compose service: already on the map.
		ciBuild("web", "apps/web/Dockerfile", true, []string{"ghcr.io/acme/web:latest"}, buildWf("web")),
		// Built in CI, pushed by a later docker image push.
		ciBuild("api", "apps/api/Dockerfile", false, []string{"ghcr.io/acme/api:${{ github.sha }}"},
			buildWf("release", run(22, `docker image push 'ghcr.io/acme/api:${{ github.sha }}'`), run(24, "docker push ghcr.io/acme/other:1"))),
		// Built in CI, only tests run after it: test only, by the runner test.
		ciBuild("runner", "tests/Dockerfile", nil, []string{"acme-runner"}, buildWf("check", run(22, "npm ci && npx playwright test"))),
		// Built by a script no CI job reaches: not a CI image.
		ciBuild("job", "apps/job/Dockerfile", false, []string{"acme/job"}),
	}}
	m := DeriveWith(s, strings.Repeat("0", 64), Settings{DefaultBranch: "main"}, Catalogue{})
	got := map[string]CIImage{}
	for _, c := range m.CIImages {
		got[c.Image] = c
	}
	if len(got) != 2 {
		t.Fatalf("ci images %v, want api and tests", m.CIImages)
	}
	api := got["ci_image:apps/api/Dockerfile:_final"]
	if api.TestOnly || len(api.Pushes) != 1 || api.Pushes[0].Target != "ghcr.io/acme/api" || api.Pushes[0].By != "docker_push" || api.Pushes[0].Line != 22 {
		t.Errorf("api: %+v", api)
	}
	if api.Deployment != DeploymentNotSeen || !strings.HasSuffix(api.Detail, "deployment not seen in this repo") || api.Builds[0].Line != 20 {
		t.Errorf("api detail: %q, builds %+v", api.Detail, api.Builds)
	}
	tests := got["ci_image:tests/Dockerfile:_final"]
	if !tests.TestOnly || tests.Builds[0].TestJob == nil || tests.Builds[0].TestJob.By != "runner" || len(tests.Pushes) != 0 {
		t.Errorf("tests: %+v", tests)
	}
	if !strings.Contains(tests.Detail, "test only: job check only runs tests after the build") {
		t.Errorf("tests detail: %q", tests.Detail)
	}
	var on []string
	for _, a := range m.Attached {
		on = append(on, a.Service+" <- "+a.Fact)
	}
	if len(on) != 1 || on[0] != "ci_image:apps/api/Dockerfile:_final <- dependency:npm:apps/api:express" {
		t.Errorf("attached %v", on)
	}
	for _, st := range m.Stacks {
		if st.Role != RoleNone {
			t.Errorf("stack %s is %s; a CI image never makes production", st.Stack, st.Role)
		}
	}
}

func TestCIImagePushFlag(t *testing.T) {
	s := &fact.Scan{Facts: []fact.Fact{
		ciRecipe("Dockerfile", "_final", true, "."),
		ciBuild("app", "Dockerfile", true, []string{"ghcr.io/acme/app:1", "acme/app:1", "ghcr.io/acme/app:latest"}, buildWf("ship")),
	}}
	m := DeriveWith(s, strings.Repeat("0", 64), Settings{DefaultBranch: "main"}, Catalogue{})
	if len(m.CIImages) != 1 {
		t.Fatalf("ci images %v", m.CIImages)
	}
	var ts []string
	for _, p := range m.CIImages[0].Pushes {
		ts = append(ts, p.Target+"/"+p.By)
	}
	if strings.Join(ts, " ") != "acme/app/push ghcr.io/acme/app/push" {
		t.Errorf("pushes %v", ts)
	}
}
