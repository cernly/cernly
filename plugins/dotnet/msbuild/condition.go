package msbuild

import "strings"

// Conditions are decided only when the answer is exact: a conjunction (as
// joinCond builds them) of comparisons '$(Name)' == 'literal' or !=, either
// side first, each side quoted or bare. Every property it names must be known:
// one unset for this project is the empty string only when some project or
// props file of the repo sets it (reader.setInRepo); one no file sets may come
// from the environment and is unknown, as is one set under a condition or
// computed, and one MSBuild, the SDK or the command line may set (see
// unsetUnknown). A parenthesised group, negated or not, is one operand that
// is never decided. Anything else is undecided.

type tri int

const (
	undecided tri = iota
	isFalse
	isTrue
)

// decide evaluates a condition against the properties after the property pass.
func (v *eval) decide(cond string) tri {
	toks, ok := condTokens(cond)
	if !ok {
		return undecided
	}
	var parts [][]condToken
	start, depth := 0, 0
	for i, t := range toks {
		switch {
		case t.kind == tokOther && t.text == "(":
			depth++
		case t.kind == tokOther && t.text == ")":
			depth--
		case t.kind == tokWord && strings.EqualFold(t.text, "or"):
			return undecided // or binds looser than and: no conjunction
		case t.kind == tokWord && strings.EqualFold(t.text, "and") && depth == 0:
			// An and inside parentheses belongs to a group, which compare
			// never decides: !(a and b) is not a conjunction of a and b.
			parts = append(parts, toks[start:i])
			start = i + 1
		}
	}
	parts = append(parts, toks[start:])
	out := isTrue
	for _, p := range parts {
		switch v.compare(p) {
		case isFalse:
			return isFalse
		case undecided:
			out = undecided
		}
	}
	return out
}

func (v *eval) compare(p []condToken) tri {
	if len(p) != 3 || p[1].kind != tokOp {
		return undecided
	}
	l, ok := v.operand(p[0])
	if !ok {
		return undecided
	}
	r, ok := v.operand(p[2])
	if !ok {
		return undecided
	}
	eq := strings.EqualFold(l, r)
	if !eq && typed(l) && typed(r) {
		return undecided // MSBuild compares numbers, versions and booleans as such
	}
	if eq == (p[1].text == "==") {
		return isTrue
	}
	return isFalse
}

// operand is the value of a literal or of exactly one property reference.
func (v *eval) operand(t condToken) (string, bool) {
	if t.kind != tokString && t.kind != tokWord {
		return "", false
	}
	s := t.text
	if strings.HasPrefix(s, "$(") && strings.HasSuffix(s, ")") && isName(s[2:len(s)-1]) {
		key := strings.ToLower(s[2 : len(s)-1])
		if v.unknown[key] {
			return "", false
		}
		if val, ok := v.props[key]; ok {
			return val, true
		}
		if unsetUnknown(key) || !v.e.setInRepo[key] {
			return "", false
		}
		return "", true
	}
	if strings.ContainsAny(s, "$@%") {
		return "", false // other expansions, item lists, metadata, escapes
	}
	return s, true
}

// unsetUnknown reports properties that have a value at item evaluation even
// when no file the reader sees sets them: MSBuild's reserved properties, and
// the ones the SDK, a solution build or the command line supply.
func unsetUnknown(key string) bool {
	if strings.HasPrefix(key, "msbuild") || strings.HasPrefix(key, "solution") ||
		strings.HasPrefix(key, "targetframework") || strings.HasPrefix(key, "targetplatform") {
		return true
	}
	switch key {
	case "configuration", "platform", "platformtarget", "runtimeidentifier", "os",
		"buildinginsidevisualstudio", "designtimebuild", "buildingproject":
		return true
	}
	return false
}

// typed reports a value MSBuild may compare as a number, a version or a
// boolean rather than as a string.
func typed(s string) bool {
	switch strings.ToLower(strings.TrimPrefix(s, "!")) {
	case "true", "false", "on", "off", "yes", "no":
		return true
	}
	if s == "" {
		return false
	}
	for _, c := range strings.ToLower(s) {
		if !(c >= '0' && c <= '9' || c == '.' || c == '+' || c == '-' || c == 'x' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s[0] >= '0' && s[0] <= '9' || s[0] == '.' || s[0] == '+' || s[0] == '-'
}

type tokKind int

const (
	tokString tokKind = iota // '...' without its quotes
	tokWord                  // a bare run of other characters, $(...) included
	tokOp                    // == or !=
	tokOther                 // parentheses, !, <, >, ...
)

type condToken struct {
	kind tokKind
	text string
}

// condTokens splits a condition the way MSBuild scans it: a property, item or
// metadata reference is one span up to its matching parenthesis, quotes inside
// it included. It fails on an unterminated string or reference.
func condTokens(s string) ([]condToken, bool) {
	var out []condToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != '\''; j++ {
				if k, ok := refEnd(s, j); ok {
					b.WriteString(s[j:k])
					j = k - 1
					continue
				} else if k < 0 {
					return nil, false
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, false
			}
			out = append(out, condToken{tokString, b.String()})
			i = j + 1
		case (c == '=' || c == '!') && i+1 < len(s) && s[i+1] == '=':
			out = append(out, condToken{tokOp, s[i : i+2]})
			i += 2
		case strings.IndexByte("()!<>=,", c) >= 0:
			out = append(out, condToken{tokOther, string(c)})
			i++
		default:
			j := i
			for j < len(s) && strings.IndexByte(" \t\r\n'()!<>=,", s[j]) < 0 {
				if k, ok := refEnd(s, j); ok {
					j = k
					continue
				} else if k < 0 {
					return nil, false
				}
				j++
			}
			out = append(out, condToken{tokWord, s[i:j]})
			i = j
		}
	}
	return out, true
}

// refEnd returns the end of a $( @( %( reference starting at i, past its
// matching parenthesis; ok is false when none starts there, and the end is -1
// when it is unterminated.
func refEnd(s string, i int) (int, bool) {
	if i+1 >= len(s) || s[i+1] != '(' || strings.IndexByte("$@%", s[i]) < 0 {
		return 0, false
	}
	depth := 0
	var quote byte
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return -1, false
}
