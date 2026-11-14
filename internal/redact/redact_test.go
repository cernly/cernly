package redact

import "testing"

func TestUserinfo(t *testing.T) {
	for in, want := range map[string]string{
		"git+https://user:TOKEN@github.com/acme/x.git#v1": "git+https://github.com/acme/x.git#v1",
		"git+https://ghp_abc123@github.com/acme/x.git":    "git+https://github.com/acme/x.git",
		"git+https://oauth2:TOKEN@gitlab.com/a/b.git@v2":  "git+https://gitlab.com/a/b.git@v2",
		"https://user:p@ss@host:8080/simple":              "https://host:8080/simple",
		"https://u:p@a.com/x https://v:q@b.com/y":         "https://a.com/x https://b.com/y",
		"http://admin:secret@backend:8080":                "http://backend:8080",
		"git+ssh://git@github.com/acme/x.git":             "git+ssh://git@github.com/acme/x.git",
		"git+ssh://deploy@github.com/acme/x.git":          "git+ssh://github.com/acme/x.git",
		"git@github.com:acme/x.git":                       "git@github.com:acme/x.git",
		"https://registry.npmjs.org/@scope/pkg/-/x.tgz":   "https://registry.npmjs.org/@scope/pkg/-/x.tgz",
		"npm:@scope/pkg@^1.2.0":                           "npm:@scope/pkg@^1.2.0",
		"^1.2.3":                                          "^1.2.3",
		"":                                                "",
	} {
		if got := Userinfo(in); got != want {
			t.Errorf("Userinfo(%q) = %q, want %q", in, got, want)
		}
	}
}
