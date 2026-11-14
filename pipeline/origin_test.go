package pipeline

import "testing"

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
