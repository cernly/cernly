package dotnet

// General C# token helpers, shared by the producers.

import "strings"

func isID(t token, s string) bool { return t.kind == tokIdent && t.text == s }
func isP(t token, s string) bool  { return t.kind == tokPunct && t.text == s }

// constStrings are the `const string X = "…";` of a file.
func constStrings(toks []token) map[string]string {
	out := map[string]string{}
	for i := 0; i+5 < len(toks); i++ {
		if isID(toks[i], "const") && isID(toks[i+1], "string") && toks[i+2].kind == tokIdent && isP(toks[i+3], "=") &&
			toks[i+4].kind == tokString && toks[i+4].literal && isP(toks[i+5], ";") {
			out[toks[i+2].text] = toks[i+4].text
		}
	}
	return out
}

// dotted reads a.b.c from toks[i] and returns it and the index after it.
func dotted(toks []token, i int) (string, int) {
	var b strings.Builder
	for i < len(toks) && toks[i].kind == tokIdent {
		b.WriteString(toks[i].text)
		i++
		if i+1 < len(toks) && isP(toks[i], ".") && toks[i+1].kind == tokIdent {
			b.WriteByte('.')
			i++
			continue
		}
		break
	}
	return b.String(), i
}

// closing is the index of the token that closes the open bracket at toks[i],
// or -1 when nothing within toks closes it. It reads the pairs the lexer
// matched, so it costs nothing however deep the nesting.
func closing(toks []token, i int, open, close string) int {
	if i < 0 || i >= len(toks) || !isP(toks[i], open) {
		return -1
	}
	if p := toks[i].pair; p > 0 && i+p < len(toks) && isP(toks[i+p], close) {
		return i + p
	}
	return -1
}

// splitTop cuts tokens at commas outside (), [] and {}. It jumps over each
// bracketed group by its pair; after an open that does not close within toks,
// no comma is at the top, so the rest is one piece.
func splitTop(toks []token) [][]token {
	var out [][]token
	start := 0
scan:
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			if t.pair == 0 || i+t.pair >= len(toks) {
				break scan
			}
			i += t.pair
		case ",":
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

func joinText(toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		b.WriteString(t.text)
	}
	return b.String()
}
