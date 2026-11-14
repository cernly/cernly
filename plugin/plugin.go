// Package plugin defines the contract every extractor plugin follows: read
// tracked files, return facts with evidence, nothing else. No network, no
// clock, no randomness, no environment variables.
//
// Plugins are stack adapters: each translates one stack's files into the shared
// fact vocabulary in package fact. Built-in plugins register themselves from an
// init function; see https://cernly.dev/how/plugins/ for WebAssembly plugins.
package plugin

import (
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo"
)

// Result is what one plugin run returns: its facts, the errors it hit and
// whether the run failed as a whole.
type Result struct {
	Facts  []fact.Fact
	Errors []fact.LocationError
	// Failed marks a run that produced nothing usable (a WebAssembly module
	// that trapped, ran out of time or wrote invalid output); Errors says why.
	Failed bool
}

// Plugin reads the tracked files of a repo and returns facts with evidence.
// Run must be deterministic and must not touch the network, the clock or the
// environment.
type Plugin interface {
	Name() string
	Version() string
	// Watches reports whether a change to this repo-relative path can change
	// the plugin's output. Core uses it to choose the files a WebAssembly plugin
	// receives and the files kubernetes reads; a caller may use it to skip a
	// plugin when only other files changed.
	Watches(path string) bool
	Run(r *repo.Repo) Result
}

// Consumer is a phase-2 plugin: a tool adapter that runs after every plain
// plugin and gets their facts of the kinds it consumes, such as the stacks an
// invocation reader found. Phase-2 plugins never see each other's output.
type Consumer interface {
	Plugin
	Consumes() []string
	RunWith(r *repo.Repo, in []fact.Fact) Result
}

// Origin is implemented by plugins that are not compiled in: WebAssembly
// modules. The scan records where each came from.
type Origin interface {
	Source() string // fact.SourceWasm
	SHA256() string // hex hash of the module
	Signer() string // key id that signed it
}

// NamePattern is the rule every plugin name follows; the scan schema uses the
// same pattern.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

var (
	mu       sync.Mutex
	registry = map[string]Plugin{}
)

// Register adds a built-in plugin. It panics on an invalid or duplicate name,
// because both are programming errors caught by any test run.
func Register(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	name := p.Name()
	if !NamePattern.MatchString(name) {
		panic(fmt.Sprintf("plugin name %q does not match %s", name, NamePattern))
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("plugin %q registered twice", name))
	}
	registry[name] = p
}

// All returns every registered plugin, sorted by name, so the run order and
// therefore the output never depend on import order.
func All() []Plugin {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Plugin, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
