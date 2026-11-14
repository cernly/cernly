package main

import (
	"bytes"
	"encoding/xml"
	"path"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/version"
)

// pom is one pom.xml as written; values stay unresolved until resolve.
type pom struct {
	file                     string
	dir                      string
	group, artifact, version string
	parent                   *coord
	parentPath               string // relativePath, default ../pom.xml
	props                    map[string]string
	propLine                 map[string]int
	deps                     []mvnDep
	managed                  []mvnDep
}

type coord struct {
	group, artifact, version string
	line                     int
	end                      int // the line of </dependency>; 0 when not a dependency
}

type mvnDep struct {
	coord
	scope    string
	optional bool
}

// parsePom reads a pom with encoding/xml; lines come from the decoder.
func parsePom(file string, data []byte) (*pom, error) {
	p := &pom{file: file, dir: path.Dir(file), props: map[string]string{}, propLine: map[string]int{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var stack []string
	var cur *mvnDep
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return p, nil
			}
			return p, err
		}
		line, _ := dec.InputPos()
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
			joined := strings.Join(stack, "/")
			switch joined {
			case "project/dependencies/dependency", "project/dependencyManagement/dependencies/dependency",
				"project/profiles/profile/dependencies/dependency":
				cur = &mvnDep{coord: coord{line: line}}
			case "project/parent":
				p.parent = &coord{line: line}
				p.parentPath = "../pom.xml"
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			joined := strings.Join(stack, "/")
			v := strings.TrimSpace(text.String())
			switch {
			case cur != nil && strings.HasSuffix(joined, "dependency/groupId"):
				cur.group = v
			case cur != nil && strings.HasSuffix(joined, "dependency/artifactId"):
				cur.artifact = v
			case cur != nil && strings.HasSuffix(joined, "dependency/version"):
				cur.version = v
			case cur != nil && strings.HasSuffix(joined, "dependency/scope"):
				cur.scope = v
			case cur != nil && strings.HasSuffix(joined, "dependency/optional"):
				cur.optional = v == "true"
			case cur != nil && strings.HasSuffix(joined, "/dependency"):
				cur.end = line
				if strings.Contains(joined, "dependencyManagement") {
					p.managed = append(p.managed, *cur)
				} else {
					p.deps = append(p.deps, *cur)
				}
				cur = nil
			case joined == "project/groupId":
				p.group = v
			case joined == "project/artifactId":
				p.artifact = v
			case joined == "project/version":
				p.version = v
			case joined == "project/parent/groupId":
				p.parent.group = v
			case joined == "project/parent/artifactId":
				p.parent.artifact = v
			case joined == "project/parent/version":
				p.parent.version = v
			case joined == "project/parent/relativePath":
				p.parentPath = v
			case len(stack) == 3 && stack[1] == "properties":
				p.props[stack[2]] = v
				p.propLine[stack[2]] = line
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			text.Reset()
		}
	}
}

