//go:build js && wasm

// Command cernly-viewer is the local viewer's WebAssembly module. It sets
// globalThis.cernly.view(facts) and .compare(a, b), each returning JSON, and
// .scan(source, name, plugins, trust), a promise of the scan of the folder
// the page picked (see scan.go), then fires "cernly-ready". It reads nothing
// but what it is given.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/cernly/cernly/localview"
)

func result(out []byte, err error) any {
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	return string(out)
}

func arg(args []js.Value, i int) []byte {
	if i >= len(args) || args[i].Type() != js.TypeString {
		return nil
	}
	return []byte(args[i].String())
}

func register() {
	api := js.Global().Get("Object").New()
	api.Set("view", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return result(localview.Of(arg(args, 0)))
	}))
	api.Set("compare", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return result(localview.Compare(arg(args, 0), arg(args, 1)))
	}))
	api.Set("scan", js.FuncOf(func(_ js.Value, args []js.Value) any {
		src, name, bundled, trust, err := scanArgs(args)
		return promise(func() (any, error) {
			if err != nil {
				return nil, err
			}
			return scanFolder(src, name, bundled, trust)
		})
	}))
	api.Set("maxBytes", localview.MaxBytes)
	api.Set("maxFiles", MaxFiles)
	js.Global().Set("cernly", api)
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("cernly-ready"))
}

func main() {
	register()
	select {}
}
