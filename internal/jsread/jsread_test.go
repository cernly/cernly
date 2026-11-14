package jsread

import (
	"fmt"
	"testing"

	"github.com/cernly/cernly/repo"
)

const sample = `import axios from "axios";

type Props<T> = { items: Array<T>; label?: string };

const API_BASE_URL: string = "https://api.vendor.io/v2";
const help = "https://docs.example.com/help";      // example domain: dropped
const site = "https://widgets-shop.be/about";

export function Card<T>({ items, label }: Props<T>) {
  const re = /"https:\/\/not-a-host\.com"/g;
  const token = process.env.NEXT_PUBLIC_TOKEN as string;
  const { STRIPE_KEY, SENTRY_DSN: dsn, DEBUG = "0" } = process.env;
  const region = import.meta.env.VITE_REGION;
  const key = process.env["MOLLIE_API_KEY"];
  const u = new URL("https://challenges.cloudflare.com/turnstile/v0/api.js");
  void fetch(` + "`${API_BASE_URL}/items`" + `);
  void fetch(u);
  void axios.post("https://hooks.slack.com/services/x", {});
  void window.fetch("https://api.mollie.com/v2/payments");
  return <div title="don't / split">{label} isn't {items.length}</div>;
}
`

func TestRead(t *testing.T) {
	res, err := Read(&repo.Repo{}, "src/card.tsx", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, e := range res.Env {
		env = append(env, fmt.Sprintf("%s:%d", e.Name, e.Line))
	}
	want := "[NEXT_PUBLIC_TOKEN:11 STRIPE_KEY:12 SENTRY_DSN:12 DEBUG:12 VITE_REGION:13 MOLLIE_API_KEY:14]"
	if fmt.Sprint(env) != want {
		t.Errorf("env = %v\nwant  %s", env, want)
	}
	best, usage := map[string]string{}, map[string]string{}
	rank := map[string]int{Mention: 1, Default: 2, Request: 3}
	for _, h := range res.Hosts {
		if rank[h.Usage] > rank[usage[h.Host]] {
			usage[h.Host] = h.Usage
			best[h.Host] = fmt.Sprintf("%s:%d", h.Usage, h.Line)
		}
	}
	for host, w := range map[string]string{
		"api.vendor.io":             "request:16",
		"challenges.cloudflare.com": "request:17",
		"hooks.slack.com":           "request:18",
		"api.mollie.com":            "request:19",
		"widgets-shop.be":           "mention:7",
	} {
		if best[host] != w {
			t.Errorf("%s = %q, want %q", host, best[host], w)
		}
	}
	for _, h := range []string{"docs.example.com", "not-a-host.com"} {
		if _, ok := best[h]; ok {
			t.Errorf("%s reported", h)
		}
	}
}

func TestSource(t *testing.T) {
	for p, want := range map[string]bool{
		"src/a.ts": true, "src/b.tsx": true, "lib/c.mjs": true, "types/x.d.ts": false, "src/a.test.ts": false,
		"src/__tests__/a.ts": false, "public/vendor.min.js": false, "README.md": false, "e2e/login.spec.ts": false,
	} {
		if got := Source(p); got != want {
			t.Errorf("Source(%s) = %v", p, got)
		}
	}
}

// Read goes through the repo's memo: a second read of the same file returns
// the cached result (the same backing array), a changed file is read again.
func TestReadMemo(t *testing.T) {
	r := &repo.Repo{}
	first, err := Read(r, "a.tsx", []byte(sample))
	if err != nil || len(first.Env) == 0 {
		t.Fatalf("first read: %v, %d env", err, len(first.Env))
	}
	again, _ := Read(r, "a.tsx", []byte(sample))
	if &again.Env[0] != &first.Env[0] {
		t.Error("second read of the same file was parsed again")
	}
	changed, _ := Read(r, "a.tsx", []byte(sample+"\n"))
	if len(changed.Env) == 0 || &changed.Env[0] == &first.Env[0] {
		t.Error("a changed file was served from the cache")
	}
}
