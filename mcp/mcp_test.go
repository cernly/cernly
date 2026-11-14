package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// The get_map schema text lists the sections the tool serves, so a section
// added to the list without the tag fails here.
func TestMapInputDescribesEverySection(t *testing.T) {
	f, _ := reflect.TypeOf(MapInput{}).FieldByName("Section")
	want := "one of " + strings.Join(sections, ", ")
	if got := f.Tag.Get("jsonschema"); got != want {
		t.Errorf("Section description %q, want %q", got, want)
	}
}
