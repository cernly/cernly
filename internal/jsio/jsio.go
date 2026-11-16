//go:build js && wasm

// Package jsio is the scan module's bridge to JavaScript: waiting on a
// promise from a goroutine, and copying bytes across.
package jsio

import (
	"errors"
	"syscall/js"
)

// Await blocks the calling goroutine until the promise settles; the Go
// scheduler yields to the event loop meanwhile. Never call it from a js.FuncOf
// callback itself: start a goroutine.
func Await(promise js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	then := js.FuncOf(func(_ js.Value, a []js.Value) any {
		v := js.Undefined()
		if len(a) > 0 {
			v = a[0]
		}
		ch <- result{v: v}
		return nil
	})
	defer then.Release()
	catch := js.FuncOf(func(_ js.Value, a []js.Value) any {
		msg := "rejected"
		if len(a) > 0 {
			if m := a[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			} else {
				msg = js.Global().Get("String").Invoke(a[0]).String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	defer catch.Release()
	promise.Call("then", then, catch)
	r := <-ch
	return r.v, r.err
}

// Bytes copies a Uint8Array into Go.
func Bytes(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

// Array copies Go bytes into a new Uint8Array.
func Array(b []byte) js.Value {
	a := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(a, b)
	return a
}
