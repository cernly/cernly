package kubernetes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/repotest"
)

func TestImageRewrite(t *testing.T) {
	for _, c := range [][5]string{
		{"ghcr.io/example/api:latest", "", "1.8.0", "", "ghcr.io/example/api:1.8.0"},
		{"localhost:5000/api", "registry.example.org/api", "", "", "registry.example.org/api"},
		{"api:1.0@sha256:aa", "", "", "sha256:bb", "api:1.0@sha256:bb"},
	} {
		if got := rewrite(c[0], c[1], c[2], c[3]); got != c[4] {
			t.Errorf("rewrite(%q) = %q, want %q", c[0], got, c[4])
		}
	}
	if imageName("localhost:5000/api:2") != "localhost:5000/api" || imageName("localhost:5000/api") != "localhost:5000/api" {
		t.Error("imageName")
	}
}

// A Helm repository URL and a kustomize remote lose their credentials.
func TestNoCredentials(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"chart/Chart.yaml":          "apiVersion: v2\nname: web\nversion: 1.0.0\ndependencies:\n  - name: db\n    version: 1.2.3\n    repository: https://user:SECRET1@charts.example.org/stable\n",
		"chart/templates/x.yaml":    "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n",
		"deploy/kustomization.yaml": "resources:\n  - https://SECRET2@github.com/example/base//deploy?ref=v1\n  - app.yaml\n",
		"deploy/app.yaml":           "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  template:\n    spec:\n      containers:\n        - name: app\n          image: ghcr.io/example/app:1.0\n",
	})
	res := Plugin{}.Run(r)
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("credential in facts: %s", out)
	}
	if !strings.Contains(string(out), "charts.example.org/stable") || !strings.Contains(string(out), "github.com/example/base") {
		t.Errorf("URLs missing: %s", out)
	}
}

// A resource listed many times is expanded once, and a kustomization's
// objects are capped (17 GB before).
func TestKustomizeAmplification(t *testing.T) {
	root := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	doc := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: n\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          image: ghcr.io/example/c:1\n---\n"
	write("deploy/a.yaml", strings.Repeat(doc, 3000))
	write("deploy/kustomization.yaml", "resources:\n"+strings.Repeat("  - a.yaml\n", 5000))
	// Distinct files are not deduplicated, so the object budget caps them.
	defer func(n int) { maxKustomizeObjects = n }(maxKustomizeObjects)
	maxKustomizeObjects = 1000
	big := "resources:\n"
	for i := 0; i < 6; i++ {
		name := string(rune('a'+i)) + ".yaml"
		write("many/"+name, strings.Repeat(doc, 500))
		big += "  - " + name + "\n"
	}
	write("many/kustomization.yaml", big)
	// An overlay over the over-budget base: the base's cut must not break it.
	write("overlay/kustomization.yaml", "resources:\n  - ../many\n")
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	found := false
	for _, e := range res.Errors {
		found = found || strings.Contains(e.Message, "objects from")
	}
	if !found {
		t.Errorf("no budget error: %v", res.Errors)
	}
}
