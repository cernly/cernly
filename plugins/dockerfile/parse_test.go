package dockerfile

import (
	"fmt"
	"testing"
)

// CRLF line endings: BuildKit's parser joins continuations and keeps lines.
func TestCRLF(t *testing.T) {
	res, err := parse([]byte("FROM node:20 AS build\r\nRUN npm ci \\\r\n  && npm run build\r\nFROM nginx\r\nCOPY --from=build /a \\\r\n  /b\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range res.AST.Children {
		got = append(got, fmt.Sprintf("%d-%d %s %q", n.StartLine, n.EndLine, n.Value, args(n)))
	}
	want := `[1-1 FROM ["node:20" "AS" "build"] 2-3 RUN ["npm ci   && npm run build"] 4-4 FROM ["nginx"] 5-6 COPY ["/a" "/b"]]`
	if fmt.Sprint(got) != want {
		t.Errorf("got %s", got)
	}
}

// An ADD URL keeps no credentials, also when an ARG puts them there.
func TestRemoteCredentials(t *testing.T) {
	res, err := parse([]byte("ARG TOKEN=SECRET2\nFROM alpine:3.20\nARG TOKEN\nADD https://u:SECRET1@files.acme.dev/ca.pem /etc/ca.pem\nADD https://${TOKEN}@files.acme.dev/key /key\n"))
	if err != nil {
		t.Fatal(err)
	}
	st := readStages(res)
	var got []string
	for _, r := range st[len(st)-1].remote {
		got = append(got, r.url)
	}
	if len(got) != 2 || got[0] != "https://files.acme.dev/ca.pem" || got[1] != "https://files.acme.dev/key" {
		t.Errorf("remote = %q", got)
	}
}
