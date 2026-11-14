// Package dockerfile reads Dockerfiles with BuildKit's own parser (the one
// Docker uses) and reports, per buildable stage, the base images, what is
// copied in from the build context, and what gets built.
package dockerfile

import (
	"bytes"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/cernly/cernly/internal/redact"
)

func parse(data []byte) (*parser.Result, error) {
	return parser.Parse(bytes.NewReader(data))
}

// args returns an instruction's arguments as the parser split them.
func args(n *parser.Node) []string {
	var out []string
	for a := n.Next; a != nil; a = a.Next {
		out = append(out, a.Value)
	}
	return out
}

// flag returns the value of --name=value among an instruction's flags.
func flag(n *parser.Node, name string) (string, bool) {
	for _, f := range n.Flags {
		if v, ok := strings.CutPrefix(f, "--"+name+"="); ok {
			return v, true
		}
	}
	return "", false
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:?-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// substitute replaces build arguments with their declared defaults, as
// docker build does when no --build-arg is given. Unknown variables stay as
// written.
func substitute(s string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRe.FindStringSubmatch(m)
		name := sub[1]
		if name == "" {
			name = sub[4]
		}
		if v, ok := vars[name]; ok && v != "" {
			return v
		}
		if sub[2] != "" {
			return sub[3]
		}
		return m
	})
}

// stage is one FROM block.
type stage struct {
	index   int
	name    string
	base    string // as written, ARG defaults applied
	rawBase string
	line    int
	vars    map[string]string
	workdir string
	copies  []copyOp
	builds  []build
	remote  []remoteSrc
}

// copyOp maps container paths in this stage to where they came from: the
// build context (fromStage == "") or another stage or image.
type copyOp struct {
	line, endLine int
	fromStage     string // stage name/index or image ref; "" = build context
	srcs          []string
	dest          string // absolute container path
	destIsDir     bool
}

type build struct {
	line, endLine int
	kind          string // "go" or "npm"
	pkg           string // container path for go, workspace for npm
	output        string // container path of the built artifact, "" when unknown
	raw           string
}

type remoteSrc struct {
	line int
	url  string
}

func isRemote(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "git@") ||
		strings.HasSuffix(s, ".git") && strings.Contains(s, "://")
}

func absIn(workdir, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(workdir, p)
}

// readStages walks the instructions of a parsed Dockerfile.
func readStages(res *parser.Result) []*stage {
	globals := map[string]string{}
	var stages []*stage
	var cur *stage
	for _, n := range res.AST.Children {
		// The parser keeps the instruction as written ("FROM", "from").
		instr := strings.ToLower(n.Value)
		switch instr {
		case "arg":
			for _, a := range args(n) {
				name, val, _ := strings.Cut(a, "=")
				val = strings.Trim(val, `"'`)
				if cur == nil {
					globals[name] = val
				} else if val != "" || cur.vars[name] == "" {
					if val == "" {
						val = globals[name]
					}
					cur.vars[name] = val
				}
			}
		case "from":
			a := args(n)
			if len(a) == 0 {
				continue
			}
			cur = &stage{index: len(stages), rawBase: a[0], line: n.StartLine, vars: map[string]string{}, workdir: "/"}
			cur.base = substitute(a[0], globals)
			if len(a) >= 3 && strings.EqualFold(a[1], "as") {
				cur.name = strings.ToLower(a[2])
			}
			stages = append(stages, cur)
		case "workdir":
			if cur != nil {
				if a := args(n); len(a) > 0 {
					cur.workdir = absIn(cur.workdir, substitute(a[0], cur.allVars(globals)))
				}
			}
		case "copy", "add":
			if cur == nil {
				continue
			}
			a := args(n)
			if len(n.Heredocs) > 0 || len(a) < 2 {
				continue // inline file content, not from the context
			}
			vars := cur.allVars(globals)
			op := copyOp{line: n.StartLine, endLine: n.EndLine}
			if from, ok := flag(n, "from"); ok {
				op.fromStage = strings.ToLower(substitute(from, vars))
			}
			dest := substitute(a[len(a)-1], vars)
			op.dest = absIn(cur.workdir, dest)
			op.destIsDir = strings.HasSuffix(dest, "/") || len(a) > 2
			for _, s := range a[:len(a)-1] {
				s = substitute(s, vars)
				if instr == "add" && isRemote(s) {
					cur.remote = append(cur.remote, remoteSrc{line: n.StartLine, url: redact.Userinfo(s)})
					continue
				}
				op.srcs = append(op.srcs, s)
			}
			if len(op.srcs) > 0 {
				cur.copies = append(cur.copies, op)
			}
		case "run":
			if cur == nil {
				continue
			}
			var scripts []string
			if len(n.Heredocs) > 0 {
				for _, h := range n.Heredocs {
					scripts = append(scripts, h.Content)
				}
			} else if n.Attributes["json"] {
				scripts = append(scripts, shellJoin(args(n)))
			} else {
				scripts = append(scripts, strings.Join(args(n), " "))
			}
			for _, sc := range scripts {
				for _, b := range findBuilds(substitute(sc, cur.allVars(globals))) {
					b.line, b.endLine = n.StartLine, n.EndLine
					if b.kind == "go" {
						b.pkg = absIn(cur.workdir, b.pkg)
						if b.output != "" {
							b.output = absIn(cur.workdir, b.output)
						} else {
							b.output = path.Join(cur.workdir, path.Base(b.pkg))
						}
					}
					cur.builds = append(cur.builds, b)
				}
			}
		}
	}
	return stages
}

func (s *stage) allVars(globals map[string]string) map[string]string {
	out := make(map[string]string, len(globals)+len(s.vars))
	for k, v := range globals {
		out[k] = v
	}
	for k, v := range s.vars {
		out[k] = v
	}
	return out
}

func shellJoin(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strconv.Quote(x)
	}
	return strings.Join(out, " ")
}

// contextPath maps a container path in a stage back to the build context,
// through the COPY that put it there. ok is false when the path did not come
// from the context (another stage, generated during the build).
func (s *stage) contextPath(p string) (string, bool) {
	best, bestLen := "", -1
	for _, c := range s.copies {
		if c.fromStage != "" {
			continue
		}
		for _, src := range c.srcs {
			dest := c.dest
			if c.destIsDir && !strings.HasSuffix(src, "/") && path.Ext(src) != "" {
				dest = path.Join(c.dest, path.Base(src))
			}
			if p == dest || strings.HasPrefix(p, dest+"/") || dest == "/" {
				if len(dest) > bestLen {
					rel := strings.TrimPrefix(strings.TrimPrefix(p, dest), "/")
					best, bestLen = path.Join(strings.TrimSuffix(src, "/"), rel), len(dest)
				}
			}
		}
	}
	return path.Clean(best), bestLen >= 0
}
