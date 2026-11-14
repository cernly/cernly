// Package ghfilter matches GitHub Actions filter patterns (branches, tags,
// paths) as the filter pattern cheat sheet describes them.
package ghfilter

import (
	"regexp"
	"strings"
	"sync"
)

// maxCached bounds the compiled-pattern cache: patterns come from the
// workflows of every repo a long-running process scans, so the cache is emptied when full
// rather than growing for the life of the process.
const maxCached = 1024

var (
	mu    sync.Mutex
	cache = map[string]*regexp.Regexp{}
)

// Match reports whether s matches one pattern: * any run without /, ** any
// run, ? and + zero-or-one and one-or-more of the preceding character,
// [a-z0-9] one listed character, \ escapes. A leading ! is not special here.
func Match(pattern, s string) bool {
	mu.Lock()
	re, ok := cache[pattern]
	mu.Unlock()
	if !ok {
		re = compile(pattern)
		mu.Lock()
		if len(cache) >= maxCached {
			clear(cache)
		}
		cache[pattern] = re
		mu.Unlock()
	}
	return re.MatchString(s)
}

// Filter applies patterns in order, as GitHub does: a later match decides, a
// ! pattern excludes what earlier ones included.
func Filter(patterns []string, s string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if Match(strings.TrimPrefix(p, "!"), s) {
			matched = !neg
		}
	}
	return matched
}

func compile(pattern string) *regexp.Regexp {
	p := []rune(pattern)
	var atoms []string
	quantified := false // the last atom already repeats; ? or + after it is literal
	lit := func(r rune) { atoms, quantified = append(atoms, regexp.QuoteMeta(string(r))), false }
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '\\':
			if i+1 < len(p) {
				i++
			}
			lit(p[i])
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				for i+1 < len(p) && p[i+1] == '*' {
					i++
				}
				atoms = append(atoms, ".*")
			} else {
				atoms = append(atoms, "[^/]*")
			}
			quantified = true
		case '?', '+':
			if len(atoms) == 0 || quantified {
				lit(c)
				continue
			}
			atoms[len(atoms)-1] += string(c)
			quantified = true
		case '[':
			end := i + 1
			for end < len(p) && p[end] != ']' {
				end++
			}
			if end == len(p) || end == i+1 {
				lit(c)
				continue
			}
			var b strings.Builder
			b.WriteByte('[')
			for _, r := range p[i+1 : end] {
				if r == '-' {
					b.WriteByte('-')
				} else {
					b.WriteString(regexp.QuoteMeta(string(r)))
				}
			}
			b.WriteByte(']')
			if _, err := regexp.Compile(b.String()); err != nil {
				lit(c) // a bad range such as [z-a]
				continue
			}
			atoms, quantified = append(atoms, b.String()), false
			i = end
		default:
			lit(c)
		}
	}
	return regexp.MustCompile("^" + strings.Join(atoms, "") + "$")
}
