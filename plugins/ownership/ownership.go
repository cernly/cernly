// Package ownership reports who owns a repo, as the repo itself says it:
// the owners a CODEOWNERS file names, and the entities a Backstage
// catalog-info.yaml describes (name, kind, type, owner, system, lifecycle).
// Every fact points at the lines that say it. Nothing is guessed: an owner
// nobody wrote down is not reported. See https://cernly.dev/how/ownership/.
package ownership

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/yamlnode"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

const name = "ownership"

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return name }
func (Plugin) Version() string { return "0.1.0" }

// codeownersFiles are the places GitHub and GitLab read, in the order they
// look: the first one tracked is the one that applies, the others are not read
// by the forge and not here.
var codeownersFiles = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

func isCatalog(p string) bool {
	b := path.Base(p)
	return b == "catalog-info.yaml" || b == "catalog-info.yml"
}

func (Plugin) Watches(p string) bool {
	for _, f := range codeownersFiles {
		if p == f {
			return true
		}
	}
	return isCatalog(p)
}

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	for _, f := range codeownersFiles {
		if !r.Has(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			break
		}
		res.Facts = append(res.Facts, Codeowners(f, data)...)
		break
	}
	seen := map[string]string{} // entity key -> the file that declared it first
	for _, f := range r.Files {
		if !isCatalog(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		facts, errs := Catalog(f, data)
		res.Errors = append(res.Errors, errs...)
		for _, x := range facts {
			if first, dup := seen[x.Key]; dup {
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Line: x.Evidence[0].Line,
					Message: fmt.Sprintf("%s is also declared in %s; this one is ignored", strings.TrimPrefix(x.Key, "catalog_entity:"), first)})
				continue
			}
			seen[x.Key] = f
			res.Facts = append(res.Facts, x)
		}
	}
	return res
}

type owned struct {
	owner    string
	patterns []string
	lines    []int
	all      bool
}

// Codeowners reads one CODEOWNERS file: one owner fact per owner it names,
// with the patterns they own in file order and every line that names them.
// GitLab sections ([Name] @default-owners) give their owners to entries
// without their own. Owners are compared without case, as the forges do.
func Codeowners(file string, data []byte) []fact.Fact {
	byOwner := map[string]*owned{}
	var order []string
	var sectionOwners []string
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "^[") {
			sectionOwners = nil
			if end := strings.Index(line, "]"); end >= 0 {
				rest := strings.TrimSpace(line[end+1:])
				// [Section][2] @owners: an approval count before the owners.
				if strings.HasPrefix(rest, "[") {
					if e := strings.Index(rest, "]"); e >= 0 {
						rest = rest[e+1:]
					}
				}
				sectionOwners = ownersIn(strings.Fields(rest))
			}
			continue
		}
		fields := splitPattern(line)
		if len(fields) == 0 {
			continue
		}
		pattern, owners := fields[0], ownersIn(fields[1:])
		if len(owners) == 0 && len(fields) == 1 {
			owners = sectionOwners
		}
		for _, o := range owners {
			k := strings.ToLower(o)
			x := byOwner[k]
			if x == nil {
				x = &owned{owner: k}
				byOwner[k] = x
				order = append(order, k)
			}
			if !slices.Contains(x.patterns, pattern) {
				x.patterns = append(x.patterns, pattern)
			}
			x.lines = append(x.lines, i+1)
			if pattern == "*" || pattern == "/" || pattern == "**" || pattern == "/**" || pattern == "/*" {
				x.all = true
			}
		}
	}
	sort.Strings(order)
	var out []fact.Fact
	for _, k := range order {
		x := byOwner[k]
		f := fact.Fact{Kind: fact.KindOwner, Key: "owner:" + k, Plugin: name, Attrs: map[string]any{
			"owner": x.owner, "type": ownerType(x.owner), "source": "codeowners", "file": file,
			"patterns": fact.Strings(x.patterns), "default": x.all,
		}}
		for _, l := range x.lines {
			f.Evidence = append(f.Evidence, fact.Evidence{Path: file, Line: l})
		}
		out = append(out, f)
	}
	return out
}

