// Package imageref reads image references as written, ${VAR:-default}
// templates included. The full parse (registry, tag, digest, pinning) is
// the artifact reference; this holds only what links need. See
// https://cernly.dev/how/facts/.
package imageref

import "strings"

// Repo returns the last path segment of an image reference without tag or
// digest: "${REGISTRY:-localhost:5000}/acme/api:${TAG:-latest}" -> "api".
// Separators inside ${...} do not count.
func Repo(ref string) string {
	depth, last, tag := 0, -1, -1
	for i := 0; i < len(ref); i++ {
		switch {
		case strings.HasPrefix(ref[i:], "${"):
			depth++
			i++
		case ref[i] == '}' && depth > 0:
			depth--
		case depth > 0:
		case ref[i] == '@':
			return segment(ref[:i], last, tag)
		case ref[i] == '/':
			last, tag = i, -1
		case ref[i] == ':':
			if tag < 0 {
				tag = i
			}
		}
	}
	return segment(ref, last, tag)
}

func segment(ref string, last, tag int) string {
	if tag > last {
		ref = ref[:tag]
	}
	return ref[last+1:]
}

// Name returns an image reference without its tag or digest, as written:
// "ghcr.io/acme/api:${TAG}" -> "ghcr.io/acme/api". A ":" before the last "/"
// (a registry port) and separators inside ${...} are kept.
func Name(ref string) string {
	depth, last := 0, -1
	for i := 0; i < len(ref); i++ {
		switch {
		case strings.HasPrefix(ref[i:], "${"):
			depth++
			i++
		case ref[i] == '}' && depth > 0:
			depth--
		case depth > 0:
		case ref[i] == '@':
			return Name(ref[:i])
		case ref[i] == '/':
			last = i
		case ref[i] == ':' && i > last && !strings.Contains(ref[i:], "/"):
			return ref[:i]
		}
	}
	return ref
}
