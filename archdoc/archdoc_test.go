package archdoc

import (
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// The Routes column lists only the edges that reach the row's own service: an
// edge with the same route key to another service (another stack mounting the
// same file) is not repeated in it.
func TestEndpointsRoutesOwnService(t *testing.T) {
	m := &rules.Map{
		Endpoints: []rules.EndpointRow{{Endpoint: "endpoint:a", Service: "service:compose:prod:web", Method: "GET", Path: "/health",
			Auth: rules.EndpointAuth{State: "anonymous"}, Routes: []string{"route:r"}}},
		Routes: []rules.RouteEdge{
			{Route: "route:r", Stack: "prod", To: "service:compose:prod:web", Sites: []string{"shop.example.com"}, Match: "/*"},
			{Route: "route:r", Stack: "staging", To: "service:compose:staging:web", Sites: []string{"staging.example.com"}, Match: "/*"},
		},
	}
	d := &doc{s: &fact.Scan{}, m: m, o: Options{MaxEvidence: 5}, facts: map[string]fact.Fact{}, ids: map[string]int{}}
	d.endpoints(fact.Fact{Key: "service:compose:prod:web", Attrs: map[string]any{"name": "web"}})
	out := d.b.String()
	if !strings.Contains(out, "| shop.example.com /* |") || strings.Contains(out, "staging.example.com") {
		t.Errorf("routes column:\n%s", out)
	}
}

func TestHostileStringsStayLiteral(t *testing.T) {
	img := `<img src=x onerror=alert(1)>`
	pipe := "a|b"
	link := "](javascript:alert(1))"
	tick := "x`<script>y</script>"
	nl := "line1\n# injected\nline2"
	mer := `q" ]` + "\n" + `end["x`
	path := "dir (x)/a b](https://evil)#<i>.yml"
	key := "service:compose:prod:" + img
	svc := fact.Fact{Kind: fact.KindService, Key: key, Attrs: map[string]any{"name": img + pipe + link + tick + nl + mer, "image": mer + "%%{init: {'theme':'forest'}}%%"},
		Evidence: []fact.Evidence{{Path: path, Line: 3}}}
	s := &fact.Scan{Repo: "github.com/o/" + img, Commit: fact.Commit{SHA: "abc1234567"}, Facts: []fact.Fact{svc}}
	m := &rules.Map{
		Stacks:   []rules.StackRole{{Stack: "stack:compose:prod", Role: "production"}},
		Parties:  []rules.Party{{ID: mer, Name: img + pipe + link, Category: tick, Services: []string{key}, Signals: []rules.Signal{{Kind: nl, Fact: key}}}},
		Unknown:  []rules.UnknownHost{{Host: img + tick, From: []string{key}}},
		Findings: []rules.Finding{{Rule: "r", Subject: key, Detail: img + link + tick + pipe + nl, From: []string{key}}},
	}
	out := string(Build(s, m, Options{}))
	chk := strings.ReplaceAll(out, `\]`, "") // an escaped bracket is literal text
	for _, bad := range []string{"<img", "<script", "<i>", "](javascript", "](https://evil", "\n# injected", "\nend[", `q" ]`} {
		if strings.Contains(chk, bad) {
			t.Errorf("raw %q survived:\n%s", bad, out)
		}
	}
	for i, block := range strings.Split(out, "```mermaid") {
		if i > 0 && strings.Contains(strings.SplitN(block, "```", 2)[0], "%%{") {
			t.Errorf("a Mermaid directive survived in a diagram:\n%s", block)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, "&lt;img") {
			if n := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|"); n != 6 {
				t.Errorf("row has %d unescaped pipes, want 6: %q", n, line)
			}
		}
	}
	for _, want := range []string{"&lt;img src=x onerror=alert(1)&gt;", `a\|b`, `\]`, "#quot;", "%28x%29/a%20b%5D"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in output:\n%s", want, out)
		}
	}
}

// The title escape is one pass: the ; of an entity code stays as it is.
func TestMermaidTitle(t *testing.T) {
	if got, want := mermaidTitle(`a"b;c%d`), "a#quot;b#59;c#37;d"; got != want {
		t.Errorf("mermaidTitle = %q, want %q", got, want)
	}
}

func TestPlainStringsUnchanged(t *testing.T) {
	for _, s := range []string{"api (stack prod)", "github.com/o/r", "docker/compose.yml:12", "a, b + c", "~ ^/api/"} {
		if md(s) != s || mermaidText(s) != s || urlPath(strings.ReplaceAll(s, " ", "")) != strings.ReplaceAll(s, " ", "") && !strings.ContainsAny(s, "()~") {
			t.Errorf("changed: %q", s)
		}
	}
	if got := urlPath("a/b-c_d.yml"); got != "a/b-c_d.yml" {
		t.Errorf("urlPath = %q", got)
	}
}

// A backslash is literal inside a code span, so only a table cell escapes a
// pipe there; prose and footnotes keep it as it is.
func TestCodePipes(t *testing.T) {
	if got, want := code("a|b"), "`a|b`"; got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
	if got, want := codeCell("a|b"), "`a\\|b`"; got != want {
		t.Errorf("codeCell = %q, want %q", got, want)
	}
}
