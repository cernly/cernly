package imageref

import (
	"regexp"
	"sort"
	"strings"
)

// Pinning grades.
const (
	PinDigest   = "digest"   // @sha256:...
	PinExact    = "exact"    // a full version: x.y.z or a release date, variant suffix allowed
	PinFloating = "floating" // latest, no tag, a major or major.minor, a branch or word tag
	PinUnknown  = "unknown"  // the tag comes from a variable without a default
)

// Ref is the artifact_ref of scan.v1.schema.json for an image.
type Ref struct {
	Raw        string
	Registry   string
	Repository string
	Tag        string
	Digest     string
	Templated  []string // variable names in Raw
	Pinning    string
}

// Attrs returns the ref as fact attrs.
func (r Ref) Attrs() map[string]any {
	a := map[string]any{"raw": r.Raw, "registry": r.Registry, "repository": r.Repository, "pinning": r.Pinning}
	if r.Tag != "" {
		a["tag"] = r.Tag
	}
	if r.Digest != "" {
		a["digest"] = r.Digest
	}
	if len(r.Templated) > 0 {
		t := make([]any, len(r.Templated))
		for i, v := range r.Templated {
			t[i] = v
		}
		a["templated"] = t
	}
	return a
}

// Parse reads an image reference as written. ${VAR:-default} templates show
// their default, nested ones too; a variable without a default stays as
// ${VAR} and makes what it covers unknown. Docker Hub names are normalised:
// postgres -> docker.io/library/postgres.
func Parse(raw string) Ref {
	r := Ref{Raw: raw}
	vars := map[string]bool{}
	val := expand(raw, vars)
	for v := range vars {
		r.Templated = append(r.Templated, v)
	}
	sort.Strings(r.Templated)

	name := val
	if i := strings.Index(name, "@"); i >= 0 {
		name, r.Digest = name[:i], name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost" || strings.Contains(parts[0], "${")) {
		r.Registry, parts = parts[0], parts[1:]
	}
	if r.Registry == "" || r.Registry == "index.docker.io" {
		r.Registry = "docker.io"
	}
	if r.Registry == "docker.io" && len(parts) == 1 {
		parts = append([]string{"library"}, parts...)
	}
	r.Repository = strings.Join(parts, "/")
	r.Pinning = pinning(r.Tag, r.Digest)
	return r
}

var (
	fullVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+._].*)?$`)
	releaseDate = regexp.MustCompile(`^(RELEASE\.)?[0-9]{4}-[0-9]{2}-[0-9]{2}(T.*)?$`)
)

func pinning(tag, digest string) string {
	switch {
	case strings.HasPrefix(digest, "sha256:") && !strings.Contains(digest, "${"):
		return PinDigest
	case strings.Contains(tag, "${"):
		return PinUnknown
	case fullVersion.MatchString(tag), releaseDate.MatchString(tag):
		return PinExact
	}
	return PinFloating
}

// expand applies ${VAR:-default}, ${VAR-default}, ${VAR} and $VAR as they read
// without an environment: defaults are shown, other variables kept.
func expand(s string, vars map[string]bool) string {
	var b strings.Builder
	expandRange(&b, s, 0, len(s), braces(s), vars, 0)
	return b.String()
}

// maxDepth caps ${ nesting, as the other parsers cap theirs; deeper text is
// kept as written.
const maxDepth = 1000

// expandRange expands s[lo:hi] into b; closes maps each ${ to its }.
func expandRange(b *strings.Builder, s string, lo, hi int, closes map[int]int, vars map[string]bool, depth int) {
	if depth > maxDepth {
		b.WriteString(s[lo:hi])
		return
	}
	for i := lo; i < hi; i++ {
		if s[i] != '$' || i+1 == hi {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] != '{' {
			j := i + 1
			for j < hi && (s[j] == '_' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			if j == i+1 {
				b.WriteByte('$')
				continue
			}
			vars[s[i+1:j]] = true
			b.WriteString("${" + s[i+1:j] + "}")
			i = j - 1
			continue
		}
		end, ok := closes[i]
		if !ok || end >= hi {
			b.WriteString(s[i:hi])
			return
		}
		body := s[i+2 : end]
		name, defAt := body, -1
		if k := strings.IndexAny(body, ":-?+"); k >= 0 {
			name = body[:k]
			op := k
			if body[op] == ':' {
				op++
			}
			if op < len(body) && body[op] == '-' {
				defAt = i + 2 + op + 1
			}
		}
		vars[name] = true
		if defAt >= 0 {
			expandRange(b, s, defAt, end, closes, vars, depth+1)
		} else {
			b.WriteString("${" + name + "}")
		}
		i = end
	}
}

// braces matches every ${ in s to the } that ends it, in one pass with a
// stack; an unclosed ${ has no entry.
func braces(s string) map[int]int {
	closes := map[int]int{}
	var open []int
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			open = append(open, i)
			i++
		case s[i] == '}' && len(open) > 0:
			closes[open[len(open)-1]] = i
			open = open[:len(open)-1]
		}
	}
	return closes
}
