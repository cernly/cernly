package imageref

import (
	"strings"
	"testing"

	"github.com/cernly/cernly/internal/scaling"
)

func TestExpand(t *testing.T) {
	for in, want := range map[string]string{
		"a:${T:-${U:-1}}":   "a:1",
		"a:${T-x}${U}":      "a:x${U}",
		"a:${T:+x}":         "a:${T}",
		"a:${T:}":           "a:${T}",
		"a:$T-$":            "a:${T}-$",
		"$${A}":             "$${A}",
		"a:${T:-${U}":       "a:${T:-${U}",
		"a}:${T:-}}b":       "a}:}b",
		"x${A:-${B:-${C}}}": "x${C}",
	} {
		if got := expand(in, map[string]bool{}); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
}

// Deep nesting is linear, and past maxDepth kept as written.
func TestExpandDeep(t *testing.T) {
	scaling.Linear(t, 20000, func(n int) func() {
		raw := strings.Repeat("${A:-", n) + "x" + strings.Repeat("}", n)
		return func() { expand(raw, map[string]bool{}) }
	})
	n := 80000
	raw := strings.Repeat("${A:-", n) + "x" + strings.Repeat("}", n)
	got := expand(raw, map[string]bool{})
	want := strings.Repeat("${A:-", n-maxDepth-1) + "x" + strings.Repeat("}", n-maxDepth-1)
	if got != want {
		t.Errorf("expand kept %d bytes, want %d", len(got), len(want))
	}
	if got := expand(strings.Repeat("${A:-", 10)+"x"+strings.Repeat("}", 10), map[string]bool{}); got != "x" {
		t.Errorf("shallow = %q", got)
	}
}