// stripComment drops a # comment; a \# is a literal hash in a pattern.
func stripComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '#' {
			return s[:i]
		}
	}
	return s
}

// splitPattern splits on whitespace, keeping "\ " inside the pattern.
func splitPattern(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if c == ' ' || c == '\t' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ownersIn keeps the fields that are owners: @user, @org/team or an email.
func ownersIn(fields []string) []string {
	var out []string
	for _, f := range fields {
		if (strings.HasPrefix(f, "@") && len(f) > 1) || (strings.Contains(f, "@") && strings.Contains(f, ".") && !strings.HasPrefix(f, "@")) {
			out = append(out, f)
		}
	}
	return out
}

func ownerType(o string) string {
	switch {
	case !strings.HasPrefix(o, "@"):
		return "email"
	case strings.Contains(o, "/"):
		return "team"
	}
	return "user"
}

// catalogFields are the fields read from an entity, by where they sit.
var catalogFields = []struct{ attr, section, key string }{
	{"title", "metadata", "title"},
	{"type", "spec", "type"},
	{"owner", "spec", "owner"},
	{"system", "spec", "system"},
	{"domain", "spec", "domain"},
	{"lifecycle", "spec", "lifecycle"},
}

// Catalog reads a Backstage catalog file: one catalog_entity fact per
// backstage.io entity, keyed by its entity reference (kind:namespace/name).
// Location entities only point at other files and are left out; so are
// scaffolder skeletons whose name is still a ${{ }} placeholder.
func Catalog(file string, data []byte) ([]fact.Fact, []fact.LocationError) {
	var out []fact.Fact
	var errs []fact.LocationError
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errs = append(errs, fact.LocationError{Path: file, Message: fmt.Sprintf("invalid YAML: %v", err)})
			break
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		root := doc.Content[0]
		api, _ := scalar(root, "apiVersion")
		kind, kindLine := scalar(root, "kind")
		if !strings.HasPrefix(api, "backstage.io/") || kind == "" || strings.EqualFold(kind, "Location") {
			continue
		}
		meta := yamlnode.Get(root, "metadata")
		entity, nameLine := scalar(meta, "name")
		if entity == "" || strings.Contains(entity, "${{") {
			continue
		}
		ns, _ := scalar(meta, "namespace")
		if ns == "" {
			ns = "default"
		}
		ref := strings.ToLower(kind) + ":" + strings.ToLower(ns) + "/" + strings.ToLower(entity)
		attrs := map[string]any{"kind": strings.ToLower(kind), "name": entity, "namespace": ns, "file": file}
		lines := []int{kindLine, nameLine}
		for _, cf := range catalogFields {
			v, l := scalar(yamlnode.Get(root, cf.section), cf.key)
			if v == "" || strings.Contains(v, "${{") {
				continue
			}
			attrs[cf.attr] = v
			lines = append(lines, l)
		}
		if tags := yamlnode.Get(meta, "tags"); tags != nil && tags.Kind == yaml.SequenceNode {
			var ts []any
			for _, t := range tags.Content {
				if t.Kind == yaml.ScalarNode && t.Value != "" {
					ts = append(ts, t.Value)
				}
			}
			if len(ts) > 0 {
				attrs["tags"] = ts
				lines = append(lines, tags.Line)
			}
		}
		f := fact.Fact{Kind: fact.KindCatalogEntity, Key: "catalog_entity:" + ref, Plugin: name, Attrs: attrs}
		sort.Ints(lines)
		for i, l := range lines {
			if l > 0 && (i == 0 || l != lines[i-1]) {
				f.Evidence = append(f.Evidence, fact.Evidence{Path: file, Line: l})
			}
		}
		out = append(out, f)
	}
	return out, errs
}

// scalar returns a string value and the line of its key.
func scalar(n *yaml.Node, key string) (string, int) {
	if n == nil || n.Kind != yaml.MappingNode {
		return "", 0
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key && n.Content[i+1].Kind == yaml.ScalarNode {
			return strings.TrimSpace(n.Content[i+1].Value), n.Content[i].Line
		}
	}
	return "", 0
}
