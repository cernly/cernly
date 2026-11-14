package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// Only facts files with the current schema id map; any other id is refused.
func TestMapSchemaIDs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "fixture.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	cur := []byte(`"` + fact.SchemaID + `"`)
	if !bytes.Contains(raw, cur) {
		t.Fatalf("fixture does not carry %s", fact.SchemaID)
	}
	for id, ok := range map[string]bool{fact.SchemaID: true, "other.scan/v1": false} {
		_, err := Map(bytes.Replace(raw, cur, []byte(`"`+id+`"`), 1), rules.Settings{DefaultBranch: "main"})
		if (err == nil) != ok {
			t.Errorf("schema %s: err = %v, want ok = %v", id, err, ok)
		}
	}
}
