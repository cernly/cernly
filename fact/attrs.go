package fact

import (
	"maps"
	"slices"
)

// Str reads a string attr; any other type reads as "".
func (f Fact) Str(k string) string {
	v, _ := f.Attrs[k].(string)
	return v
}

// Strs reads a list attr; a string attr reads as a one-item list.
func (f Fact) Strs(k string) []string {
	if v, ok := f.Attrs[k].(string); ok {
		return []string{v}
	}
	xs, _ := f.Attrs[k].([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if v, ok := x.(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// Strings converts a string slice to the []any shape attrs hold.
func Strings(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// SortedSet is the members of a set as an attr list, in sorted order.
func SortedSet(m map[string]bool) []any {
	return Strings(slices.Sorted(maps.Keys(m)))
}

// SortedValues is the values of a map as an attr list, in key order.
func SortedValues[V any](m map[string]V) []any {
	out := make([]any, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, m[k])
	}
	return out
}
