package command

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/repotest"
)

func TestStepCondition(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		".github/workflows/cd.yml": `on:
  push:
    branches: [main]
jobs:
  build:
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    steps:
      - run: echo build
  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - if: github.ref_name == 'dev'
        run: docker compose -f docker-compose.prod.yml up -d
      - run: docker compose -f docker-compose.prod.yml up -d
  other:
    runs-on: ubuntu-latest
    steps:
      - run: echo first
      - if: ${{ github.event_name == 'push' }}
        run: docker compose -f docker-compose.prod.yml up -d
  named:
    runs-on: ubuntu-latest
    steps:
      - name: Up
        id: up
        if: github.ref_name == 'main'
        run: docker compose -f docker-compose.prod.yml up -d
      - id: again
        if: github.ref_name == 'main'
        run: docker compose -f docker-compose.prod.yml up -d
`,
		"docker-compose.prod.yml": "services:\n  api:\n    image: acme/api:1\n",
	})
	res := Plugin{}.Run(r)
	var got []string
	for _, f := range res.Facts {
		if f.Kind != fact.KindStack {
			continue
		}
		wfs, _ := f.Attrs["workflows"].([]any)
		for _, w := range wfs {
			wm := w.(map[string]any)
			got = append(got, fmt.Sprintf("%v %v", wm["job"], wm["conditions"]))
			// The step that reaches the stack is the one with the if: (main's
			// steps in workflow refs), also for a per-step copy of the job.
			if st, ok := wm["step"].(map[string]any); ok {
				got = append(got, fmt.Sprintf("  step %v %v after %d", st["line"], st["run"], len(anyList(wm["steps_after"]))))
			}
		}
	}
	want := []string{
		"deploy [map[if:github.ref == 'refs/heads/main' job:build] map[if:github.ref_name == 'dev' job:deploy step:#1]]",
		"  step 14 docker compose -f docker-compose.prod.yml up -d after 1",
		"deploy [map[if:github.ref == 'refs/heads/main' job:build]]",
		"  step 16 docker compose -f docker-compose.prod.yml up -d after 0",
		// name:, else id:, else the position from 1.
		"named [map[if:github.ref_name == 'main' job:named step:Up]]",
		"  step 26 docker compose -f docker-compose.prod.yml up -d after 1",
		"named [map[if:github.ref_name == 'main' job:named step:again]]",
		"  step 30 docker compose -f docker-compose.prod.yml up -d after 0",
		"other [map[if:${{ github.event_name == 'push' }} job:other step:#2]]",
		"  step 21 docker compose -f docker-compose.prod.yml up -d after 0",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("workflows:\n%q\nwant\n%q", got, want)
	}
}

func anyList(v any) []any { l, _ := v.([]any); return l }

// Filters are recorded as written, in order; absent keys stay absent.
func TestTriggers(t *testing.T) {
	for on, want := range map[string]string{
		"push":                      "map[branches:[*] events:[push]]",
		"{push: {paths: [src/**]}}": "map[branches:[*] events:[push]]",
		"{push: {branches: [release/**, '!release/**-rc', main]}}": "map[branches:[release/** !release/**-rc main] events:[push]]",
		"{push: {tags: [v*]}}":                                      "map[branches:[] events:[push] tags:[v*]]",
		"{push: {tags-ignore: [v0*]}}":                              "map[branches:[] events:[push] tags_ignore:[v0*]]",
		"{push: {tags: }}":                                          "map[branches:[] events:[push] tags:[]]",
		"{push: {branches-ignore: [main]}}":                         "map[branches:[] branches_ignore:[main] events:[push]]",
		"{push: {branches-ignore: dev, tags: [v*]}}":                "map[branches:[] branches_ignore:[dev] events:[push] tags:[v*]]",
		"{workflow_run: {workflows: [CI], branches-ignore: [dev]}}": "map[after:[CI] after_branches_ignore:[dev] branches:[] events:[workflow_run]]",
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(on), &n); err != nil {
			t.Fatal(err)
		}
		tr := triggers(n.Content[0])
		m := workflowInfo{Events: tr.events, Branches: tr.branches, BranchesIgn: tr.branchesIgn, Tags: tr.tags,
			TagsIgn: tr.tagsIgn, After: tr.after, AfterBranch: tr.afterBranches, AfterIgn: tr.afterIgn}.ref(false)
		for _, k := range []string{"path", "job", "manual"} {
			delete(m, k)
		}
		if got := fmt.Sprint(m); got != want {
			t.Errorf("%s: %s, want %s", on, got, want)
		}
	}
}

// Folding joins and splits lines, so findings in folded and multi-line flow
// run: scalars are mapped back to the raw lines; literal ones map line for
// line. A scalar that cannot be mapped gives its first line.
func TestScalarLines(t *testing.T) {
	head := "on: push\njobs:\n  b:\n    runs-on: x\n    steps:\n"
	for name, tc := range map[string]struct{ steps, want string }{
		"folded":                {"      - run: >\n          echo a\n\n          echo b\n\n          docker build -t web ./svc\n", "[web 11-0]"},
		"folded strip":          {"      - run: >-\n          echo a\n\n          docker build\n          -t web\n          ./svc\n\n          docker build -t api ./api\n", "[api 13-0 web 9-11]"},
		"folded more-indented":  {"      - run: >\n          echo a\n            echo b\n          docker build -t web ./svc\n", "[web 9-0]"},
		"folded nested":         {"      - run: >\n          echo a\n\n          ssh h\n          'docker build -t web ./svc'\n", "[web 10-0]"},
		"literal":               {"      - run: |\n          echo a\n\n          docker build \\\n            -t web ./svc\n", "[web 9-10]"},
		"plain":                 {"      - run: echo a\n\n          docker build\n          -t web ./svc\n", "[web 8-9]"},
		"single quoted":         {"      - run: 'echo a\n\n          docker build -t web ./svc'\n", "[web 8-0]"},
		"double quoted escape":  {"      - run: \"echo \\\"a\\\"\n\n          docker build -t web ./svc\"\n", "[web 6-0]"},
		"indentation indicator": {"      - run: >2\n           echo a\n\n          docker build -t web ./svc\n", "[web 7-0]"},
	} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".github/workflows"), 0o755)
		os.WriteFile(filepath.Join(root, ".github/workflows/ci.yml"), []byte(head+tc.steps), 0o644)
		r, err := repo.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range (Plugin{}).Run(r).Facts {
			if f.Kind == fact.KindImageBuild {
				e := f.Evidence[0]
				got = append(got, fmt.Sprintf("%v %d-%d", f.Attrs["image_repo"], e.Line, e.EndLine))
			}
		}
		sort.Strings(got)
		if fmt.Sprint(got) != tc.want {
			t.Errorf("%s: builds = %v, want %s", name, got, tc.want)
		}
	}
}
