package nginxconf

import (
	"fmt"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := "# top\nhttp {\n  server {\n    server_name ${HOST} a.example.org; # c\n    return 200 \"ok; {not a block}\";\n    set $x 'a\\'b';\n    rewrite ^/(.*)$ /x/$1 break;\n  }\n}\n"
	nodes, err := ParseFile("n.conf", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	srv := nodes[0].Block[0]
	var got []string
	for _, d := range srv.Block {
		got = append(got, fmt.Sprintf("%d:%s%q", d.Line(), d.Name(), d.Values()))
	}
	want := `[4:server_name["${HOST}" "a.example.org"] 5:return["200" "ok; {not a block}"] 6:set["$x" "a'b"] 7:rewrite["^/(.*)$" "/x/$1" "break"]]`
	if fmt.Sprint(got) != want {
		t.Errorf("directives\n got %v\nwant %s", got, want)
	}
	if srv.Block[0].Args[0].File != "n.conf" {
		t.Error("tokens record their file")
	}
	for _, bad := range []string{"server {\n", "listen 80\n", "}\n", "x \"open;\n", "server_name ${A;\n"} {
		if _, err := ParseFile("b.conf", []byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	env, err := EnvNames([]byte("server { server_name ${A_HOST}; # ${IN_COMMENT}\n proxy_pass http://$host:${B_PORT}; }\n"))
	if err != nil || fmt.Sprint(env) != "[{A_HOST 1} {B_PORT 2}]" {
		t.Errorf("env = %v %v ($host is nginx's own; comments are not read)", env, err)
	}
}

func TestDeepBlocks(t *testing.T) {
	if _, err := ParseFile("d.conf", []byte(strings.Repeat("a{", 5_000))); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Errorf("deep blocks: %v", err)
	}
	ok := strings.Repeat("a {\n", 100) + strings.Repeat("}\n", 100)
	if _, err := ParseFile("d.conf", []byte(ok)); err != nil {
		t.Error(err)
	}
}
