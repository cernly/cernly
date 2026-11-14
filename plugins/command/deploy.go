package command

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
)

// deployCmd is a kubectl, kustomize or helm call that applies definition
// files: the invocation half of a kubernetes or helm stack. The kubernetes
// plugin reads the files.
type deployCmd struct {
	tool    string   // kubernetes or helm
	paths   []string // in order: -f files or folders, -k folders; helm: the chart, then -f values files
	kustom  []bool   // per path: given with -k (a kustomization folder)
	literal bool
	starts  bool // apply, create, replace; helm upgrade and install
	release string
	sub     string
}

var kubectlValued = set("-f", "--filename", "-k", "--kustomize", "-n", "--namespace", "--context", "--kubeconfig",
	"-l", "--selector", "-o", "--output", "--field-manager", "--server", "-s", "--user", "--cluster", "--token",
	"--prune-allowlist", "--timeout", "--wait-for")

// dryRun reports --dry-run (kubectl's --dry-run=client|server, helm's
// --dry-run): the command validates, it does not deploy. --dry-run=none
// still deploys.
func dryRun(args []arg) bool {
	for _, a := range args {
		if a.text == "--dry-run" || strings.HasPrefix(a.text, "--dry-run=") && a.text != "--dry-run=none" && a.text != "--dry-run=false" {
			return true
		}
	}
	return false
}

func parseKubectl(args []arg) *deployCmd {
	d := &deployCmd{tool: "kubernetes", literal: true}
	for i := 0; i < len(args); {
		name, val, lit, used := flagValue(args, i, kubectlValued)
		if used == 0 {
			if d.sub == "" {
				d.sub = args[i].text
			}
			i++
			continue
		}
		switch name {
		case "-f", "--filename", "-k", "--kustomize":
			d.paths = append(d.paths, val)
			d.kustom = append(d.kustom, name == "-k" || name == "--kustomize")
			d.literal = d.literal && lit
		}
		i += used
	}
	switch d.sub {
	case "apply", "create", "replace":
		d.starts = !dryRun(args)
	case "diff", "delete":
	default:
		return nil
	}
	if len(d.paths) == 0 {
		return nil
	}
	return d
}

// parseKustomize reads `kustomize build <dir>`; the output usually goes to
// kubectl apply -f -, which this reader cannot follow, so it does not start.
func parseKustomize(args []arg) *deployCmd {
	if len(args) < 2 || args[0].text != "build" {
		return nil
	}
	for _, a := range args[1:] {
		if !strings.HasPrefix(a.text, "-") {
			return &deployCmd{tool: "kubernetes", sub: "build", paths: []string{a.text}, kustom: []bool{true}, literal: a.literal}
		}
	}
	return &deployCmd{tool: "kubernetes", sub: "build", paths: []string{"."}, kustom: []bool{true}, literal: true}
}

var helmValued = set("-f", "--values", "-n", "--namespace", "--version", "--set", "--set-string", "--set-file",
	"--set-json", "--kube-context", "--kubeconfig", "--timeout", "--repo", "--post-renderer", "-o", "--output",
	"--description", "--history-max", "--username", "--password", "--ca-file", "--cert-file", "--key-file")

func parseHelm(args []arg) *deployCmd {
	if len(args) == 0 {
		return nil
	}
	d := &deployCmd{tool: "helm", sub: args[0].text, literal: true}
	switch d.sub {
	case "upgrade", "install":
		d.starts = !dryRun(args)
	case "template":
	default:
		return nil
	}
	var pos []arg
	var values []string
	for i := 1; i < len(args); {
		name, val, lit, used := flagValue(args, i, helmValued)
		if used == 0 {
			pos = append(pos, args[i])
			i++
			continue
		}
		if name == "-f" || name == "--values" {
			values = append(values, val)
			d.literal = d.literal && lit
		}
		i += used
	}
	// helm upgrade|install RELEASE CHART; helm install --generate-name CHART
	switch len(pos) {
	case 0:
		return nil
	case 1:
		d.paths = []string{pos[0].text}
		d.literal = d.literal && pos[0].literal
	default:
		d.release = pos[0].text
		d.paths = []string{pos[1].text}
		d.literal = d.literal && pos[1].literal
	}
	d.paths = append(d.paths, values...)
	d.kustom = make([]bool, len(d.paths))
	return d
}

