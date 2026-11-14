package pyread

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo"
)

func TestLex(t *testing.T) {
	src := "x = f\"{a!r:>{w}} {{lit}} { {'k': \"v\"}['k'] }\" + r'\\d{2}' + '''tri\n\"quote\"''' \\\n  + b\"\\x00\"  # c\ny = 1\n"
	toks, err := lex(src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range toks {
		got = append(got, fmt.Sprintf("%d:%d:%q", k.line, k.kind, k.text))
	}
	want := `[1:0:"x" 1:5:"=" 1:3:"" 1:0:"a" 1:0:"w" 1:5:"{" 1:2:"k" 1:5:":" 1:2:"v" 1:5:"}" 1:5:"[" 1:2:"k" 1:5:"]" 1:4:"" 1:5:"+" 1:2:"\\d{2}" 1:5:"+" 1:2:"tri\n\"quote\"" 3:5:"+" 3:2:"x00" 4:0:"y" 4:5:"=" 4:1:"1"]`
	if fmt.Sprint(got) != want {
		t.Errorf("tokens\n got %v\nwant %s", got, want)
	}
	if !toks[0].first || toks[20].text != "y" || !toks[20].first || toks[17].first {
		t.Error("logical lines: a joined line is one line, the next starts a new one")
	}
	for _, bad := range []string{"x = 'open\n", "x = f'{a'\n", "s = '''never closed\n"} {
		if _, err := lex(bad); err == nil {
			t.Errorf("%q lexed", bad)
		}
	}
}

func TestNestedFStrings(t *testing.T) {
	if _, err := lex("x = " + strings.Repeat("f\"{", 10_000)); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Errorf("deep f-strings: %v", err)
	}
	ok := "x = " + strings.Repeat("f\"{", 50) + "a" + strings.Repeat("}\"", 50) + "\n"
	if _, err := lex(ok); err != nil {
		t.Error(err)
	}
}

const sample = `import os
from os import environ as env, getenv
from urllib.request import urlopen

import httpx
import requests
from pydantic_settings import BaseSettings, SettingsConfigDict

API_URL = os.environ.get("API_URL", "https://api.vendor.io/v1")
HOOK = f"https://hooks.slack.com/services/{os.environ['SLACK_PATH']}"
DOCS = "https://docs.example.org/x"  # example domain: not public
PING = "https://hc-ping.com/" + env["HC_UUID"]
label = f"{getenv("STAGE", "dev")}"


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="APP_")

    database_url: str
    token: str = Field(alias="RAW_TOKEN")
    _private: int = 1

    def method(self) -> None:
        local: int = 2


async def call():
    httpx.post("https://api.stripe.com/v1/charges")
    requests.get(f"{API_URL}/items")
    requests.request("GET", "https://api.github.com/repos")
    async with httpx.AsyncClient() as client:
        await client.post(HOOK)
    urlopen(PING)
    os.environ.setdefault("SETDEFAULT_VAR", "x")
    other.environ["NOT_OS"]
`

func TestMatch(t *testing.T) {
	res, err := Read(&repo.Repo{}, "app.py", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	var env, hosts []string
	for _, e := range res.Env {
		env = append(env, fmt.Sprintf("%s:%d%s", e.Name, e.Line, map[bool]string{true: "(settings)"}[e.How == "settings"]))
	}
	for _, h := range res.Hosts {
		hosts = append(hosts, fmt.Sprintf("%s:%s:%d", h.Host, h.Usage, h.Line))
	}
	sort.Strings(env)
	sort.Strings(hosts)
	wantEnv := "[API_URL:9 APP_DATABASE_URL:19(settings) HC_UUID:12 SETDEFAULT_VAR:34 SLACK_PATH:10 STAGE:13]"
	wantHosts := "[api.github.com:mention:30 api.github.com:request:30 api.stripe.com:mention:28 api.stripe.com:request:28 api.vendor.io:default:9 api.vendor.io:mention:9 api.vendor.io:request:29 hc-ping.com:mention:12 hc-ping.com:request:33 hooks.slack.com:mention:10 hooks.slack.com:request:32]"
	if fmt.Sprint(env) != wantEnv {
		t.Errorf("env\n got %v\nwant %s", env, wantEnv)
	}
	if fmt.Sprint(hosts) != wantHosts {
		t.Errorf("hosts\n got %v\nwant %s", hosts, wantHosts)
	}
}

// A settings class whose base list is never closed reads as nothing.
func TestUnclosedSettingsClass(t *testing.T) {
	res, err := Read(&repo.Repo{}, "app.py", []byte("class A(BaseSettings"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Env) != 0 {
		t.Errorf("env %+v", res.Env)
	}
}

func TestSource(t *testing.T) {
	for p, want := range map[string]bool{"app/main.py": true, "tests/test_x.py": false, "app/test_x.py": false, "x_test.py": false,
		"conftest.py": false, "scripts/run.pyw": true, "main.go": false, "pkg/testing/util.py": false} {
		if Source(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}

// Read goes through the repo's memo: a second read of the same file returns
// the cached result (the same backing array), a changed file is read again.
func TestReadMemo(t *testing.T) {
	r := &repo.Repo{}
	first, err := Read(r, "app.py", []byte(sample))
	if err != nil || len(first.Env) == 0 {
		t.Fatalf("first read: %v, %d env", err, len(first.Env))
	}
	again, _ := Read(r, "app.py", []byte(sample))
	if &again.Env[0] != &first.Env[0] {
		t.Error("second read of the same file was parsed again")
	}
	changed, _ := Read(r, "app.py", []byte(sample+"\n"))
	if len(changed.Env) == 0 || &changed.Env[0] == &first.Env[0] {
		t.Error("a changed file was served from the cache")
	}
}
