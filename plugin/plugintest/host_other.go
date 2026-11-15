//go:build !js

package plugintest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cernly/cernly/pipeline"
)

func compile(t testing.TB, pkg string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s for wasip1: %v\n%s", pkg, err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func useRunner(testing.TB) {}

var scanWorkTree = pipeline.ScanWorkTree
