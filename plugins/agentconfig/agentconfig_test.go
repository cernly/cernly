package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cernly/cernly/repo"
)

func TestAgentConfig(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{".cursor/rules/a.mdc", ".cursorrules", "AGENTS.md", "web/AGENTS.md", "web/CLAUDE.md",
		"opencode.json", ".opencode/commands/x.md", ".no-mistakes.yaml", "docs/cursor-notes.md", "src/app.ts"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range (Plugin{}).Run(r).Facts {
		got = append(got, fmt.Sprintf("%s:%v", f.Key, f.Attrs["files"]))
	}
	want := "[agent_config:agents-md:2 agent_config:claude:1 agent_config:cursor:2 agent_config:opencode:2]"
	if fmt.Sprint(got) != want {
		t.Errorf("got %v\nwant %s", got, want)
	}
}
