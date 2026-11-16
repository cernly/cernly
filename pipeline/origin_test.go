package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteID(t *testing.T) {
	for u, want := range map[string]string{
		"git@github.com:Owner/Repo.git":                "github.com/owner/repo",
		"git@github.com:owner/repo":                    "github.com/owner/repo",
		"ssh://git@github.com/owner/repo.git":          "github.com/owner/repo",
		"ssh://git@host:2222/owner/repo.git":           "host/owner/repo",
		"ssh://git@host:2222/owner/repo/":              "host/owner/repo",
		"https://github.com/owner/repo.git":            "github.com/owner/repo",
		"https://user:token@github.com/owner/repo.git": "github.com/owner/repo",
		"https://host:8443/owner/repo.git":             "host/owner/repo",
		"https://user:token@host:8443/owner/repo":      "host/owner/repo",
		"/srv/mirror.git":                              "",
	} {
		if got := remoteID(u); got != want {
			t.Errorf("remoteID(%q) = %q, want %q", u, got, want)
		}
	}
}

// Without an origin the id is the folder's name, also when the folder is ".".
func TestOriginIDOfDot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := OriginID("."); got != "local/shop" {
		t.Errorf("OriginID(.) = %q", got)
	}
}