// deployName names a kubernetes or helm stack: the workflow environment
// when there is exactly one, else the last path's name (overlays/prod ->
// prod, values-prod.yaml -> prod, k8s/app.yaml -> app), else the release.
func deployName(d *deployCmd, files []string) string {
	if d.tool == "helm" && len(files) == 1 && d.release != "" {
		return d.release
	}
	last := strings.TrimSuffix(files[len(files)-1], "/")
	b := path.Base(last)
	b = strings.TrimSuffix(strings.TrimSuffix(b, ".yaml"), ".yml")
	for _, pre := range []string{"values-", "values.", "values_"} {
		b = strings.TrimPrefix(b, pre)
	}
	if b == "" || b == "." || b == "values" {
		if d.release != "" {
			return d.release
		}
		return "default"
	}
	return b
}

func (st *state) deployFacts() []fact.Fact {
	type agg struct {
		d          *deployCmd
		files      []string
		unresolved []string
		starts     bool
		wfs        map[string]any
		wfStarts   map[string]bool
		ev         []fact.Evidence
		envNames   map[string]bool
	}
	aggs := map[string]*agg{}
	for _, f := range st.found {
		d := f.deploy
		if d == nil {
			continue
		}
		dir := path.Dir(f.snip.path)
		var files, unresolved []string
		for _, raw := range d.paths {
			p, ok := st.resolveAt(f.cwd, raw, dir, false)
			if !ok {
				p, ok = st.resolveAt(f.cwd, raw, dir, true)
			}
			if !ok || !d.literal || raw == "-" || strings.Contains(raw, "://") {
				unresolved = append(unresolved, p)
			}
			files = append(files, p)
		}
		id := d.tool + "\x00" + strings.Join(files, "\x00")
		a := aggs[id]
		if a == nil {
			a = &agg{d: d, files: files, unresolved: unresolved, wfs: map[string]any{}, wfStarts: map[string]bool{}, envNames: map[string]bool{}}
			aggs[id] = a
		}
		a.starts = a.starts || d.starts
		chain, wfs := st.reachOf(f.snip, true)
		a.ev = append(a.ev, st.evidence(f, chain...)...)
		for _, w := range wfs {
			m := w.(map[string]any)
			wid := m["path"].(string) + "#" + m["job"].(string)
			if _, seen := a.wfs[wid]; !seen || d.starts && !a.wfStarts[wid] {
				a.wfs[wid] = w
				a.wfStarts[wid] = d.starts
			}
			if e, ok := m["environment"].(string); ok {
				a.envNames[e] = true
			}
		}
	}
	ids := make([]string, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	used := map[string]int{}
	var out []fact.Fact
	for _, id := range ids {
		a := aggs[id]
		name := deployName(a.d, a.files)
		if envs := fact.SortedSet(a.envNames); len(envs) == 1 {
			name = envs[0].(string)
		}
		used[a.d.tool+"\x00"+name]++
		if n := used[a.d.tool+"\x00"+name]; n > 1 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		attrs := map[string]any{
			"name": name, "tool": a.d.tool, "source": "invocation", "files": fact.Strings(a.files), "default_files": false,
			"starts": a.starts, "ci_invoked": len(a.wfs) > 0,
		}
		ext := map[string]any{}
		var kustom []any
		for i, k := range a.d.kustom {
			if k {
				kustom = append(kustom, a.files[i])
			}
		}
		if len(kustom) > 0 {
			ext["kustomize"] = kustom
		}
		if a.d.release != "" {
			ext["release"] = a.d.release
		}
		if len(ext) > 0 {
			attrs["ext"] = ext
		}
		if len(a.unresolved) > 0 {
			attrs["unresolved"] = fact.Strings(a.unresolved)
		}
		if len(a.wfs) > 0 {
			attrs["workflows"] = fact.SortedValues(withStarts(a.wfs, a.wfStarts))
		}
		out = append(out, fact.Fact{Kind: fact.KindStack, Key: "stack:" + a.d.tool + ":" + name, Attrs: attrs, Plugin: "commands", Evidence: a.ev})
	}
	return out
}
