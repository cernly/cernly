package gitfs

import (
	"fmt"
	"strings"
)

// config holds git config values by "section.key" or "section.sub.key": the
// section and key lowercase, the subsection as written. Values keep file
// order, so the first is git's first.
type config map[string][]string

func (c config) get(key string) (string, bool) {
	v := c[key]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// parseConfig reads git's config syntax: [section], [section "sub"], the
// legacy [section.sub], key = value, a key alone (true), quoting, \" \\ \n \t
// \b escapes, # and ; comments and a backslash at a line end.
func parseConfig(data []byte) (config, error) {
	c := config{}
	s := string(data)
	s = strings.TrimPrefix(s, "\xEF\xBB\xBF")
	i, n := 0, len(s)
	section := ""
	line := 1
	fail := func(msg string) error { return fmt.Errorf("config line %d: %s", line, msg) }
	for i < n {
		ch := s[i]
		switch {
		case ch == '\n':
			line++
			i++
		case ch == ' ' || ch == '\t' || ch == '\r':
			i++
		case ch == '#' || ch == ';':
			for i < n && s[i] != '\n' {
				i++
			}
		case ch == '[':
			i++
			start := i
			for i < n && s[i] != ']' && s[i] != ' ' && s[i] != '\t' && s[i] != '"' && s[i] != '\n' {
				i++
			}
			name := s[start:i]
			sub := ""
			hasSub := false
			if i < n && (s[i] == ' ' || s[i] == '\t') {
				for i < n && (s[i] == ' ' || s[i] == '\t') {
					i++
				}
				if i >= n || s[i] != '"' {
					return nil, fail("bad section header")
				}
				i++
				var b strings.Builder
				for i < n && s[i] != '"' {
					if s[i] == '\n' {
						return nil, fail("newline in subsection")
					}
					if s[i] == '\\' && i+1 < n {
						i++
					}
					b.WriteByte(s[i])
					i++
				}
				if i >= n {
					return nil, fail("unterminated subsection")
				}
				i++ // closing quote
				sub, hasSub = b.String(), true
			}
			if i >= n || s[i] != ']' {
				return nil, fail("bad section header")
			}
			i++
			if !hasSub {
				if d := strings.IndexByte(name, '.'); d >= 0 { // legacy [section.sub]
					name, sub, hasSub = name[:d], strings.ToLower(name[d+1:]), true
				}
			}
			section = strings.ToLower(name)
			if hasSub {
				section += "." + sub
			}
		default:
			start := i
			for i < n && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] == '-') {
				i++
			}
			if i == start || section == "" {
				return nil, fail("bad key")
			}
			key := section + "." + strings.ToLower(s[start:i])
			for i < n && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			if i >= n || s[i] == '\n' || s[i] == '\r' || s[i] == '#' || s[i] == ';' {
				c[key] = append(c[key], "true")
				continue
			}
			if s[i] != '=' {
				return nil, fail("bad key")
			}
			i++
			for i < n && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			var b strings.Builder
			quoted := false
			trimTo := 0
		value:
			for i < n {
				ch := s[i]
				switch {
				case ch == '\n':
					break value
				case ch == '\r' && i+1 < n && s[i+1] == '\n':
					i++
				case ch == '\\':
					i++
					if i >= n {
						return nil, fail("dangling backslash")
					}
					switch e := s[i]; e {
					case '\n':
						line++
					case '\r':
						if i+1 < n && s[i+1] == '\n' {
							i++
							line++
						} else {
							return nil, fail("bad escape")
						}
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'b':
						b.WriteByte('\b')
					case '\\', '"':
						b.WriteByte(e)
					default:
						return nil, fail("bad escape")
					}
					trimTo = b.Len()
					i++
				case ch == '"':
					quoted = !quoted
					trimTo = b.Len()
					i++
				case !quoted && (ch == '#' || ch == ';'):
					for i < n && s[i] != '\n' {
						i++
					}
				default:
					b.WriteByte(ch)
					if quoted || (ch != ' ' && ch != '\t') {
						trimTo = b.Len()
					}
					i++
				}
			}
			if quoted {
				return nil, fail("unterminated quote")
			}
			v := b.String()
			if len(v) > trimTo {
				v = v[:trimTo]
			}
			c[key] = append(c[key], v)
		}
	}
	return c, nil
}
