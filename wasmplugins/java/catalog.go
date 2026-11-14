package main

import (
	"path"
	"strings"

	"github.com/cernly/cernly/fact"
)

// A Gradle version catalog (gradle/libs.versions.toml) read with a small
// reader for the TOML its format uses: [versions], [libraries], [bundles],
// key = "string", key = { k = "v", version.ref = "x" } and arrays of strings
// for bundles. Lines are kept for evidence.

type catEntry struct {
	group, artifact, version string
	ev                       []fact.Evidence
}

// catalog maps an accessor (spring.boot.starter.web, bundles.web) to its
// libraries.
type catalog map[string][]catEntry

// accessor turns an alias into Gradle's accessor: -, _ and . separate.
func accessor(alias string) string {
	return strings.NewReplacer("-", ".", "_", ".").Replace(alias)
}

func catalogs(p *project, paths []string, e *emitter) catalog {
	out := catalog{}
	for _, f := range paths {
		if path.Base(f) != "libs.versions.toml" || path.Base(path.Dir(f)) != "gradle" {
			continue
		}
		mod := path.Dir(path.Dir(f))
		tab := readTOML(p.files[f])
		versions := map[string]tomlVal{}
		for k, v := range tab["versions"] {
			versions[k] = v
		}
		libs := map[string]catEntry{}
		for alias, v := range tab["libraries"] {
			ce := catEntry{ev: []fact.Evidence{ev(f, v.line, mod)}}
			if v.str != "" {
				c := strings.SplitN(v.str, ":", 3)
				if len(c) < 2 {
					e.fail(f, v.line, "library "+alias+": not group:artifact[:version]")
					continue
				}
				ce.group, ce.artifact = c[0], c[1]
				if len(c) == 3 {
					ce.version = c[2]
				}
			} else {
				if m := v.table["module"].str; m != "" {
					c := strings.SplitN(m, ":", 2)
					if len(c) == 2 {
						ce.group, ce.artifact = c[0], c[1]
					}
				} else {
					ce.group, ce.artifact = v.table["group"].str, v.table["name"].str
				}
				if ref := v.table["version.ref"].str; ref != "" {
					if rv, ok := versions[ref]; ok {
						ce.version = rv.versionString()
						ce.ev = append(ce.ev, ev(f, rv.line, mod))
					} else {
						e.fail(f, v.line, "library "+alias+": version.ref "+ref+" not in [versions]")
					}
				} else if vv, ok := v.table["version"]; ok {
					ce.version = vv.versionString()
				}
				if ce.group == "" || ce.artifact == "" {
					e.fail(f, v.line, "library "+alias+": no module or group and name")
					continue
				}
			}
			libs[accessor(alias)] = ce
			out[accessor(alias)] = []catEntry{ce}
		}
		for alias, v := range tab["bundles"] {
			var list []catEntry
			for _, item := range v.list {
				ce, ok := libs[accessor(item)]
				if !ok {
					e.fail(f, v.line, "bundle "+alias+": library "+item+" not found")
					continue
				}
				ce.ev = append(append([]fact.Evidence(nil), ce.ev...), ev(f, v.line, mod))
				list = append(list, ce)
			}
			out["bundles."+accessor(alias)] = list
		}
	}
	return out
}

type tomlVal struct {
	str   string
	table map[string]tomlVal
	list  []string
	line  int
}

// versionString reads a version entry: "1.2" or { strictly / require /
// prefer = "1.2" }.
func (v tomlVal) versionString() string {
	if v.str != "" {
		return v.str
	}
	for _, k := range []string{"strictly", "require", "prefer"} {
		if s := v.table[k].str; s != "" {
			return s
		}
	}
	return ""
}

// readTOML reads [section] headers and key = value lines, values being
// strings, inline tables of strings (one level, dotted keys kept as written)
// and arrays of strings, possibly over several lines.
func readTOML(data []byte) map[string]map[string]tomlVal {
	out := map[string]map[string]tomlVal{}
	section := ""
	lines := strings.Split(string(data), "\n")
	for n := 0; n < len(lines); n++ {
		line := strings.TrimSpace(stripComment(lines[n]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if out[section] == nil {
				out[section] = map[string]tomlVal{}
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 || out[section] == nil {
			continue
		}
		key := unquote(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		start := n + 1
		// Gather a multi-line array or inline table.
		for (strings.HasPrefix(val, "[") && !strings.Contains(val, "]")) || (strings.HasPrefix(val, "{") && !strings.Contains(val, "}")) {
			if n+1 >= len(lines) {
				break
			}
			n++
			val += " " + strings.TrimSpace(stripComment(lines[n]))
		}
		v := tomlVal{line: start}
		switch {
		case strings.HasPrefix(val, `"`) || strings.HasPrefix(val, `'`):
			v.str = unquote(val)
		case strings.HasPrefix(val, "{"):
			v.table = map[string]tomlVal{}
			body := strings.TrimSuffix(strings.TrimPrefix(val, "{"), "}")
			for _, kv := range splitTop(body) {
				i := strings.Index(kv, "=")
				if i < 0 {
					continue
				}
				k := strings.ReplaceAll(unquote(strings.TrimSpace(kv[:i])), " ", "")
				rv := strings.TrimSpace(kv[i+1:])
				if strings.HasPrefix(rv, "{") {
					// version = { strictly = "1.2" }
					inner := map[string]tomlVal{}
					for _, ikv := range splitTop(strings.TrimSuffix(strings.TrimPrefix(rv, "{"), "}")) {
						if j := strings.Index(ikv, "="); j >= 0 {
							inner[unquote(strings.TrimSpace(ikv[:j]))] = tomlVal{str: unquote(strings.TrimSpace(ikv[j+1:]))}
						}
					}
					v.table[k] = tomlVal{table: inner}
					continue
				}
				v.table[k] = tomlVal{str: unquote(rv)}
			}
		case strings.HasPrefix(val, "["):
			body := strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
			for _, item := range splitTop(body) {
				if s := unquote(strings.TrimSpace(item)); s != "" {
					v.list = append(v.list, s)
				}
			}
		}
		out[section][key] = v
	}
	return out
}

// splitTop splits on commas outside quotes and braces.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '{':
			depth++
		case c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}

func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#':
			return s[:i]
		}
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
