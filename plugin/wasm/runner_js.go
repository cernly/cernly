//go:build js

package wasm

import (
	"errors"
	"math/rand"
	"sync"
	"syscall/js"

	"github.com/cernly/cernly/internal/jsio"
)

// engine runs a module in the browser's own engine: the page's
// cernlyRunPlugin (web/viewer/wasi.js in a Worker of its own) takes the
// verified bytes and stdin and settles with the run. Nothing to compile ahead.
type engine struct{}

func (p *Plugin) load() error {
	if js.Global().Get("cernlyRunPlugin").Type() != js.TypeFunction {
		return errors.New("this page cannot run outside plugins")
	}
	return nil
}

// Close releases nothing: each run's Worker ends with the run.
func (p *Plugin) Close() error { return nil }

// randomBytes is the stream wazero's default random source gives a module:
// math/rand seeded 42. A plugin that reads more fails, never gets other bytes.
var randomBytes = sync.OnceValue(func() []byte {
	b := make([]byte, 64<<10)
	_, _ = rand.New(rand.NewSource(42)).Read(b)
	return b
})

func (p *Plugin) exec(stdin []byte) runOut {
	opts := js.Global().Get("Object").New()
	opts.Set("name", p.m.Name)
	opts.Set("random", jsio.Array(randomBytes()))
	opts.Set("maxPages", p.limits.MemoryPages)
	opts.Set("maxOut", p.limits.Output)
	opts.Set("maxErr", 4096)
	opts.Set("timeoutMs", p.limits.Timeout.Milliseconds())
	v, err := jsio.Await(js.Global().Call("cernlyRunPlugin", jsio.Array(p.module), jsio.Array(stdin), opts))
	if err != nil {
		return runOut{err: err}
	}
	o := runOut{}
	if s := v.Get("stdout"); s.Truthy() {
		o.stdout = jsio.Bytes(s)
	}
	if s := v.Get("stderr"); s.Truthy() {
		o.stderr = jsio.Bytes(s)
	}
	if e := v.Get("exit"); e.Type() == js.TypeNumber {
		o.exit = e.Int()
	}
	o.overOut = v.Get("overOut").Truthy()
	o.timedOut = v.Get("timedOut").Truthy()
	if e := v.Get("error"); e.Type() == js.TypeString {
		o.err = errors.New(e.String())
	}
	return o
}
