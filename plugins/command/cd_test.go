package command

import (
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo/repotest"
)

// A literal cd in a workflow step moves the commands after it:
// `cd chart/ && helm template .` must not read the repo root as the chart.
func TestWorkflowCd(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		".github/workflows/ci.yml": `on:
  push:
    branches: [main]
jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - run: |
          cd chart/
          helm template .
      - run: cd deploy && docker compose up -d
      - run: |
          cd "$DIR"
          kubectl apply -k overlay
      - run: |
          (cd k8s && kubectl apply -f app.yaml)
          kubectl apply -f k8s/other.yaml
      - run: docker compose -f docker-compose.yml up -d
`,
		"chart/Chart.yaml":           "name: shop\n",
		"deploy/docker-compose.yml":  "services:\n  api:\n    image: acme/api:1\n",
		"docker-compose.yml":         "services:\n  web:\n    image: acme/web:1\n",
		"overlay/kustomization.yaml": "resources: []\n",
		"k8s/app.yaml":               "kind: Deployment\n",
		"k8s/other.yaml":             "kind: Deployment\n",
	})
	res := Plugin{}.Run(r)
	var got []string
	for _, f := range res.Facts {
		if f.Kind != fact.KindStack {
			continue
		}
		var files []string
		for _, x := range anyList(f.Attrs["files"]) {
			files = append(files, x.(string))
		}
		got = append(got, f.Key+" "+strings.Join(files, ","))
	}
	sort.Strings(got)
	want := []string{
		// Compose stacks are named by file, so the second default gets a suffix.
		"stack:compose:default deploy/docker-compose.yml",
		"stack:compose:default-2 docker-compose.yml",
		"stack:helm:chart chart",
		// A subshell's cd is not followed; its file is found from the root as before.
		"stack:kubernetes:app app.yaml",
		// After a cd to an expansion the folder is unknown: read as before, from the root.
		"stack:kubernetes:other k8s/other.yaml",
		"stack:kubernetes:overlay overlay",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("stacks:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
