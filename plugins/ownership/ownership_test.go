package ownership

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
)

func describe(fs []fact.Fact) string {
	var out []string
	for _, f := range fs {
		var lines []string
		for _, e := range f.Evidence {
			lines = append(lines, fmt.Sprint(e.Line))
		}
		out = append(out, fmt.Sprintf("%s %v @%s", f.Key, f.Attrs["patterns"], strings.Join(lines, ",")))
	}
	return strings.Join(out, "\n")
}

func TestCodeowners(t *testing.T) {
	src := `# comment
*            @acme/platform   # everyone
/web/        @acme/web jane@example.org
/api/        @acme/payments @Acme/Platform
docs/my\ notes @octo-dev
/vendor/
\#odd        @octo-dev
`
	got := describe(Codeowners(".github/CODEOWNERS", []byte(src)))
	want := `owner:@acme/payments [/api/] @4
owner:@acme/platform [* /api/] @2,4
owner:@acme/web [/web/] @3
owner:@octo-dev [docs/my notes #odd] @5,7
owner:jane@example.org [/web/] @3`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	for _, f := range Codeowners("CODEOWNERS", []byte(src)) {
		switch f.Key {
		case "owner:@acme/platform":
			if f.Attrs["default"] != true || f.Attrs["type"] != "team" {
				t.Errorf("platform: %v", f.Attrs)
			}
		case "owner:jane@example.org":
			if f.Attrs["type"] != "email" || f.Attrs["default"] != false {
				t.Errorf("jane: %v", f.Attrs)
			}
		case "owner:@octo-dev":
			if f.Attrs["type"] != "user" {
				t.Errorf("octo: %v", f.Attrs)
			}
		}
	}
}

// GitLab sections give their default owners to entries without their own.
func TestCodeownersSections(t *testing.T) {
	src := `[Docs] @acme/writers
docs/
README.md @octo-dev
^[Infra][2] @acme/ops
deploy/
[Empty]
misc/
`
	got := describe(Codeowners("CODEOWNERS", []byte(src)))
	want := `owner:@acme/ops [deploy/] @5
owner:@acme/writers [docs/] @2
owner:@octo-dev [README.md] @3`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestCatalog(t *testing.T) {
	src := `apiVersion: backstage.io/v1alpha1
kind: Component
metadata:
  name: Billing-API
  namespace: payments
  tags: [go]
spec:
  type: service
  owner: group:acme/payments
  system: shop
  lifecycle: production
---
apiVersion: backstage.io/v1alpha1
kind: Location
metadata:
  name: more
---
apiVersion: backstage.io/v1alpha1
kind: Component
metadata:
  name: ${{ values.name }}
---
apiVersion: example.org/v1
kind: Component
metadata:
  name: other
---
apiVersion: backstage.io/v1alpha1
kind: System
metadata:
  name: shop
spec:
  owner: ${{ values.owner }}
  domain: commerce
`
	fs, errs := Catalog("catalog-info.yaml", []byte(src))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	got := describe(fs)
	want := "catalog_entity:component:payments/billing-api <nil> @2,4,6,8,9,10,11\ncatalog_entity:system:default/shop <nil> @29,31,34"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	a := fs[0].Attrs
	if a["name"] != "Billing-API" || a["owner"] != "group:acme/payments" || a["lifecycle"] != "production" || fmt.Sprint(a["tags"]) != "[go]" {
		t.Errorf("attrs %v", a)
	}
	if _, ok := fs[1].Attrs["owner"]; ok {
		t.Errorf("a placeholder owner must be left out: %v", fs[1].Attrs)
	}
	if _, errs := Catalog("x.yaml", []byte("a: [")); len(errs) != 1 {
		t.Errorf("invalid YAML: %v", errs)
	}
}
