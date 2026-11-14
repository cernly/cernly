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
