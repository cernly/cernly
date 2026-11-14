package outbound

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo/repotest"
)

// Imports between the repo's own packages, across two modules, with every
// import line as evidence; outside packages, tests and folders without Go files
// are not imports.
func TestImports(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"go.mod":                     "module example.com/shop\n\ngo 1.24\n",
		"cmd/api/main.go":            "package main\n\nimport (\n\t\"fmt\"\n\tb \"example.com/shop/internal/billing\"\n\t\"example.com/shop/internal/gone\"\n\t\"example.com/lib/money\"\n)\n",
		"cmd/api/extra.go":           "package main\n\nimport \"example.com/shop/internal/billing\"\n",
		"internal/billing/b.go":      "package billing\n\nimport \"example.com/lib/money\"\n",
		"internal/billing/b_test.go": "package billing\n\nimport \"example.com/shop/cmd/api\"\n",
		"lib/go.mod":                 "module example.com/lib\n\ngo 1.24\n",
		"lib/money/money.go":         "package money\n\nimport \"github.com/shopspring/decimal\"\n",
	})
	var got []string
	for _, f := range (Plugin{}).Run(r).Facts {
		if f.Kind != fact.KindImport {
			continue
		}
		got = append(got, fmt.Sprintf("%s %s>%s %v", f.Key, f.Attrs["from_dir"], f.Attrs["to_dir"], f.Evidence))
	}
	want := []string{
		"import:go:example.com/shop/cmd/api:example.com/lib/money cmd/api>lib/money [{cmd/api/main.go 7 0 .}]",
		"import:go:example.com/shop/cmd/api:example.com/shop/internal/billing cmd/api>internal/billing [{cmd/api/extra.go 3 0 .} {cmd/api/main.go 5 0 .}]",
		"import:go:example.com/shop/internal/billing:example.com/lib/money internal/billing>lib/money [{internal/billing/b.go 3 0 .}]",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOutbound(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"backend/go.mod": "module example.com/api\n\ngo 1.24\n",
		"backend/hibp/hibp.go": `package hibp

import nethttp "net/http"

func Check(prefix string) {
	req, _ := nethttp.NewRequestWithContext(nil, "GET",
		"https://api.pwnedpasswords.com/range/"+prefix, nil)
	_ = req
}
`,
		"backend/price/consts.go": `package price

const pageURL = "https://www.example-news.com/markets.html"
`,
		"backend/price/fetch.go": `package price

import (
	"fmt"
	"net/http"
	"net/url"
)

func Fetch() {
	_, _ = http.Get(pageURL)
	u, _ := url.Parse("https://rates.ecb.europa.eu/x")
	_, _ = http.Get(u.String())
	s := fmt.Sprintf("https://api.vendor.io/v1/%s", "x")
	_, _ = http.Post(s, "", nil)
}
`,
		"backend/config/config.go": `package config

const DefaultBaseURL = "https://ec.europa.eu"

func envStr(k, d string) string { return d }

var chift = envStr("CHIFT_API_BASE_URL", "https://api.chift.eu")
`,
		"backend/mail/mail.go": `package mail

var footer = "Visit https://widgets-shop.be"
var link = "https://widgets-shop.be/help"
var local = "http://minio:9000"
var sample = "https://app.example.com"
`,
		"backend/mail/mail_test.go":   "package mail\n\nvar t = \"https://only-in-tests.io\"\n",
		"backend/testdata/fixture.go": "package testdata\n\nvar x = \"https://only-in-testdata.io\"\n",
	})
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	got := map[string]string{}
	for _, f := range res.Facts {
		if f.Kind != fact.KindOutboundHost {
			continue
		}
		got[f.Attrs["host"].(string)] = fmt.Sprint(f.Attrs["usage"], " ", f.Evidence[0].Unit)
	}
	want := map[string]string{
		"api.pwnedpasswords.com": "request backend",
		"www.example-news.com":   "request backend",
		"rates.ecb.europa.eu":    "request backend",
		"api.vendor.io":          "request backend",
		"ec.europa.eu":           "default backend",
		"api.chift.eu":           "default backend",
		"widgets-shop.be":        "mention backend",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}
