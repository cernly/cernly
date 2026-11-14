// Package sdk is what a WebAssembly plugin imports: the wire format between
// the scanner and a plugin, and a Main that reads the input from stdin and
// writes the output to stdout.
//
// A plugin gets the tracked, non-excluded files that match its manifest's
// watches, plus, for a phase-2 plugin, the phase-1 facts it consumes. It
// returns facts in the shared vocabulary (schema/scan.v1.schema.json) whose
// evidence points only at files or facts it was sent. It has no file
// system, no network, no environment and a fixed clock, so it cannot do
// anything else.
//
// The package uses only the standard library and the fact package, so it builds for
// GOOS=wasip1 GOARCH=wasm with the Go toolchain and with TinyGo. See
// https://cernly.dev/how/wasm-plugins/.
package sdk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/cernly/cernly/fact"
)

// API is the plugin protocol version this package speaks.
const API = 1

// Manifest describes a plugin; it is signed together with the module's hash
// (schema/plugin.v1.schema.json, "manifest").
type Manifest struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	PluginAPI int      `json:"plugin_api"`
	Watches   []string `json:"watches"`
	Emits     []string `json:"emits"`
	Consumes  []string `json:"consumes,omitempty"`
	// Tools narrows consumed stack facts to these tools.
	Tools []string `json:"tools,omitempty"`
	// SHA256 is the hex hash of the .wasm module this manifest belongs to.
	SHA256 string `json:"sha256"`
}

// File is one tracked file the scanner sends.
type File struct {
	Path    string `json:"path"`
	Content []byte `json:"content_b64"`
}

// Repo holds the files sent, sorted by path.
type Repo struct {
	Files []File `json:"files"`
}

// Input is what the plugin reads on stdin.
type Input struct {
	PluginAPI int         `json:"plugin_api"`
	Repo      Repo        `json:"repo"`
	Facts     []fact.Fact `json:"facts"`
}

// Output is what the plugin writes on stdout.
type Output struct {
	Facts  []fact.Fact          `json:"facts"`
	Errors []fact.LocationError `json:"errors"`
}

// Main reads the input, calls run and writes its output. A plugin's main
// function is one line: sdk.Main(run).
func Main(run func(in *Input) Output) {
	if err := Serve(os.Stdin, os.Stdout, run); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Serve is Main with explicit streams, for tests run on the host.
func Serve(r io.Reader, w io.Writer, run func(in *Input) Output) error {
	var in Input
	if err := json.NewDecoder(bufio.NewReader(r)).Decode(&in); err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	if in.PluginAPI != API {
		return fmt.Errorf("plugin_api %d, this plugin speaks %d", in.PluginAPI, API)
	}
	out := run(&in)
	if out.Facts == nil {
		out.Facts = []fact.Fact{}
	}
	if out.Errors == nil {
		out.Errors = []fact.LocationError{}
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return err
	}
	return bw.Flush()
}

// File returns the sent file at path.
func (in *Input) File(p string) (File, bool) {
	for _, f := range in.Repo.Files {
		if f.Path == p {
			return f, true
		}
	}
	return File{}, false
}

// Line returns the 1-based line of a byte offset in content.
func Line(content []byte, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	n := 1
	for _, b := range content[:offset] {
		if b == '\n' {
			n++
		}
	}
	return n
}

// Match reports whether a watch pattern covers a repo-relative file: the
// pattern matches the file itself or one of its parent folders, `**` spans
// any number of folders and other segments use path.Match syntax. It is the
// rule .cernly.yml exclusions follow.
func Match(pattern, file string) bool {
	p := strings.Split(strings.TrimSuffix(pattern, "/"), "/")
	n := strings.Split(file, "/")
	for i := 1; i <= len(n); i++ {
		if matchSegs(p, n[:i]) {
			return true
		}
	}
	return false
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for i := 0; i <= len(n); i++ {
				if matchSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, err := path.Match(p[0], n[0]); err != nil || !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}
