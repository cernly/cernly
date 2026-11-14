package caddy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo/repotest"
)

func TestAddress(t *testing.T) {
	for in, want := range map[string]string{
		"http://app.Example.com":    "http app.example.com  ",
		":8083":                     "  8083 ",
		"{$HOST}":                   "   HOST",
		"http://{$HOST:x.local}:81": "http  81 HOST",
		"h2c://zitadel-api:8080":    "h2c zitadel-api 8080 ",
		"example.com/path":          " example.com  ",
		"[::1]:443":                 " [::1] 443 ",
	} {
		a := parseAddress(in)
		if got := a.scheme + " " + a.host + " " + a.port + " " + a.hostEnv; got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func run(t *testing.T, files map[string]string) plugin.Result {
	t.Helper()
	r := repotest.Files(t, files)
	return Plugin{}.Run(r)
}

// A snippet that imports itself is a cycle: skipped with an error, not
// expanded to the depth limit (exponential before).
func TestSnippetImportCycle(t *testing.T) {
	res := run(t, map[string]string{
		"Caddyfile": "(s) {\n" + strings.Repeat("  import s\n", 6) + "  reverse_proxy app:80\n}\nexample.com {\n  import s\n}\n",
	})
	if len(res.Facts) != 1 {
		t.Errorf("facts = %d, want 1", len(res.Facts))
	}
	if len(res.Errors) != 6 || !strings.Contains(res.Errors[0].Message, "import cycle: snippet s") {
		t.Errorf("errors = %v, want 6 import cycle errors", res.Errors)
	}
}

// Files that import each other, at the top level and inside a site.
func TestFileImportCycle(t *testing.T) {
	res := run(t, map[string]string{
		"Caddyfile": strings.Repeat("import a.caddy\n", 6) + "example.com {\n" + strings.Repeat("  import b.caddy\n", 6) + "}\n",
		"a.caddy":   strings.Repeat("import a.caddy\n", 6),
		"b.caddy":   strings.Repeat("import b.caddy\n", 6) + "reverse_proxy app:80\n",
	})
	if len(res.Facts) != 1 {
		t.Errorf("facts = %d, want 1", len(res.Facts))
	}
	if len(res.Errors) != 12 || !strings.Contains(res.Errors[0].Message, "import cycle: file") {
		t.Errorf("errors = %v, want 12 import cycle errors", res.Errors)
	}
}

// A diamond of snippet imports without a cycle stops at maxImports.
func TestImportBudget(t *testing.T) {
	var b strings.Builder
	for i := 1; i < 8; i++ {
		fmt.Fprintf(&b, "(s%d) {\n%s}\n", i, strings.Repeat(fmt.Sprintf("  import s%d\n", i+1), 6))
	}
	b.WriteString("(s8) {\n  reverse_proxy app:80\n}\nexample.com {\n  import s1\n}\n")
	res := run(t, map[string]string{"Caddyfile": b.String()})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "imports from Caddyfile") {
		t.Errorf("errors = %v, want the import budget error", res.Errors)
	}
}

// Credentials in an upstream or redirect URL never reach a fact.
func TestCredentials(t *testing.T) {
	res := run(t, map[string]string{
		"Caddyfile": "example.net {\n  reverse_proxy http://admin:SECRET1@app:8080\n  redir /old https://u:SECRET2@example.org/new 301\n}\n",
	})
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	data, _ := json.Marshal(res.Facts)
	out := string(data)
	if strings.Contains(out, "SECRET") {
		t.Errorf("credential in facts: %s", out)
	}
	if !strings.Contains(out, `"host":"app"`) || !strings.Contains(out, `"https://example.org/new"`) {
		t.Errorf("upstream or target lost: %s", out)
	}
}

// A leading byte order mark (dropped by repo.Read) is not part of the first
// site address.
func TestBOM(t *testing.T) {
	res := run(t, map[string]string{"Caddyfile": "\xEF\xBB\xBFexample.com {\n  reverse_proxy app:80\n}\n"})
	data, _ := json.Marshal(res.Facts)
	if out := string(data); len(res.Errors) > 0 || strings.Contains(out, "\ufeff") || !strings.Contains(out, `"example.com"`) {
		t.Errorf("errors %v, facts %s", res.Errors, out)
	}
}

// Two reverse_proxy lines on one matcher: Caddy serves the first, so its
// upstreams stay and the second is only evidence.
func TestDuplicateRouteKeepsFirst(t *testing.T) {
	res := run(t, map[string]string{
		"Caddyfile": "example.com {\n  reverse_proxy /api/* api:8080\n  reverse_proxy /api/* other:9090\n}\n",
	})
	if len(res.Errors) > 0 || len(res.Facts) != 1 {
		t.Fatalf("facts %d, errors %v", len(res.Facts), res.Errors)
	}
	f := res.Facts[0]
	data, _ := json.Marshal(f.Attrs)
	if !strings.Contains(string(data), `"host":"api"`) || strings.Contains(string(data), "other") {
		t.Errorf("upstreams = %s, want the first (api:8080)", data)
	}
	lines := map[int]bool{}
	for _, e := range f.Evidence {
		lines[e.Line] = true
	}
	if !lines[2] || !lines[3] {
		t.Errorf("evidence %v, want lines 2 and 3", f.Evidence)
	}
}

// Repeated imports of a large file are bounded by the directives produced,
// not only by the number of import events (39 GB before).
func TestImportAmplification(t *testing.T) {
	res := run(t, map[string]string{
		"Caddyfile": "example.com {\n  reverse_proxy app:80\n}\n" + strings.Repeat("import big.caddy\n", 1000),
		"big.caddy": strings.Repeat("a 1\n", 50000),
	})
	if len(res.Facts) == 0 {
		t.Error("the sites read before the limit are kept")
	}
	found := false
	for _, e := range res.Errors {
		found = found || strings.Contains(e.Message, "directives from")
	}
	if !found {
		t.Errorf("no budget error: %v", res.Errors)
	}
}
