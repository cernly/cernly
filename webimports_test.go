package core_test

import "strings"

// webImports lists the module specifiers a TypeScript or JavaScript source
// imports or re-exports: import and export declarations (a clause may span
// lines and hold string names), side-effect imports and import(). It lexes the
// source first, so comments, strings, template literals and regular
// expressions never count, and several statements on one line are all seen.
func webImports(src string) []string {
	toks := lexWeb(src)
	var specs []string
	isStr := func(i int) bool { return i < len(toks) && toks[i].str }
	is := func(i int, s string) bool { return i >= 0 && i < len(toks) && !toks[i].str && toks[i].text == s }
	// clauseFrom finds the string after the first `from` of a declaration
	// clause that starts at i, and stops at anything a clause cannot hold.
	clauseFrom := func(i int) {
		for ; i < len(toks); i++ {
			switch {
			case is(i, "from") && isStr(i+1):
				specs = append(specs, toks[i+1].text)
				return
			case toks[i].str, is(i, "{"), is(i, "}"), is(i, ","), is(i, "*"), isIdent(toks[i]):
			default:
				return
			}
		}
	}
	for i := 0; i < len(toks); i++ {
		switch {
		case is(i, "import") && !is(i-1, "."):
			switch {
			case is(i+1, "("):
				if isStr(i + 2) {
					specs = append(specs, toks[i+2].text)
				}
			case is(i+1, "."):
			case isStr(i + 1):
				specs = append(specs, toks[i+1].text)
			default:
				clauseFrom(i + 1)
			}
		case is(i, "export") && !is(i-1, "."):
			j := i + 1
			if is(j, "type") {
				j++
			}
			if is(j, "{") || is(j, "*") {
				clauseFrom(j)
			}
		}
	}
	return specs
}

type webTok struct {
	text string
	str  bool
}

func isIdent(t webTok) bool {
	if t.str || t.text == "" {
		return false
	}
	c := t.text[0]
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func lexWeb(s string) []webTok {
	var toks []webTok
	i := 0
	// canRegex: a slash after these starts a regular expression, not a division.
	canRegex := func() bool {
		if len(toks) == 0 {
			return true
		}
		t := toks[len(toks)-1]
		if t.str {
			return false
		}
		switch t.text {
		case "(", ",", "=", ":", "[", "!", "&", "|", "?", "{", "}", ";", "return", "typeof", "=>", "+", "-", "*", "<", ">":
			return true
		}
		return false
	}
	var skipTemplate func()
	skipTemplate = func() { // i is just after the opening backtick
		for i < len(s) {
			switch s[i] {
			case '\\':
				i += 2
			case '`':
				i++
				return
			case '$':
				if i+1 < len(s) && s[i+1] == '{' {
					i += 2
					depth := 1
					for i < len(s) && depth > 0 {
						switch s[i] {
						case '{':
							depth++
						case '}':
							depth--
						case '`':
							i++
							skipTemplate()
							continue
						case '"', '\'':
							q := s[i]
							i++
							for i < len(s) && s[i] != q && s[i] != '\n' {
								if s[i] == '\\' {
									i++
								}
								i++
							}
						}
						i++
					}
					continue
				}
				i++
			default:
				i++
			}
		}
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = len(s)
			} else {
				i += end + 4
			}
		case c == '/' && canRegex():
			i++
			inClass := false
			for i < len(s) && s[i] != '\n' {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == '[' {
					inClass = true
				} else if s[i] == ']' {
					inClass = false
				} else if s[i] == '/' && !inClass {
					i++
					break
				}
				i++
			}
			for i < len(s) && (s[i] >= 'a' && s[i] <= 'z') {
				i++
			}
			toks = append(toks, webTok{text: "/re/"})
		case c == '"' || c == '\'':
			start := i + 1
			i++
			for i < len(s) && s[i] != c && s[i] != '\n' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			end := i
			if end > len(s) {
				end = len(s)
			}
			toks = append(toks, webTok{text: s[start:end], str: true})
			i++
		case c == '`':
			i++
			skipTemplate()
			toks = append(toks, webTok{text: "``", str: false})
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || c >= '0' && c <= '9':
			start := i
			for i < len(s) && (s[i] == '_' || s[i] == '$' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] >= 0x80) {
				i++
			}
			toks = append(toks, webTok{text: s[start:i]})
		default:
			toks = append(toks, webTok{text: string(c)})
			i++
		}
	}
	return toks
}
