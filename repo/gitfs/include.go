package gitfs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	maxIncludeDepth = 10        // git's limit on nested includes
	maxConfigBytes  = 16 << 20  // one config file
	maxConfigTotal  = 64 << 20  // all config files of one repository
	maxConfigFiles  = 256       // config files read for one repository
	maxGlobSteps    = 1_000_000 // matching work for one onbranch pattern
)

// loadConfig reads .git/config and, when extensions.worktreeConfig is on,
// .git/config.worktree, in git's order (later wins), following include.path
// and includeIf "onbranch:" to files inside .git. What it cannot evaluate in
// a browser (a path outside .git, gitdir:, hasconfig:) is listed by Unread.
func (r *Repo) loadConfig() error {
	l := &cfgLoader{r: r}
	if err := l.file(".git/config", 0); err != nil {
		return err
	}
	if v, ok := fold(l.list).get("extensions.worktreeconfig"); ok && isTrue(v) {
		if err := l.file(".git/config.worktree", 0); err != nil {
			return err
		}
	}
	r.cfg = fold(l.list)
	r.unread = l.unread
	return nil
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

type cfgLoader struct {
	r        *Repo
	list     []kv
	unread   []string
	files    int
	bytes    int64
	branch   string
	branchOK bool
}

func (l *cfgLoader) skip(s string) {
	for _, u := range l.unread {
		if u == s {
			return
		}
	}
	l.unread = append(l.unread, s)
}

// file reads one config file and the files it includes; a missing file is
// fine, as it is for git.
func (l *cfgLoader) file(name string, depth int) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%s: exceeded maximum include depth (%d)", name, maxIncludeDepth)
	}
	if l.files++; l.files > maxConfigFiles {
		return fmt.Errorf("%s: more than %d config files included", name, maxConfigFiles)
	}
	data, err := l.r.readFile(name, maxConfigBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if l.bytes += int64(len(data)); l.bytes > maxConfigTotal {
		return fmt.Errorf("%s: config files are over %d bytes in all", name, maxConfigTotal)
	}
	entries, err := parseConfigList(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for _, e := range entries {
		l.list = append(l.list, e)
		switch {
		case e.key == "include.path":
			if err := l.include(name, e.val, "include.path", depth); err != nil {
				return err
			}
		case strings.HasPrefix(e.key, "includeif.") && strings.HasSuffix(e.key, ".path") && len(e.key) > len("includeif..path"):
			cond := e.key[len("includeif.") : len(e.key)-len(".path")]
			label := "includeIf." + cond + ".path"
			ok, known := l.cond(cond)
			if !known {
				l.skip(label + " " + e.val)
			} else if ok {
				if err := l.include(name, e.val, label, depth); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// include follows a path relative to the including file when it stays inside
// .git; any other path is recorded as unread.
func (l *cfgLoader) include(from, val, label string, depth int) error {
	if val == "" {
		return nil
	}
	if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "~") {
		l.skip(label + " " + val)
		return nil
	}
	p := path.Join(path.Dir(from), val)
	if !strings.HasPrefix(p, ".git/") {
		l.skip(label + " " + val)
		return nil
	}
	return l.file(p, depth+1)
}

// cond evaluates an includeIf condition: its result, and whether it can be
// evaluated here at all. Conditions git does not know never match.
func (l *cfgLoader) cond(c string) (match, known bool) {
	switch {
	case strings.HasPrefix(c, "gitdir:"), strings.HasPrefix(c, "gitdir/i:"), strings.HasPrefix(c, "hasconfig:"):
		return false, false
	case strings.HasPrefix(c, "onbranch:"):
		pat := strings.TrimPrefix(c, "onbranch:")
		if strings.HasSuffix(pat, "/") {
			pat += "**"
		}
		b, ok := l.headBranch()
		return ok && wildmatch(pat, b), true
	}
	return false, true
}

func (l *cfgLoader) headBranch() (string, bool) {
	if !l.branchOK {
		l.branchOK = true
		if data, err := l.r.readFile(".git/HEAD", 4096); err == nil {
			t, _ := strings.CutPrefix(strings.TrimSpace(string(data)), "ref:")
			l.branch, _ = strings.CutPrefix(strings.TrimSpace(t), "refs/heads/")
			if strings.TrimSpace(t) == l.branch { // not under refs/heads/
				l.branch = ""
			}
		}
	}
	return l.branch, l.branch != ""
}

// wildmatch matches s against a git glob with path semantics: * and ? stop at
// a slash, ** crosses it as a whole path part, [...] is a class, \ escapes.
// A pattern that needs more than maxGlobSteps of work does not match.
func wildmatch(pat, s string) bool {
	steps := 0
	var m func(p, s string) bool
	m = func(p, s string) bool {
		for len(p) > 0 {
			if steps++; steps > maxGlobSteps {
				return false
			}
			switch p[0] {
			case '?':
				if s == "" || s[0] == '/' {
					return false
				}
				p, s = p[1:], s[1:]
			case '\\':
				if len(p) < 2 || s == "" || s[0] != p[1] {
					return false
				}
				p, s = p[2:], s[1:]
			case '[':
				if s == "" || s[0] == '/' {
					return false
				}
				ok, rest, valid := class(p, s[0])
				if !valid || !ok {
					return false
				}
				p, s = rest, s[1:]
			case '*':
				n := 0
				for n < len(p) && p[n] == '*' {
					n++
				}
				rest := p[n:]
				first := len(pat)-len(p) == 0 || pat[len(pat)-len(p)-1] == '/'
				if n >= 2 && first && (rest == "" || rest[0] == '/') {
					if rest == "" {
						return true
					}
					for k := 0; k <= len(s); k++ { // zero or more whole folders
						if (k == 0 || s[k-1] == '/') && m(rest[1:], s[k:]) {
							return true
						}
					}
					return false
				}
				for k := 0; ; k++ {
					if m(rest, s[k:]) {
						return true
					}
					if k >= len(s) || s[k] == '/' {
						return false
					}
				}
			default:
				if s == "" || s[0] != p[0] {
					return false
				}
				p, s = p[1:], s[1:]
			}
		}
		return s == ""
	}
	return m(pat, s)
}

// class matches c against the bracket class at the start of p.
func class(p string, c byte) (match bool, rest string, valid bool) {
	i := 1
	neg := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		neg = true
		i++
	}
	first := true
	for i < len(p) && (p[i] != ']' || first) {
		first = false
		lo := p[i]
		if lo == '\\' && i+1 < len(p) {
			i++
			lo = p[i]
		}
		hi := lo
		if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
			hi = p[i+2]
			i += 2
		}
		i++
		if lo <= c && c <= hi {
			match = true
		}
	}
	if i >= len(p) {
		return false, "", false
	}
	return match != neg, p[i+1:], true
}
