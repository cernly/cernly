package caddyfile

import (
	"fmt"
	"strings"
	"testing"
)

func TestLex(t *testing.T) {
	src := "a.example.com {\n\trespond \"x # y \\\" z\" 200 # comment\n\theader X `raw {here}`\n\treverse_proxy \\\n\t\tapi:80\n\trespond <<HTML\n\t<p>{ }</p>\n\tHTML 200\n\tredir {scheme}://x{uri}\n}\n"
	toks, err := Lex([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tk := range toks {
		if tk.Text != "\n" {
			got = append(got, fmt.Sprintf("%d:%s", tk.Line, tk.Text))
		}
	}
	want := "[1:a.example.com 1:{ 2:respond 2:x # y \" z 2:200 3:header 3:X 3:raw {here} 4:reverse_proxy 5:api:80 6:respond 6:<p>{ }</p> 8:200 9:redir 9:{scheme}://x{uri} 10:}]"
	if fmt.Sprint(got) != want {
		t.Errorf("tokens\n got %v\nwant %s", got, want)
	}
	nodes, err := Parse(toks)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].Block) != 5 || nodes[0].Block[2].Name() != "reverse_proxy" || len(nodes[0].Block[2].Args) != 2 {
		t.Errorf("nodes = %+v", nodes)
	}
	if _, err := Parse(mustLex(t, "a {\n}\n}\n")); err == nil {
		t.Error("an unexpected } parsed")
	}
	if _, err := Lex([]byte("a \"open\n")); err == nil {
		t.Error("an unterminated quote lexed")
	}
}

func TestDeepBlocks(t *testing.T) {
	if _, err := Parse(mustLex(t, strings.Repeat("{ ", 5_000))); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Errorf("deep blocks: %v", err)
	}
	ok := strings.Repeat("a {\n", 100) + strings.Repeat("}\n", 100)
	if _, err := Parse(mustLex(t, ok)); err != nil {
		t.Error(err)
	}
}

// Placeholders in a heredoc are on the body's lines, and a repeated name
// gets each occurrence's own line.
func TestEnvNamesLines(t *testing.T) {
	src := "respond <<EOF\n  {$A}\n  {$A}\n  {$B}\n  EOF\nheader X \"{env.C}\n{$A:d}\"\n"
	es, err := EnvNames([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := "[{A 2} {A 3} {B 4} {C 6} {A 7}]"
	if got := fmt.Sprint(es); got != want {
		t.Errorf("env names\n got %s\nwant %s", got, want)
	}
}

func mustLex(t *testing.T, s string) []Token {
	toks, err := Lex([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return toks
}
