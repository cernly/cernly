package migrations

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo/repotest"
)

// Flyway versions compare part by part ('.' sorts before '_', so a plain
// string or digit-run compare puts V1.1__ before V1__); other names keep the
// digit-run order.
func TestBefore(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"V1.1__b.sql", "R__view.sql", "V1__a.sql", "V1_2__c.sql", "V2__d.sql", "V1.10__e.sql"},
			[]string{"V1__a.sql", "V1.1__b.sql", "V1_2__c.sql", "V1.10__e.sql", "V2__d.sql", "R__view.sql"}},
		{[]string{"db/V10__x.sql", "db/V9__y.sql", "db/V09.1__z.sql"},
			[]string{"db/V9__y.sql", "db/V09.1__z.sql", "db/V10__x.sql"}},
		{[]string{"10_b.up.sql", "2_a.up.sql", "20240101_c.sql"},
			[]string{"2_a.up.sql", "10_b.up.sql", "20240101_c.sql"}},
	} {
		got := append([]string(nil), c.in...)
		sort.Slice(got, func(i, j int) bool { return before(got[i], got[j]) })
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("order\n got %v\nwant %v", got, c.want)
		}
	}
}

// A MySQL-shaped set (one ENGINE= file) is lexed as MySQL as a whole: a later
// file with a backslash-escaped quote and a # comment still replays.
func TestMySQLSet(t *testing.T) {
	r := repotest.Files(t, map[string]string{
		"db/migrations/V1__init.sql":  "CREATE TABLE users (id int PRIMARY KEY) ENGINE=InnoDB;\n",
		"db/migrations/V2__notes.sql": "# a MySQL comment\nINSERT INTO users VALUES (1); -- 'x'\nCREATE TABLE notes (id int, body varchar(20) DEFAULT 'it\\'s');\n",
	})
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	var keys []string
	for _, f := range res.Facts {
		keys = append(keys, f.Key)
	}
	sort.Strings(keys)
	if s := fmt.Sprint(keys); !strings.Contains(s, "notes") || !strings.Contains(s, "users") {
		t.Errorf("facts = %v", keys)
	}
}
