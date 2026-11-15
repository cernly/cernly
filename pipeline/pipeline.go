// Package pipeline scans a commit and derives its map. It is the one entry
// point for every caller, so the same commit always gives the same bytes.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/rules"
)

// Version is the extractor version every scan records. Bump on any output
// change; see CHANGELOG.md.
const Version = "0.4.1"

// ScanOptions says what Scan reads and how. An empty Commit scans the working
// tree, labelled with HEAD; an empty RepoID is taken from the origin remote.
type ScanOptions struct {
	RepoID      string
	Commit      string
	CommittedAt string
	Plugins     []plugin.Plugin
	Operator    config.Operator
}

// Scan runs every plugin on dir and returns the normalised scan.
func Scan(dir string, o ScanOptions) (*fact.Scan, []string, error) {
	return scan(dir, o, false)
}

// ScanWorkTree scans dir as it is on disk and only labels it with sha, also
// inside a git work tree: for test fixtures, whose commit is made up.
func ScanWorkTree(dir, repoID, sha, committedAt string, ps []plugin.Plugin, op config.Operator) (*fact.Scan, []string, error) {
	return scan(dir, ScanOptions{RepoID: repoID, Commit: sha, CommittedAt: committedAt, Plugins: ps, Operator: op}, true)
}

func scan(dir string, o ScanOptions, labelOnly bool) (*fact.Scan, []string, error) {
	repoID, sha, committedAt, ps, op := o.RepoID, o.Commit, o.CommittedAt, o.Plugins, o.Operator
	labelHEAD := sha == ""
	r, sha, err := openRepo(dir, sha, labelOnly)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	// Without a commit the working tree is read but labelled with HEAD: say
	// so when the two differ. A given commit is read from git, never dirty.
	dirty := false
	if labelHEAD {
		if dirty, err = r.Dirty(); err != nil {
			return nil, nil, err
		}
	}
	var hint []byte
	name, ignored := config.HintFile(r.Has, op.HintFallbacks)
	if name != "" {
		if hint, err = r.Read(name); err != nil {
			return nil, nil, err
		}
	}
	plan, err := config.BuildFrom(name, hint, op)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range ignored {
		plan.Record.Errors = append(plan.Record.Errors, fact.LocationError{Path: f, Message: "ignored: " + name + " is present"})
	}
	r.Restrict(plan.Apply(r.Files))
	if sha == "" || committedAt == "" {
		rev := sha
		if rev == "" {
			rev = "HEAD"
		}
		gs, gt, err := gitCommit(r.Root, rev)
		if err != nil {
			return nil, nil, fmt.Errorf("commit unknown (pass --commit and --committed-at outside a git checkout): %w", err)
		}
		if sha == "" {
			sha = gs
		}
		if committedAt == "" {
			committedAt = gt
		}
	}
	if repoID == "" {
		repoID = OriginID(r.Root)
	}
	s := &fact.Scan{
		Schema:    fact.SchemaID,
		Repo:      repoID,
		Commit:    fact.Commit{SHA: sha, CommittedAt: committedAt, Dirty: dirty},
		Extractor: fact.Extractor{Version: Version},
	}
	if !plan.Empty() {
		rec := plan.Record
		s.Config = &rec
	}
	// Two phases: plain plugins first, then consumers with the phase-1 facts
	// of the kinds they name, merged and sorted canonically.
	addRun := func(p plugin.Plugin, res plugin.Result, panicked error) {
		for _, f := range res.Facts {
			if len(f.Evidence) == 0 {
				// No evidence, no fact; the plugin bug shows in its run.
				res.Errors = append(res.Errors, fact.LocationError{Path: ".", Message: "fact without evidence dropped: " + f.Kind + " " + f.Key})
				continue
			}
			s.Facts = append(s.Facts, f)
		}
		status := "ok"
		switch {
		case panicked != nil:
			status = "failed"
			res.Errors = append(res.Errors, fact.LocationError{Path: ".", Message: "plugin panic: " + panicked.Error()})
		case res.Failed:
			status = "failed"
		case len(res.Errors) > 0:
			status = "partial"
		}
		run := fact.PluginRun{Name: p.Name(), Version: p.Version(), Source: fact.SourceBuiltin, Status: status, Errors: res.Errors}
		if o, ok := p.(plugin.Origin); ok {
			run.Source, run.SHA256, run.Signer = o.Source(), o.SHA256(), o.Signer()
		}
		s.Plugins = append(s.Plugins, run)
	}
	var consumers []plugin.Consumer
	for _, p := range ps {
		if c, ok := p.(plugin.Consumer); ok {
			consumers = append(consumers, c)
			continue
		}
		res, panicked := runSafe(func() plugin.Result { return p.Run(r) })
		addRun(p, res, panicked)
	}
	if len(consumers) > 0 {
		phase1 := fact.Scan{Facts: append([]fact.Fact(nil), s.Facts...)}
		phase1.Normalise()
		for _, c := range consumers {
			in := factsOf(phase1.Facts, c.Consumes())
			res, panicked := runSafe(func() plugin.Result { return c.RunWith(r, in) })
			addRun(c, res, panicked)
		}
	}
	return s, s.Normalise(), nil
}

