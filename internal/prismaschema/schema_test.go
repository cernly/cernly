package prismaschema

import "testing"

func TestParseErrors(t *testing.T) {
	for src, line := range map[string]int{
		"model A {\n  id Int @id\n":             1, // not closed
		"model A {\n  s String @default(\"x\n}": 2, // string across a line
		"\n\nmodel {\n}\n":                      3, // no name
		"model A {\n  id Int # bad\n}\n":        2,
	} {
		_, err := Parse("s.prisma", []byte(src))
		if err == nil {
			t.Errorf("%q parsed", src)
			continue
		}
		if got := LineOf(err); got != line {
			t.Errorf("%q: error on line %d, want %d (%v)", src, got, line, err)
		}
	}
}
