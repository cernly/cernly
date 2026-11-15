//go:build js && wasm

package main

import (
	"encoding/json"
	"os"
	"syscall/js"
	"testing"

	"github.com/cernly/cernly/pipeline"
)

func TestExports(t *testing.T) {
	g := js.Global()
	if g.Get("dispatchEvent").IsUndefined() {
		g.Set("dispatchEvent", js.FuncOf(func(js.Value, []js.Value) any { return true }))
		g.Set("Event", g.Get("Object")) // Node before EventTarget on the global: any constructor will do
	}
	register()
	raw, err := os.ReadFile("../../schema/example.facts.json")
	if err != nil {
		t.Fatal(err)
	}
	var ok map[string]any
	if err := json.Unmarshal([]byte(g.Get("cernly").Call("view", string(raw)).String()), &ok); err != nil {
		t.Fatal(err)
	}
	if ok["error"] != nil || ok["map"] == nil {
		t.Fatalf("view: %v", ok["error"])
	}
	var bad map[string]any
	if err := json.Unmarshal([]byte(g.Get("cernly").Call("view", "x").String()), &bad); err != nil {
		t.Fatal(err)
	}
	if bad["error"] == nil {
		t.Fatal("view of x gave no error")
	}
	var cmp map[string]any
	_ = json.Unmarshal([]byte(g.Get("cernly").Call("compare", string(raw), string(raw)).String()), &cmp)
	if cmp["error"] != nil || cmp["drift"] == nil {
		t.Fatalf("compare: %v", cmp["error"])
	}
	var up struct {
		Facts  string         `json:"facts"`
		Map    string         `json:"map"`
		Counts map[string]int `json:"counts"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal([]byte(g.Get("cernly").Call("redact", string(raw)).String()), &up); err != nil {
		t.Fatal(err)
	}
	if up.Error != "" || up.Facts == "" || up.Map == "" || up.Counts["repo"] != 1 {
		t.Fatalf("redact: %+v", up)
	}
	var nope map[string]any
	_ = json.Unmarshal([]byte(g.Get("cernly").Call("redact", "x").String()), &nope)
	if nope["error"] == nil {
		t.Fatal("redact of x gave no error")
	}
	if g.Get("cernly").Get("version").String() != pipeline.Version {
		t.Fatalf("version %s", g.Get("cernly").Get("version").String())
	}
}
