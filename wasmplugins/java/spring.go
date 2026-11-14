package main

import (
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/hostname"
)

// isSpringConfig reports Spring Boot's config files: application.yml,
// application-<profile>.properties, bootstrap.yml.
func isSpringConfig(f string) bool {
	if isTestSource(f) {
		return false
	}
	b := path.Base(f)
	for _, stem := range []string{"application", "bootstrap"} {
		if !strings.HasPrefix(b, stem) {
			continue
		}
		rest := strings.TrimPrefix(b, stem)
		for _, ext := range []string{".yml", ".yaml", ".properties"} {
			if strings.HasSuffix(rest, ext) {
				mid := strings.TrimSuffix(rest, ext)
				return mid == "" || (strings.HasPrefix(mid, "-") && len(mid) > 1)
			}
		}
	}
	return false
}

// placeholders returns the names in ${NAME} and ${NAME:default}, nested
// defaults included. Default values are dropped.
func placeholders(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			return out
		}
		s = s[i+2:]
		end := len(s)
		for j := 0; j < len(s); j++ {
			if s[j] == ':' || s[j] == '}' {
				end = j
				break
			}
		}
		out = append(out, s[:end])
		s = s[end:]
	}
}

// envShaped is the convention for environment variable names: upper case,
// digits and underscores. A dotted or lower-case placeholder is a Spring
// property, which may come from anywhere; only an explicit
// environment-shaped name is recorded.
func envShaped(n string) bool {
	if n == "" || !(n[0] >= 'A' && n[0] <= 'Z' || n[0] == '_') {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (e *emitter) env(n, origin string, evd fact.Evidence) {
	e.add(fact.KindEnvVar, "env_var:"+n, map[string]any{"name": n, "origins": []any{origin}}, evd)
}

func (e *emitter) host(h, usage string, evd fact.Evidence) {
	e.add(fact.KindOutboundHost, "outbound_host:"+h, map[string]any{"host": h, "via": "literal_url", "usage": usage}, evd)
}

// configuredURL reports a property key that holds a URL the app calls:
// its last segment ends in url, uri, endpoint or host.
func configuredURL(key string) bool {
	k := strings.ToLower(key)
	if i := strings.LastIndexAny(k, "."); i >= 0 {
		k = k[i+1:]
	}
	k = strings.NewReplacer("-", "", "_", "").Replace(k)
	for _, s := range []string{"url", "uri", "endpoint", "host", "baseurl"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

func springValue(p *project, f, mod, key, val string, line int, e *emitter) {
	for _, n := range placeholders(val) {
		if envShaped(n) {
			e.env(n, "config", ev(f, line, mod))
		}
	}
	if strings.HasPrefix(val, "jdbc:postgresql:") {
		p.engine[mod] = "postgres"
	} else if strings.HasPrefix(val, "jdbc:mysql:") || strings.HasPrefix(val, "jdbc:mariadb:") {
		if p.engine[mod] == "" {
			p.engine[mod] = "mysql"
		}
	}
	if strings.Contains(val, "${") {
		return
	}
	if h := hostname.FromURL(val); h != "" {
		usage := "mention"
		if configuredURL(key) {
			usage = "default"
		}
		e.host(h, usage, ev(f, line, mod))
	}
}

func springConfig(p *project, f string, e *emitter) {
	mod := p.module(f)
	data := p.files[f]
	if strings.HasSuffix(f, ".properties") {
		properties(data, func(key, val string, line int) { springValue(p, f, mod, key, val, line, e) })
		return
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if err.Error() != "EOF" {
				e.fail(f, 0, "yaml: "+err.Error())
			}
			return
		}
		walkYAML(&doc, "", func(key, val string, line int) { springValue(p, f, mod, key, val, line, e) })
	}
}

// walkYAML calls fn for every scalar with its dotted key.
func walkYAML(n *yaml.Node, key string, fn func(key, val string, line int)) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			walkYAML(c, key, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if key != "" {
				k = key + "." + k
			}
			walkYAML(n.Content[i+1], k, fn)
		}
	case yaml.ScalarNode:
		fn(key, n.Value, n.Line)
	}
}

// properties reads a .properties file: key=value, key: value or key value,
// # and ! comments, \ continuations.
func properties(data []byte, fn func(key, val string, line int)) {
	lines := strings.Split(string(data), "\n")
	for n := 0; n < len(lines); n++ {
		start := n + 1
		l := strings.TrimLeft(lines[n], " \t\f")
		if l == "" || l[0] == '#' || l[0] == '!' {
			continue
		}
		for strings.HasSuffix(strings.TrimRight(l, "\r"), `\`) && n+1 < len(lines) {
			l = strings.TrimSuffix(strings.TrimRight(l, "\r"), `\`) + strings.TrimLeft(lines[n+1], " \t\f")
			n++
		}
		l = strings.TrimRight(l, "\r")
		i := strings.IndexAny(l, "=: \t")
		if i < 0 {
			fn(l, "", start)
			continue
		}
		key := l[:i]
		val := strings.TrimLeft(l[i:], " \t")
		if val != "" && (val[0] == '=' || val[0] == ':') {
			val = strings.TrimLeft(val[1:], " \t")
		}
		fn(key, val, start)
	}
}