func maven(p *project, paths []string, e *emitter) {
	poms := map[string]*pom{} // by file
	byCoord := map[string]*pom{}
	for _, f := range paths {
		if path.Base(f) != "pom.xml" {
			continue
		}
		pm, err := parsePom(f, p.files[f])
		if err != nil {
			e.fail(f, 0, "pom.xml: "+err.Error())
			continue
		}
		poms[f] = pm
	}
	// The parent in the repo: relativePath (a folder means its pom.xml).
	parentOf := func(pm *pom) *pom {
		if pm.parent == nil || pm.parentPath == "" {
			return nil
		}
		rel := path.Join(pm.dir, pm.parentPath)
		if !strings.HasSuffix(rel, ".xml") {
			rel = path.Join(rel, "pom.xml")
		}
		if pp, ok := poms[rel]; ok && pp.artifact == pm.parent.artifact {
			return pp
		}
		return nil
	}
	for _, pm := range poms {
		g := pm.group
		if g == "" && pm.parent != nil {
			g = pm.parent.group
		}
		byCoord[g+":"+pm.artifact] = pm
	}
	for _, f := range paths {
		pm, ok := poms[f]
		if !ok {
			continue
		}
		chain := []*pom{pm}
		for pp := parentOf(pm); pp != nil && len(chain) < 20; pp = parentOf(pp) {
			chain = append(chain, pp)
		}
		res := resolver{chain: chain}
		mod := pm.dir
		for _, d := range pm.deps {
			if d.scope == "import" {
				continue
			}
			group, _, _ := res.resolve(d.group)
			artifact, _, _ := res.resolve(d.artifact)
			if group == "" || artifact == "" || strings.Contains(group+artifact, "${") {
				e.fail(f, d.line, "dependency with a computed group or artifact: "+d.group+":"+d.artifact)
				continue
			}
			p.noteDriver(mod, group, artifact)
			p.noteTest(mod, group, artifact, evSpan(f, d.line, d.end, mod))
			evs := []fact.Evidence{evSpan(f, d.line, d.end, mod)}
			raw := d.version
			ext := map[string]any{}
			if raw == "" {
				if m, mf, ok := res.managed(group, artifact); ok {
					raw = m.version
					evs = append(evs, evSpan(mf, m.line, m.end, mod))
				}
			}
			ver, used, ok := res.resolve(raw)
			for _, u := range used {
				evs = append(evs, ev(u.file, u.line, mod))
			}
			attrs := map[string]any{"ecosystem": "maven", "name": group + ":" + artifact, "direct": true, "scope": mvnScope(d.scope)}
			switch {
			case raw == "":
				ver = ""
				if top := chain[len(chain)-1]; top.parent != nil {
					ext["managed_by"] = top.parent.group + ":" + top.parent.artifact + ":" + top.parent.version
				} else {
					ext["managed_by"] = "outside the repo"
				}
			case !ok:
				ver = raw
				ext["unresolved"] = "a property not defined in the repo's poms"
			}
			attrs["version"] = ver
			if ok && ver != "" {
				attrs["version_info"] = version.Parse(ver).Attrs()
			} else {
				attrs["version_info"] = map[string]any{"raw": ver, "scheme": "unknown"}
			}
			if in, ok := byCoord[group+":"+artifact]; ok {
				attrs["internal"] = map[string]any{"name": group + ":" + artifact, "path": in.dir, "spec": "module"}
			}
			if d.optional {
				ext["optional"] = true
			}
			if len(ext) > 0 {
				attrs["ext"] = ext
			}
			e.add(fact.KindDependency, "dependency:maven:"+mod+":"+group+":"+artifact, attrs, evs...)
		}
	}
}

func mvnScope(s string) string {
	switch s {
	case "test":
		return "test"
	case "", "compile", "runtime", "provided", "system":
		return "prod"
	}
	return "unknown"
}

type resolver struct{ chain []*pom }

type use struct {
	file string
	line int
}

// resolve replaces ${prop} from the poms' properties and project
// coordinates. ok=false when a property is not defined in the repo.
func (r resolver) resolve(s string) (string, []use, bool) {
	var used []use
	ok := true
	for depth := 0; strings.Contains(s, "${") && depth < 10; depth++ {
		i := strings.Index(s, "${")
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return s, used, false
		}
		key := s[i+2 : i+j]
		v, u, found := r.lookup(key)
		if !found {
			return s, used, false
		}
		if u.file != "" {
			used = append(used, u)
		}
		s = s[:i] + v + s[i+j+1:]
	}
	if strings.Contains(s, "${") {
		ok = false
	}
	return s, used, ok
}

func (r resolver) lookup(key string) (string, use, bool) {
	me := r.chain[0]
	switch key {
	case "project.version", "pom.version", "version":
		if me.version != "" {
			return me.version, use{}, true
		}
		if me.parent != nil && me.parent.version != "" {
			return me.parent.version, use{}, true
		}
	case "project.groupId", "pom.groupId":
		if me.group != "" {
			return me.group, use{}, true
		}
		if me.parent != nil {
			return me.parent.group, use{}, true
		}
	case "project.artifactId":
		return me.artifact, use{}, true
	case "project.parent.version":
		if me.parent != nil {
			return me.parent.version, use{}, true
		}
	}
	for _, pm := range r.chain {
		if v, ok := pm.props[key]; ok {
			return v, use{pm.file, pm.propLine[key]}, true
		}
	}
	return "", use{}, false
}

// managed finds a dependencyManagement entry in the chain.
func (r resolver) managed(group, artifact string) (mvnDep, string, bool) {
	for _, pm := range r.chain {
		for _, m := range pm.managed {
			g, _, _ := r.resolve(m.group)
			a, _, _ := r.resolve(m.artifact)
			if g == group && a == artifact && m.version != "" {
				return m, pm.file, true
			}
		}
	}
	return mvnDep{}, "", false
}
