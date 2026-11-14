package dotnet

import (
	"reflect"
	"strings"
	"testing"
)

type want struct {
	kind    tokKind
	text    string
	literal bool
	line    int
}

func TestLex(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []want
	}{
		{"escaped quote", `"a\"b"`, []want{{tokString, `a"b`, true, 1}}},
		{"escapes", `"a\tb\\c\u0041"`, []want{{tokString, "a\tb\\cA", true, 1}}},
		{"verbatim", `@"c:\x"" y"`, []want{{tokString, `c:\x" y`, true, 1}}},
		{"verbatim multi line", "@\"a\nb\" x", []want{{tokString, "a\nb", true, 1}, {tokIdent, "x", false, 2}}},
		{"empty string", `"" x`, []want{{tokString, "", true, 1}, {tokIdent, "x", false, 1}}},
		{"raw multi line", "x\n  \"\"\"\n    {\"a\": 1}\n      b\n    \"\"\"\ny", []want{
			{tokIdent, "x", false, 1},
			{tokString, "{\"a\": 1}\n  b", true, 2},
			{tokIdent, "y", false, 6},
		}},
		{"raw single line", `"""abc"""`, []want{{tokString, "abc", true, 1}}},
		{"raw with quote pair inside", `""""a "" b""""`, []want{{tokString, `a "" b`, true, 1}}},
		{"raw crlf", "\"\"\"\r\n  a\r\n  \"\"\"\r\nz", []want{{tokString, "a", true, 1}, {tokIdent, "z", false, 4}}},
		{"interpolated", `$"x{y}"`, []want{{tokString, `$"x{y}"`, false, 1}}},
		{"interpolated without holes", `$"abc"`, []want{{tokString, "abc", true, 1}}},
		{"interpolated empty", `$""`, []want{{tokString, "", true, 1}}},
		{"bom", "\xef\xbb\xbfusing X;", []want{{tokIdent, "using", false, 1}, {tokIdent, "X", false, 1}, {tokPunct, ";", false, 1}}},
		{"interpolated verbatim", `$@"{y}"`, []want{{tokString, `$@"{y}"`, false, 1}}},
		{"interpolated verbatim reversed", `@$"{y}"`, []want{{tokString, `@$"{y}"`, false, 1}}},
		{"interpolated raw", `$$"""{{y}}"""`, []want{{tokString, `$$"""{{y}}"""`, false, 1}}},
		{"string inside hole", `$"{a("https://x")}" z`, []want{{tokString, `$"{a("https://x")}"`, false, 1}, {tokIdent, "z", false, 1}}},
		{"escaped braces", `$"{{a}}" z`, []want{{tokString, `{a}`, true, 1}, {tokIdent, "z", false, 1}}},
		{"line comment", "// https://a.example\nx", []want{{tokIdent, "x", false, 2}}},
		{"block comment", "/* \"x\"\n */ y", []want{{tokIdent, "y", false, 2}}},
		{"directives", "#if DEBUG\na();\n#else\nb();\n#endif\n", []want{
			{tokIdent, "a", false, 2}, {tokPunct, "(", false, 2}, {tokPunct, ")", false, 2}, {tokPunct, ";", false, 2},
			{tokIdent, "b", false, 4}, {tokPunct, "(", false, 4}, {tokPunct, ")", false, 4}, {tokPunct, ";", false, 4},
		}},
		{"crlf lines", "a\r\nb\r\nc", []want{{tokIdent, "a", false, 1}, {tokIdent, "b", false, 2}, {tokIdent, "c", false, 3}}},
		{"char", `'"' x "s"`, []want{{tokChar, `"`, false, 1}, {tokIdent, "x", false, 1}, {tokString, "s", true, 1}}},
		{"escaped char", `'\''`, []want{{tokChar, `'`, false, 1}}},
		{"numbers", `1.5e+3m 0xFF`, []want{{tokNumber, "1.5e+3m", false, 1}, {tokNumber, "0xFF", false, 1}}},
		{"verbatim identifier", `@class x`, []want{{tokIdent, "@class", false, 1}, {tokIdent, "x", false, 1}}},
		{"dollar punct", `$ x`, []want{{tokPunct, "$", false, 1}, {tokIdent, "x", false, 1}}},
		{"unterminated string", `"abc`, []want{{tokString, "abc", true, 1}}},
		{"unterminated verbatim", `@"abc`, []want{{tokString, "abc", true, 1}}},
		{"unterminated raw", "\"\"\"abc", []want{{tokString, "abc", true, 1}}},
		{"unterminated interpolated", `$"a{b`, []want{{tokString, `$"a{b`, false, 1}}},
		{"unterminated comment", "x /* y", []want{{tokIdent, "x", false, 1}}},
		{"directive with indent", "  #region R\nx", []want{{tokIdent, "x", false, 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []want
			for _, k := range lex([]byte(tt.src)) {
				got = append(got, want{k.kind, k.text, k.literal, k.line})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lex(%q)\n got %+v\nwant %+v", tt.src, got, tt.want)
			}
		})
	}
}

func TestLexNeverPanics(t *testing.T) {
	src := "$@\"{a(\"\"x\"\")}\" \"\"\" $$\"\"\"{{ '\\ @ $ \" /* // #"
	for i := 0; i <= len(src); i++ {
		lex([]byte(src[:i]))
		lex([]byte(strings.Repeat(src[i:], 2)))
	}
}
