// Package gitfs reads a git repository's .git folder through an fs.FS, with
// no git process: the index, refs, commits, trees and the object store (loose
// objects and version 2 packs). It is what a scan in the browser uses, and it
// is read-only and stdlib-only. Paths in the FS are relative to the work tree,
// so the repository is at ".git".
//
// Not supported, each with a clear error: a .git file (linked worktree or
// submodule), reftable, alternates, a sparse index, other extensions.
package gitfs

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxPackedRefs = 64 << 20

// Repo is an opened .git folder. Call Close when done.
type Repo struct {
	fsys        fs.FS
	cfg         config
	format      string
	hashLen     int
	packs       []*pack
	packsLoaded bool
	maxEntries  int      // SetMaxEntries
	unread      []string // config includes that could not be followed
}

// SetMaxEntries limits the entries of the index (and, doubled, of a tree
// walk): an index that lists more is refused with *TooManyEntriesError before
// it is allocated. 0 is no limit beyond the fixed bounds. Call it before
// Index, Tree or Dirty.
func (r *Repo) SetMaxEntries(n int) { r.maxEntries = n }

// Unread lists the config includes Open could not follow, such as
// "include.path /etc/work.gitconfig": a path outside .git (absolute, in the
// home folder or above .git), includeIf with
// gitdir: or hasconfig:. The repository's origin and settings may differ from
// what git, with those files, sees.
func (r *Repo) Unread() []string { return r.unread }

// ErrUnborn is returned by Head when HEAD names a branch with no commit.
var ErrUnborn = errors.New("HEAD points at a branch with no commits")

// Open checks that fsys holds a repository this package can read.
func Open(fsys fs.FS) (*Repo, error) {
	fi, err := fs.Stat(fsys, ".git")
	if err != nil {
		return nil, fmt.Errorf("no .git folder: %w", err)
	}
	if !fi.IsDir() {
		return nil, errors.New(".git is a file (a linked worktree or a submodule): it points outside this folder; pick the main work tree or scan with the CLI")
	}
	if _, err := fs.Stat(fsys, ".git/HEAD"); err != nil {
		return nil, fmt.Errorf("not a git repository, no .git/HEAD: %w", err)
	}
	r := &Repo{fsys: fsys, format: "sha1", hashLen: 20, cfg: config{}}
	if err := r.loadConfig(); err != nil {
		return nil, err
	}
	version := 0
	if v, ok := r.cfg.get("core.repositoryformatversion"); ok {
		if version, err = strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("core.repositoryformatversion %q: not a number", v)
		}
	}
	if version > 1 {
		return nil, fmt.Errorf("repository format version %d is not supported", version)
	}
	if v, ok := r.cfg.get("core.bare"); ok && v == "true" {
		return nil, errors.New("a bare repository has no work tree; scan a commit with the CLI")
	}
	if version == 1 {
		if v, ok := r.cfg.get("extensions.refstorage"); ok && !strings.EqualFold(v, "files") {
			return nil, fmt.Errorf("ref storage %q is not supported (only files)", v)
		}
		if v, ok := r.cfg.get("extensions.objectformat"); ok {
			switch strings.ToLower(v) {
			case "sha1":
			case "sha256":
				r.format, r.hashLen = "sha256", 32
			default:
				return nil, fmt.Errorf("object format %q is not supported", v)
			}
		}
	}
	if _, err := fs.Stat(fsys, ".git/objects/info/alternates"); err == nil {
		return nil, errors.New("objects/info/alternates is not supported: the objects live outside this folder")
	}
	return r, nil
}

// Close releases the pack files.
func (r *Repo) Close() error {
	for _, p := range r.packs {
		p.close()
	}
	r.packs = nil
	return nil
}

// ObjectFormat is "sha1" or "sha256".
func (r *Repo) ObjectFormat() string { return r.format }

func (r *Repo) newHash() hash.Hash {
	if r.format == "sha256" {
		return sha256.New()
	}
	return sha1.New()
}

// BlobID hashes content as a blob, as `git hash-object` does without filters.
func (r *Repo) BlobID(content []byte) string {
	h := r.newHash()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// blobIDReader hashes size bytes from rd as a blob; ok is false when rd gives
// a different number of bytes.
func (r *Repo) blobIDReader(rd io.Reader, size int64) (string, bool, error) {
	h := r.newHash()
	fmt.Fprintf(h, "blob %d\x00", size)
	n, err := io.Copy(h, io.LimitReader(rd, size+1))
	if err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), n == size, nil
}

