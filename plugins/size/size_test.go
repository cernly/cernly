package size

import (
	"fmt"
	"testing"

	"github.com/cernly/cernly/repo/repotest"
)

func TestSize(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"api/main.go":       "package main\n\nfunc main() {}\n",
		"api/util.go":       "package main", // no final newline: one line
		"api/empty.go":      "",
		"api/main_test.go":  "package main\n\nfunc TestX() {}\n",
		"api/schema.sql":    "create table t (id int);\n",
		"web/app.tsx":       "a\nb\n",
		"web/vendor.min.js": "x\n",
		"web/style.css":     "a{}\n",
		"README.md":         "# x\n",
	})
	var got []string
	for _, f := range (Plugin{}).Run(r).Facts {
		got = append(got, fmt.Sprint(f.Key, " ", f.Attrs["files"], " ", f.Attrs["lines"], " ", f.Evidence))
	}
	want := "[source_size:api:go 3 4 [{api/empty.go 1 0 } {api/main.go 1 3 } {api/util.go 1 0 }] source_size:api:go:test 1 3 [{api/main_test.go 1 3 }] " +
		"source_size:api:sql 1 1 [{api/schema.sql 1 0 }] source_size:web:typescript 1 2 [{web/app.tsx 1 2 }]]"
	if fmt.Sprint(got) != want {
		t.Errorf("got  %v\nwant %s", got, want)
	}
}
