package explain

import (
	"path"
	"regexp"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// unnamed is the name compose gives a stack without a project name: default,
// default-2, ... It says nothing to a reader.
var unnamed = regexp.MustCompile(`^default(-[0-9]+)?$`)

// StackLabels gives each stack of a scan a label a reader can place, by the
// stack's name. Display only: the stack's name stays in fact keys, stored
// scans and drift; only the words on screen and in the report
// change. In order:
//  1. the name declared in .cernly.yml (stacks.<name>.name);
//  2. the stack's own name, unless compose made it up (default, default-2);
//  3. the folder of its first compose file (docker/development);
//  4. the workflow that starts it (started by deploy.yml);
//  5. its first compose file (docker-compose.yml).
//
// Two stacks that would read the same get their compose file instead, and
// still the same, their name added. Deterministic: scan and map only.
func StackLabels(s *fact.Scan, m *rules.Map) map[string]string {
	declared, workflow := map[string]string{}, map[string]string{}
	for _, st := range m.Stacks {
		name := stackName(st.Stack)
		if st.Name != "" {
			declared[name] = st.Name
		}
		for _, r := range st.Reasons {
			if r.Workflow != "" && workflow[name] == "" {
				workflow[name] = "started by " + path.Base(r.Workflow)
			}
		}
	}
	out, file := map[string]string{}, map[string]string{}
	var order []string
	for _, f := range s.Facts {
		if f.Kind != fact.KindStack {
			continue
		}
		name, _ := f.Attrs["name"].(string)
		if name == "" {
			continue
		}
		first := ""
		if fs, ok := f.Attrs["files"].([]any); ok && len(fs) > 0 {
			first, _ = fs[0].(string)
		} else if fs, ok := f.Attrs["files"].([]string); ok && len(fs) > 0 {
			first = fs[0]
		}
		file[name] = first
		order = append(order, name)
		switch dir := path.Dir(first); {
		case declared[name] != "":
			out[name] = declared[name]
		case !unnamed.MatchString(name):
			out[name] = name
		case first != "" && dir != ".":
			out[name] = dir
		case workflow[name] != "":
			out[name] = workflow[name]
		case first != "":
			out[name] = first
		default:
			out[name] = name
		}
	}
	seen := map[string]int{}
	for _, n := range order {
		seen[out[n]]++
	}
	for _, n := range order {
		if seen[out[n]] > 1 && declared[n] == "" && unnamed.MatchString(n) && file[n] != "" {
			out[n] = file[n]
		}
	}
	seen = map[string]int{}
	for _, n := range order {
		seen[out[n]]++
	}
	for _, n := range order {
		if seen[out[n]] > 1 && out[n] != n {
			out[n] += " (" + n + ")"
		}
	}
	return out
}

func stackName(key string) string {
	if p := strings.SplitN(key, ":", 3); len(p) == 3 {
		return p[2]
	}
	return key
}

// SharedNames are the stack names that stacks of more than one tool use
// (a helm and a kubernetes stack both called chart): only the tool tells
// them apart.
func SharedNames(s *fact.Scan) map[string]bool {
	tools := map[string]map[string]bool{}
	for _, f := range s.Facts {
		if f.Kind != fact.KindStack {
			continue
		}
		name, _ := f.Attrs["name"].(string)
		tool, _ := f.Attrs["tool"].(string)
		if tools[name] == nil {
			tools[name] = map[string]bool{}
		}
		tools[name][tool] = true
	}
	out := map[string]bool{}
	for n, ts := range tools {
		if len(ts) > 1 {
			out[n] = true
		}
	}
	return out
}

// KeyLabel is the label of the stack stack:<tool>:<name>, with the tool
// added when another tool's stack has the same name: "chart (helm)".
func KeyLabel(labels map[string]string, shared map[string]bool, key string) string {
	p := strings.SplitN(key, ":", 3)
	if len(p) != 3 {
		return key
	}
	l := labels[p[2]]
	if l == "" {
		l = p[2]
	}
	if shared[p[2]] {
		l += " (" + p[1] + ")"
	}
	return l
}