// OriginURL is remote.origin.url as `git remote get-url origin` gives it,
// with url.<base>.insteadOf applied; "" without an origin.
func (r *Repo) OriginURL() string {
	u, ok := r.cfg.get("remote.origin.url") // git's last value wins
	if !ok {
		return ""
	}
	best, base := 0, ""
	for k, vals := range r.cfg {
		if !strings.HasPrefix(k, "url.") || !strings.HasSuffix(k, ".insteadof") {
			continue
		}
		b := strings.TrimSuffix(strings.TrimPrefix(k, "url."), ".insteadof")
		for _, prefix := range vals {
			if strings.HasPrefix(u, prefix) && len(prefix) > best {
				best, base = len(prefix), b
			}
		}
	}
	if best > 0 {
		u = base + u[best:]
	}
	return u
}

func (r *Repo) isID(s string) bool {
	if len(s) != 2*r.hashLen {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Head is the commit id HEAD resolves to: through a branch, or detached. An
// unborn branch gives an error that wraps ErrUnborn.
func (r *Repo) Head() (string, error) {
	id, err := r.resolve("HEAD", 0)
	if errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errNoObject) {
		return "", fmt.Errorf("%w: %v", ErrUnborn, err)
	}
	return id, err
}

func (r *Repo) resolve(name string, depth int) (string, error) {
	if depth > 10 {
		return "", errors.New("symbolic refs nested too deep")
	}
	if name != "HEAD" && (!strings.HasPrefix(name, "refs/") || !fs.ValidPath(name) || strings.Contains(name, "\\")) {
		return "", fmt.Errorf("ref name %q is not valid", name)
	}
	data, err := r.readFile(path.Join(".git", name), 4096)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) || name == "HEAD" {
			return "", err
		}
		if id, ok, err := r.packedRef(name); err != nil {
			return "", err
		} else if ok {
			return id, nil
		}
		return "", fmt.Errorf("ref %s: %w", name, fs.ErrNotExist)
	}
	s := strings.TrimSpace(string(data))
	if target, ok := strings.CutPrefix(s, "ref:"); ok {
		return r.resolve(strings.TrimSpace(target), depth+1)
	}
	if !r.isID(s) {
		return "", fmt.Errorf("ref %s: not a %s object id", name, r.format)
	}
	return s, nil
}

// packedRef finds one ref in .git/packed-refs by scanning its lines.
func (r *Repo) packedRef(name string) (string, bool, error) {
	data, err := r.readFile(".git/packed-refs", maxPackedRefs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	for len(data) > 0 {
		var l []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			l, data = data[:i], data[i+1:]
		} else {
			l, data = data, nil
		}
		l = bytes.TrimSuffix(l, []byte("\r"))
		if len(l) == 0 || l[0] == '#' || l[0] == '^' {
			continue
		}
		if sp := bytes.IndexByte(l, ' '); sp >= 0 && string(l[sp+1:]) == name && r.isID(string(l[:sp])) {
			return string(l[:sp]), true, nil
		}
	}
	return "", false, nil
}

// Commit is what a scan needs of a commit.
type Commit struct {
	Tree        string
	CommittedAt string // RFC 3339 in UTC, from the committer line
}

// Commit reads a commit (an annotated tag is followed to its commit).
func (r *Repo) Commit(id string) (Commit, error) {
	for i := 0; i < 10; i++ {
		typ, data, err := r.object(id)
		if err != nil {
			return Commit{}, err
		}
		switch typ {
		case "tag":
			next, ok := headerValue(data, "object")
			if !ok || !r.isID(next) {
				return Commit{}, fmt.Errorf("tag %s: no object", id)
			}
			id = next
		case "commit":
			return r.parseCommit(id, data)
		default:
			return Commit{}, fmt.Errorf("object %s is a %s, not a commit", id, typ)
		}
	}
	return Commit{}, errors.New("tags nested too deep")
}

func headerValue(data []byte, key string) (string, bool) {
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		data = data[:i]
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, key+" "); ok {
			return v, true
		}
	}
	return "", false
}

func (r *Repo) parseCommit(id string, data []byte) (Commit, error) {
	tree, ok := headerValue(data, "tree")
	if !ok || !r.isID(tree) {
		return Commit{}, fmt.Errorf("commit %s: no tree", id)
	}
	line, ok := headerValue(data, "committer")
	if !ok {
		return Commit{}, fmt.Errorf("commit %s: no committer", id)
	}
	gt := strings.LastIndexByte(line, '>')
	f := strings.Fields(line[gt+1:])
	if gt < 0 || len(f) < 1 {
		return Commit{}, fmt.Errorf("commit %s: bad committer line", id)
	}
	secs, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("commit %s: bad committer date", id)
	}
	return Commit{Tree: tree, CommittedAt: time.Unix(secs, 0).UTC().Format(time.RFC3339)}, nil
}