func factsOf(facts []fact.Fact, kinds []string) []fact.Fact {
	var out []fact.Fact
	for _, f := range facts {
		if slices.Contains(kinds, f.Kind) {
			out = append(out, f)
		}
	}
	return out
}

func runSafe(run func() plugin.Result) (res plugin.Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("%v", v)
		}
	}()
	return run(), nil
}

// openRepo reads a given commit from git's objects when dir is a git checkout,
// a folder inside one, or a bare repository; otherwise, without a commit or
// with labelOnly, the files as they are.
func openRepo(dir, sha string, labelOnly bool) (*repo.Repo, string, error) {
	if sha != "" && !labelOnly {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return repo.OpenCommit(dir, sha)
		}
		if isBare(dir) {
			return repo.OpenCommit(dir, sha) // a bare mirror
		}
		// A folder inside a checkout: the commit's files below it, never the
		// working tree under the commit's name.
		if in, err := repo.InsideWorkTree(dir); err != nil {
			return nil, "", err
		} else if in {
			return repo.OpenCommit(dir, sha)
		}
	}
	r, err := repo.Open(dir)
	return r, sha, err
}

func gitCommit(dir, rev string) (sha, committedAt string, err error) {
	out, err := repo.Git(dir, "log", "-1", "--format=%H %cI", "--end-of-options", rev).Output()
	if err != nil {
		return "", "", err
	}
	parts := strings.Fields(string(out))
	if len(parts) != 2 {
		return "", "", errors.New("unexpected git log output")
	}
	return parts[0], utc(parts[1]), nil
}

var offsetRe = regexp.MustCompile(`([+-]\d\d:\d\d|Z)$`)

// utc rewrites git's ISO date with offset to UTC so the same commit gives the
// same string whatever time zone the committer was in.
func utc(iso string) string {
	t, err := parseTime(iso)
	if err != nil {
		return offsetRe.ReplaceAllString(iso, "Z")
	}
	return t
}

// sshURLRe is ssh://git@host[:port]/path; the port is not part of the id.
// scpRe is git@host:path, where a number after the colon is a path segment.
var (
	sshURLRe = regexp.MustCompile(`^ssh://git@([^:/]+)(?::\d+)?/(.+?)(?:\.git)?/?$`)
	scpRe    = regexp.MustCompile(`^git@([^:/]+)[:/](.+?)(?:\.git)?/?$`)
)

var httpsRe = regexp.MustCompile(`^https?://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+?)(?:\.git)?/?$`)

// originID turns the origin remote into host/owner/name. Credentials in an
// https remote never reach the output.
func OriginID(dir string) string {
	out, err := repo.Git(dir, "remote", "get-url", "origin").Output()
	if err == nil {
		if id := remoteID(strings.TrimSpace(string(out))); id != "" {
			return id
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // "." names the folder, as a scan of the working tree does
	}
	return "local/" + filepath.Base(dir)
}

// remoteID is host/owner/name for a remote URL, "" when it reads none.
func remoteID(u string) string {
	for _, re := range []*regexp.Regexp{sshURLRe, scpRe, httpsRe} {
		if m := re.FindStringSubmatch(u); m != nil {
			return strings.ToLower(m[1] + "/" + m[2])
		}
	}
	return ""
}

// Map reads a facts file and derives its map.
func Map(raw []byte, set rules.Settings) (*rules.Map, error) {
	var s fact.Scan
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Schema != fact.SchemaID {
		return nil, fmt.Errorf("schema %q, want %s", s.Schema, fact.SchemaID)
	}
	sum := sha256.Sum256(raw)
	return rules.Derive(&s, hex.EncodeToString(sum[:]), set), nil
}

// isBare reports a bare git repository: objects and HEAD at the top.
func isBare(dir string) bool {
	for _, p := range []string{"HEAD", "objects"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			return false
		}
	}
	return true
}

func parseTime(iso string) (string, error) {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "", err
	}
	return t.UTC().Format(time.RFC3339), nil
}
