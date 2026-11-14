package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cernly/cernly/archdoc"
)

var docsFixtures = []string{"fixture", "fixture-caddy", "fixture-ci", "fixture-k8s", "fixture-dotnet"}

// The architecture docs, byte for byte: the same scan gives the same file,
// and every footnote used is defined and the other way round.
func TestDocsGolden(t *testing.T) {
	for _, name := range docsFixtures {
		facts := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(facts, scanDir(t, name), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := BuildDocs(facts, "main", archdoc.Options{Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		again, _ := BuildDocs(facts, "main", archdoc.Options{Version: "test"})
		if !bytes.Equal(got, again) {
			t.Errorf("%s: two runs differ", name)
		}
		golden := filepath.Join("testdata", name+".docs.golden.md")
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
			t.Errorf("docs differ from %s; run go test -update and review the diff", golden)
		}
		checkFootnotes(t, name, string(got))
	}
}

var (
	noteUse = regexp.MustCompile(`\[\^(\d+)\]`)
	noteDef = regexp.MustCompile(`(?m)^\[\^(\d+)\]: `)
)

func checkFootnotes(t *testing.T, name, md string) {
	defined := map[string]bool{}
	for _, m := range noteDef.FindAllStringSubmatch(md, -1) {
		defined[m[1]] = true
	}
	used := map[string]bool{}
	body, _, _ := strings.Cut(md, "\n## Evidence\n")
	for _, m := range noteUse.FindAllStringSubmatch(body, -1) {
		used[m[1]] = true
		if !defined[m[1]] {
			t.Errorf("%s: footnote %s used, not defined", name, m[1])
		}
	}
	for n := range defined {
		if !used[n] {
			t.Errorf("%s: footnote %s defined, not used", name, n)
		}
	}
	// Every row of a table with an Evidence column cites its facts.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, " | ") && !strings.HasPrefix(line, "| Package") && !strings.Contains(line, "Evidence |") && !strings.HasPrefix(line, "|---") {
			if !noteUse.MatchString(line) && !strings.HasPrefix(line, "| `") {
				t.Errorf("%s: a row without evidence: %s", name, line)
			}
		}
	}
}

// A stack without services gets no diagram: an empty System_Boundary does
// not parse in Mermaid (a stack found only in a Helm chart).
func TestDocsEmptyStack(t *testing.T) {
	var s map[string]any
	if err := json.Unmarshal(scanDir(t, "fixture-k8s"), &s); err != nil {
		t.Fatal(err)
	}
	// Only the empty stack, so the building block view draws it.
	var keep []any
	for _, f := range s["facts"].([]any) {
		switch f.(map[string]any)["kind"] {
		case "stack", "service", "datastore":
		default:
			keep = append(keep, f)
		}
	}
	s["facts"] = append(keep, map[string]any{
		"kind": "stack", "key": "stack:helm:empty", "plugin": "commands",
		"attrs":    map[string]any{"name": "empty", "tool": "helm", "files": []any{"chart"}, "source": "invocation", "starts": false, "ci_invoked": false, "default_files": false},
		"evidence": []any{map[string]any{"path": "deploy.sh", "line": 1}},
	})
	data, _ := json.Marshal(s)
	facts := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(facts, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := BuildDocs(facts, "main", archdoc.Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`\{\s*\}`).Match(got) {
		t.Errorf("empty boundary in docs:\n%s", got)
	}
	if !strings.Contains(string(got), "### Stack empty (none)\n\nThis stack has no services.") {
		t.Errorf("empty stack not described:\n%s", got)
	}
}
