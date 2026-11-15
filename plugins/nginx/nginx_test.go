package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo"
	"github.com/cernly/cernly/repo/repotest"
)

func TestRoutes(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"deploy/nginx/nginx.conf":       "events {}\nhttp {\n  include /etc/nginx/conf.d/*.conf;\n  upstream app { server app1:8000; server app2:8000; }\n}\n",
		"deploy/nginx/conf.d/site.conf": "server {\n  listen 443 ssl;\n  server_name example.net;\n  root /srv;\n  location /app/ { proxy_pass http://app; }\n  location /old { return 301 https://example.net/new; }\n  location /static/ { }\n  location @fallback { return https://example.net/; }\n}\n",
	})
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, f := range res.Facts {
		a := f.Attrs
		got = append(got, fmt.Sprintf("%s %s %v %v %v", f.Key, a["action"], a["upstreams"], a["status"], a["target"]))
	}
	want := "[" +
		"route:nginx:deploy/nginx/nginx.conf:https://example.net:443:* file_server <nil> <nil> <nil> " +
		"route:nginx:deploy/nginx/nginx.conf:https://example.net:443:/app/ proxy [map[address:app1:8000 host:app1 port:8000 scheme:http] map[address:app2:8000 host:app2 port:8000 scheme:http]] <nil> <nil> " +
		"route:nginx:deploy/nginx/nginx.conf:https://example.net:443:/old redirect <nil> 301 https://example.net/new " +
		"route:nginx:deploy/nginx/nginx.conf:https://example.net:443:/static/ file_server <nil> <nil> <nil> " +
		"route:nginx:deploy/nginx/nginx.conf:https://example.net:443:@fallback redirect <nil> 302 https://example.net/]"
	if fmt.Sprint(got) != want {
		t.Errorf("routes\n got %v\nwant %s\n(the included site belongs to nginx.conf, its root, and is not read on its own)", got, want)
	}
	if addr := fmt.Sprint(res.Facts[0].Attrs["addresses"]); addr != "[map[address:example.net host:example.net port:443 scheme:https]]" {
		t.Errorf("address = %s", addr)
	}
}

// A port 80 server that redirects and a 443 one that proxies, same name, are
// two routes, each with its own evidence.
func TestRoutesPerListen(t *testing.T) {
	root := t.TempDir()
	conf := "server {\n  listen 80;\n  server_name example.net;\n  location / { return 301 https://example.net$request_uri; }\n}\n" +
		"server {\n  listen 443 ssl;\n  server_name example.net;\n  location / { proxy_pass http://app:8000; }\n}\n"
	full := filepath.Join(root, "nginx.conf")
	os.WriteFile(full, []byte(conf), 0o644)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, f := range res.Facts {
		got = append(got, fmt.Sprintf("%s %s %v", f.Key, f.Attrs["action"], f.Evidence))
	}
	want := "[" +
		"route:nginx:nginx.conf:http://example.net:80:/ redirect [{nginx.conf 1 0 } {nginx.conf 3 0 } {nginx.conf 4 0 } {nginx.conf 4 0 }] " +
		"route:nginx:nginx.conf:https://example.net:443:/ proxy [{nginx.conf 6 0 } {nginx.conf 8 0 } {nginx.conf 9 0 } {nginx.conf 9 0 }]]"
	if fmt.Sprint(got) != want {
		t.Errorf("routes\n got %v\nwant %s", got, want)
	}
}

// A file that includes itself is a cycle: skipped with an error, not expanded
// to the depth limit (exponential before).
func TestIncludeCycle(t *testing.T) {
	b := strings.Repeat("include b.conf;\n", 7) + "server {\n  listen 80;\n  server_name example.net;\n  location / { proxy_pass http://app:8000; }\n}\n"
	r := repotest.Files(t, map[string]string{
		"nginx/nginx.conf": "http { include b.conf; }\n",
		"nginx/b.conf":     b,
	})
	res := Plugin{}.Run(r)
	if len(res.Facts) != 1 {
		t.Errorf("facts = %d, want 1", len(res.Facts))
	}
	if len(res.Errors) != 7 || !strings.Contains(res.Errors[0].Message, "include cycle") {
		t.Errorf("errors = %v, want 7 include cycle errors", res.Errors)
	}
}

// A diamond of includes without a cycle stops at maxIncludes.
func TestIncludeBudget(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"nginx.conf": strings.Repeat("include l1.conf;\n", 10)}
	for i := 1; i < 6; i++ {
		files[fmt.Sprintf("l%d.conf", i)] = strings.Repeat(fmt.Sprintf("include l%d.conf;\n", i+1), 10)
	}
	files["l6.conf"] = "server { listen 80; }\n"
	os.MkdirAll(filepath.Join(root, "nginx"), 0o755)
	for p, c := range files {
		os.WriteFile(filepath.Join(root, "nginx", p), []byte(c), 0o644)
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "includes from nginx/nginx.conf") {
		t.Errorf("errors = %v, want the include budget error", res.Errors)
	}
}

