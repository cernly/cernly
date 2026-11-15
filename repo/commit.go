package repo

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// OpenCommit gives the view of one commit without checking it out: the tracked
// files of its tree from `git ls-tree`, their content from git's object store.
// The working tree is never read, so a scan of old commits can run beside
// work in progress. Symlinks and submodules are skipped as in Open. Call Close when
// done.
func OpenCommit(root, rev string) (*Repo, string, error) {
	out, err := Git(root, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}").Output()
	if err != nil {
		return nil, "", fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	sha := strings.TrimSpace(string(out))
	// Run in a folder inside a work tree, ls-tree lists only what is below
	// it, relative to it; at the top or in a bare repository, everything.
	tree, err := Git(root, "ls-tree", "-r", "-z", sha).Output()
	if err != nil {
		return nil, "", fmt.Errorf("git ls-tree: %w", err)
	}
	blobs := map[string]string{}
	var files []string
	for _, e := range bytes.Split(tree, []byte{0}) {
		tab := bytes.IndexByte(e, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(string(e[:tab])) // mode type object
		if len(meta) != 3 || meta[1] != "blob" || meta[0] == "120000" {
			continue // symlinks and submodules (type commit) are never followed
		}
		p := string(e[tab+1:])
		if inSkippedDir(p) {
			continue
		}
		blobs[p] = meta[2]
		files = append(files, p)
	}
	sort.Strings(files)
	cat, err := startCat(root)
	if err != nil {
		return nil, "", err
	}
	r := &Repo{Root: root, blobs: blobs, cat: cat}
	r.setFiles(files)
	return r, sha, nil
}

// BranchRef names branch unambiguously for git: refs/heads/<branch>, or
// refs/remotes/origin/<branch> when there is no local head (a CLI scan of a
// clone). A bare name would let a tag of the same name win.
func BranchRef(dir, branch string) (string, error) {
	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
		if Git(dir, "show-ref", "--verify", "--quiet", ref).Run() == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("branch %q: neither refs/heads/%[1]s nor refs/remotes/origin/%[1]s exists", branch)
}

// Close stops the object reader of a commit view; a no-op for a checkout.
func (r *Repo) Close() error {
	if r.git != nil {
		r.git.Close()
	}
	if r.cat == nil {
		return nil
	}
	return r.cat.close()
}

// catFile keeps one `git cat-file --batch` running for all reads of a scan.
type catFile struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startCat(root string) (*catFile, error) {
	cmd := Git(root, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return &catFile{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}, nil
}

var errTooLarge = errors.New("blob too large")

// blob reads one object's content, errTooLarge when it is over max bytes;
// that content is skipped, so the stream stays in step.
func (c *catFile) blob(object string, max int64) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.in, object+"\n"); err != nil {
		return nil, err
	}
	header, err := c.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(header) // <object> <type> <size>, or <object> missing
	if len(f) != 3 || f[1] != "blob" {
		return nil, fmt.Errorf("git cat-file %s: %s", object, strings.TrimSpace(header))
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("git cat-file %s: %s", object, strings.TrimSpace(header))
	}
	if size > max {
		if _, err := c.out.Discard(int(size + 1)); err != nil {
			return nil, err
		}
		return nil, errTooLarge
	}
	data := make([]byte, size+1) // content and the trailing newline
	if _, err := io.ReadFull(c.out, data); err != nil {
		return nil, err
	}
	return data[:size], nil
}

func (c *catFile) close() error {
	c.in.Close()
	return c.cmd.Wait()
}
