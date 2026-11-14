package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Git runs git in dir. The scanned repository is untrusted and so is its
// .git/config, which can name programs for git to run: the overrides turn off
// the ones the read-only commands used here could reach. A scan never
// fetches: a partial clone would fetch a missing object from its promisor
// remote, and an ext:: or ssh remote is a command. Each protocol is turned
// off by name, because protocol.<name>.allow in the repository's config
// would win over protocol.allow; GIT_NO_LAZY_FETCH (git 2.45) stops the
// fetch itself.
func Git(dir string, args ...string) *exec.Cmd {
	return GitContext(context.Background(), dir, args...)
}

// GitContext is Git, killed when ctx is done.
func GitContext(ctx context.Context, dir string, args ...string) *exec.Cmd {
	base := []string{"-C", dir,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.sshCommand=false",
		"-c", "log.showSignature=false",
		"-c", "protocol.allow=never",
	}
	for _, p := range []string{"ext", "file", "ssh", "git", "http", "https", "fd"} {
		base = append(base, "-c", "protocol."+p+".allow=never")
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	return cmd
}

// noFilters overrides every filter driver git's config defines, wherever the
// attributes that pick one come from (.gitattributes, .git/info/attributes,
// core.attributesFile): a command left empty is not run, and none is
// required. git status runs the clean filter of a file it has to rehash.
func noFilters(dir string) ([]string, error) {
	out, err := Git(dir, "config", "-z", "--name-only", "--get-regexp", `^filter\.`).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil // no filter configured
		}
		return nil, fmt.Errorf("git config: %w", err)
	}
	var args []string
	seen := map[string]bool{}
	for _, k := range strings.Split(string(out), "\x00") {
		i, j := strings.Index(k, "."), strings.LastIndex(k, ".")
		if i < 0 || j <= i {
			continue
		}
		name := k[i+1 : j]
		if seen[name] {
			continue
		}
		seen[name] = true
		if strings.ContainsAny(name, "=\n") {
			return nil, fmt.Errorf("git filter %q: cannot be switched off safely", name)
		}
		for _, key := range []string{"clean", "smudge", "process"} {
			args = append(args, "-c", "filter."+name+"."+key+"=")
		}
		args = append(args, "-c", "filter."+name+".required=false")
	}
	return args, nil
}
