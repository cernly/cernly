// Command probe is a test plugin: its behaviour is chosen by the content of
// the file mode.txt it is sent.
package main

import (
	"net"
	"os"
	"strings"
	"time"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin/sdk"
)

func main() { sdk.Main(run) }

func ev(p string) []fact.Evidence { return []fact.Evidence{{Path: p, Line: 1}} }

func env(name, p string) fact.Fact {
	return fact.Fact{Kind: fact.KindEnvVar, Key: "env_var:" + name,
		Attrs: map[string]any{"name": name, "origins": []any{"config"}}, Evidence: ev(p)}
}

func run(in *sdk.Input) sdk.Output {
	f, ok := in.File("mode.txt")
	if !ok {
		return sdk.Output{}
	}
	mode := strings.TrimSpace(string(f.Content))
	switch mode {
	case "ok":
		return sdk.Output{Facts: []fact.Fact{env("PROBE_OK", "mode.txt")}}
	case "reject":
		return sdk.Output{Facts: []fact.Fact{
			env("PROBE_OK", "mode.txt"),
			env("PROBE_OUTSIDE", "secret.txt"),
			{Kind: fact.KindService, Key: "service:x:y", Attrs: map[string]any{"name": "y"}, Evidence: ev("mode.txt")},
			{Kind: fact.KindEnvVar, Key: "env_var:BAD", Attrs: map[string]any{"name": "BAD"}, Evidence: ev("mode.txt")},
			{Kind: fact.KindEnvVar, Key: "env_var:OTHER", Plugin: "env", Attrs: map[string]any{"name": "OTHER", "origins": []any{"config"}}, Evidence: ev("mode.txt")},
		}, Errors: []fact.LocationError{{Path: "mode.txt", Line: 1, Message: "seen"}}}
	case "sandbox":
		// Everything a sandbox must deny, reported as facts the test reads.
		var out []fact.Fact
		if _, err := os.ReadFile("/etc/hostname"); err == nil {
			out = append(out, env("SANDBOX_FILE_READ", "mode.txt"))
		}
		if _, err := os.ReadDir("."); err == nil {
			out = append(out, env("SANDBOX_DIR_READ", "mode.txt"))
		}
		if len(os.Environ()) > 0 {
			out = append(out, env("SANDBOX_ENV", "mode.txt"))
		}
		if err := os.WriteFile("x", []byte("x"), 0o644); err == nil {
			out = append(out, env("SANDBOX_WRITE", "mode.txt"))
		}
		if _, err := net.Dial("tcp", "127.0.0.1:80"); err == nil {
			out = append(out, env("SANDBOX_NET", "mode.txt"))
		}
		if time.Now().Year() >= 2025 {
			out = append(out, env("SANDBOX_REAL_CLOCK", "mode.txt"))
		}
		return sdk.Output{Facts: out}
	case "loop":
		for {
		}
	case "exit":
		os.Stderr.WriteString("probe failed on purpose\n")
		os.Exit(3)
	case "memory":
		var keep [][]byte
		for {
			keep = append(keep, make([]byte, 64<<20))
		}
	}
	return sdk.Output{}
}
