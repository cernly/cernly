package traefik

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo/repotest"
)

func TestParseRule(t *testing.T) {
	ms, err := parseRule("Host(`a.example.net`, `b.example.net`) && (PathPrefix(`/api`) || Path(\"/x\")) && !Method(`DELETE`, 'PUT')")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ms); got != "[{Host [a.example.net b.example.net] false} {PathPrefix [/api] false} {Path [/x] false} {Method [DELETE PUT] true}]" {
		t.Errorf("matchers = %s", got)
	}
	for _, bad := range []string{"Host(`a`", "Host(a)", "&& Host(`a`)", "Host(`a`) junk", "(Host(`a`)"} {
		if _, err := parseRule(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRouters(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"compose.yaml": "services:\n  web:\n    image: nginx:1.27\n    labels:\n      - traefik.http.routers.web.rule=Host(`www.example.net`)\n      - traefik.http.routers.web.middlewares=auth\n      - traefik.http.middlewares.auth.basicauth.users=admin:$$apr1$$secret\n" +
			"  off:\n    image: x\n    labels:\n      traefik.enable: \"false\"\n      traefik.http.routers.off.rule: Host(`off.example.net`)\n" +
			"  api:\n    image: y\n    labels:\n      traefik.http.routers.api.rule: Host(`api.example.net`) && Method(`POST`)\n      traefik.http.routers.api.service: apisvc\n      traefik.http.services.apisvc.loadbalancer.server.port: \"8080\"\n      traefik.http.services.other.loadbalancer.server.port: \"9\"\n",
		"traefik/dynamic.toml": "[http.routers.legacy]\n  rule = \"Host(`old.example.net`)\"\n  service = \"legacy\"\n\n[[http.services.legacy.loadBalancer.servers]]\n  url = \"http://backend:7000/\"\n",
	})
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	var got []string
	for _, f := range res.Facts {
		a := f.Attrs
		got = append(got, fmt.Sprintf("%s %v %v %v %v@%d", f.Key, a["addresses"], a["methods"], a["upstreams"], a["labels_on"], f.Evidence[0].Line))
	}
	want := "[route:traefik:compose.yaml:web [map[address:www.example.net host:www.example.net]] <nil> [map[address:web host:web]] web@5 " +
		"route:traefik:compose.yaml:api [map[address:api.example.net host:api.example.net]] [POST] [map[address:api:8080 host:api port:8080]] api@16 " +
		"route:traefik:traefik/dynamic.toml:legacy [map[address:old.example.net host:old.example.net]] <nil> [map[address:http://backend:7000/ host:backend port:7000 scheme:http]] <nil>@2]"
	if fmt.Sprint(got) != want {
		t.Errorf("routes\n got %v\nwant %s", got, want)
	}
	for _, f := range res.Facts {
		if strings.Contains(fmt.Sprint(f.Attrs), "apr1") {
			t.Error("a basic-auth label reached a fact")
		}
	}
}

// A server URL with credentials keeps only its host and port.
func TestURLCredentials(t *testing.T) {
	u := parseURL("http://admin:SECRET@backend:7000/")
	if got := fmt.Sprintf("%s %s %s", u.address, u.host, u.port); got != "http://backend:7000/ backend 7000" {
		t.Errorf("upstream = %s", got)
	}
}

func TestMatchTypeAndRewrite(t *testing.T) {
	label := func(name, rule string, extra ...string) string {
		s := "  " + name + ":\n    image: x\n    labels:\n      - traefik.http.routers." + name + ".rule=" + rule + "\n"
		for _, e := range extra {
			s += "      - " + e + "\n"
		}
		return s
	}
	compose := "services:\n" +
		label("pre", "PathPrefix(`/api`)") +
		label("exa", "Path(`/x`)") +
		label("hst", "Host(`a.example.com`)") +
		label("rgx", "PathRegexp(`^/a`)") +
		label("str", "PathPrefix(`/s`)", "traefik.http.routers.str.middlewares=sp", "traefik.http.middlewares.sp.stripprefix.prefixes=/s") +
		label("ext", "PathPrefix(`/e`)", "traefik.http.routers.ext.middlewares=sp@file") +
		label("und", "PathPrefix(`/u`)", "traefik.http.routers.und.middlewares=nope") +
		label("rep", "PathPrefix(`/r`)", "traefik.http.routers.rep.middlewares=rp", "traefik.http.middlewares.rp.replacepath.path=/z") +
		label("add", "PathPrefix(`/d`)", "traefik.http.routers.add.middlewares=ap", "traefik.http.middlewares.ap.addprefix.prefix=/z") +
		label("aut", "PathPrefix(`/t`)", "traefik.http.routers.aut.middlewares=au", "traefik.http.middlewares.au.basicauth.users=admin:x")
	r := repotest.Files(t, map[string]string{"compose.yaml": compose})
	got := map[string]string{}
	for _, f := range (Plugin{}).Run(r).Facts {
		rw, _ := f.Attrs["rewrite"].(map[string]any)
		got[f.Attrs["router"].(string)] = fmt.Sprintf("%v %v %v", f.Attrs["match_type"], rw["kind"], rw["prefix"])
	}
	for n, want := range map[string]string{
		"pre": "prefix none <nil>", "exa": "exact none <nil>", "hst": "host none <nil>", "rgx": "regex none <nil>",
		"str": "prefix strip /s", "ext": "prefix unknown <nil>", "und": "prefix unknown <nil>",
		"rep": "prefix unknown <nil>", "add": "prefix unknown <nil>", "aut": "prefix none <nil>",
	} {
		if got[n] != want {
			t.Errorf("%s: %q, want %q", n, got[n], want)
		}
	}
}