// Credentials in a proxy_pass or return URL never reach a fact.
func TestCredentials(t *testing.T) {
	root := t.TempDir()
	conf := "server {\n  listen 80;\n  server_name example.net;\n  location /api/ { proxy_pass http://admin:SECRET1@app:8000/; }\n  location /old { return 301 https://u:SECRET2@example.org/new; }\n}\n"
	os.WriteFile(filepath.Join(root, "nginx.conf"), []byte(conf), 0o644)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	out := fmt.Sprint(res.Facts)
	if strings.Contains(out, "SECRET") {
		t.Errorf("credential in facts: %s", out)
	}
	if !strings.Contains(out, "host:app port:8000") || !strings.Contains(out, "https://example.org/new") {
		t.Errorf("upstream or target lost: %s", out)
	}
}

func TestMatchTypeAndRewrite(t *testing.T) {
	root := t.TempDir()
	conf := "events {}\nhttp {\n  server {\n    listen 80;\n    server_name example.net;\n" +
		"    location /api/ { proxy_pass http://web:8080; }\n" +
		"    location /v1/ { proxy_pass http://web:8080/; }\n" +
		"    location /v2/ { proxy_pass http://web:8080/other/; }\n" +
		"    location = /x { proxy_pass http://web:8080; }\n" +
		"    location ~ ^/a { proxy_pass http://web:8080; }\n" +
		"    location /rw/ { rewrite ^/rw/(.*) /$1 break; proxy_pass http://web:8080; }\n" +
		"    location /var/ { proxy_pass http://$up; }\n" +
		"    location / { proxy_pass http://web:8080; }\n" +
		"  }\n}\n"
	os.WriteFile(filepath.Join(root, "nginx.conf"), []byte(conf), 0o644)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	got := map[string]string{}
	for _, f := range res.Facts {
		rw, _ := f.Attrs["rewrite"].(map[string]any)
		got[fmt.Sprint(f.Attrs["paths"])] = fmt.Sprintf("%v %v %v %v", f.Attrs["match_type"], rw["kind"], rw["prefix"], rw["to"])
	}
	for p, want := range map[string]string{
		"[/api/]": "prefix none <nil> <nil>",
		"[/v1/]":  "prefix replace /v1/ /",
		"[/v2/]":  "prefix replace /v2/ /other/",
		"[= /x]":  "exact none <nil> <nil>",
		"[~ ^/a]": "regex none <nil> <nil>",
		"[/rw/]":  "prefix unknown <nil> <nil>",
		"[/var/]": "prefix unknown <nil> <nil>",
		"[/]":     "prefix none <nil> <nil>",
	} {
		if got[p] != want {
			t.Errorf("%s: %q, want %q", p, got[p], want)
		}
	}
}

func TestRewriteInAncestorsIsUnknown(t *testing.T) {
	for name, conf := range map[string]string{
		"server":   "server { listen 80; server_name example.net; rewrite ^/api/(.*)$ /$1 break; location /api/ { proxy_pass http://web; } }\n",
		"if":       "server { listen 80; server_name example.net; location /api/ { if ($x) { rewrite ^ /y break; } proxy_pass http://web; } }\n",
		"nested":   "server { listen 80; server_name example.net; location /a/ { rewrite ^ /y break; location /a/b/ { proxy_pass http://web; } } }\n",
		"serverif": "server { listen 80; server_name example.net; if ($x) { rewrite ^ /y break; } location /api/ { proxy_pass http://web; } }\n",
	} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "nginx.conf"), []byte("events {}\nhttp {\n"+conf+"}\n"), 0o644)
		r, err := repo.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range (Plugin{}).Run(r).Facts {
			if p, _ := f.Attrs["paths"].([]any); len(p) == 1 && (p[0] == "/api/" || p[0] == "/a/b/") {
				found = true
				if rw, _ := f.Attrs["rewrite"].(map[string]any); rw["kind"] != "unknown" {
					t.Errorf("%s: rewrite = %v", name, rw)
				}
			}
		}
		if !found {
			t.Errorf("%s: route missing", name)
		}
	}
}

// Repeated includes of a large file are bounded by the directives produced,
// not only by the number of include events (31 GB before).
func TestIncludeAmplification(t *testing.T) {
	root := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	write("nginx/nginx.conf", "events {}\nhttp {\n  server { listen 80; location / { return 200; } }\n"+strings.Repeat("  include big.conf;\n", 1000)+"}\n")
	write("nginx/big.conf", strings.Repeat("a 1;\n", 50000))
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res := Plugin{}.Run(r)
	if len(res.Facts) == 0 {
		t.Error("the routes read before the limit are kept")
	}
	found := false
	for _, e := range res.Errors {
		found = found || strings.Contains(e.Message, "directives from")
	}
	if !found {
		t.Errorf("no budget error: %v", res.Errors)
	}
}
