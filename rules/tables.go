package rules

import (
	"sort"
	"strings"
	"unicode"

	"github.com/cernly/cernly/fact"
)

// Table is one database table as every schema source declares it: the SQL
// migrations and a Prisma schema that name the same qualified table are one
// table with two sources. The scanner already qualifies a name by its
// database convention (unqualified SQL is public unless the set is
// MySQL-shaped; Prisma on Postgres is public), so the name is the merge key and nothing
// is guessed here.
type Table struct {
	Table  string            `json:"table"`            // the qualified name, as the facts give it
	Schema string            `json:"schema,omitempty"` // the part before the last dot; absent when the name has none
	From   []string          `json:"from"`             // db_table fact keys, one per source
	Check  []SensitiveColumn `json:"check,omitempty"`  // columns whose name looks sensitive
}

// SensitiveColumn is a column whose name matches the word list: something to
// check (encryption and access), never a verdict. The name is all the rule
// reads; the facts cannot say how the value is stored or who reads it.
type SensitiveColumn struct {
	Column string   `json:"column"`
	Word   string   `json:"word"` // the list entry that matched
	From   []string `json:"from"` // db_column fact keys, one per source
}

// SensitiveWords is the fixed list. An entry of two segments matches two
// consecutive segments of the name.
var SensitiveWords = []string{
	"password", "passwd", "pwd", "passphrase",
	"secret", "token", "api_key", "apikey", "access_key", "private_key",
	"iban", "bic", "card", "cvv", "cvc",
	"ssn", "national_id",
	"email", "phone",
	"birth", "birthdate", "dob",
	"salary",
}

// Segments that say a column describes the value, points to it or configures
// it instead of holding it, when the name ends with one (token_count,
// password_changed_at, email_verified, email_id, email_settings), or starts
// with one (is_email, has_password).
var (
	sensitiveMetaLast = setOf("count", "at", "on", "verified", "enabled", "expires", "expiry", "expired", "length", "len", "type", "kind", "status", "format", "version", "changed", "updated", "created", "sent", "required", "set",
		"id", "ids", "settings", "config", "quota", "limit", "limits")
	sensitiveMetaFirst = setOf("is", "has")
)

func setOf(ws ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// NameSegments splits a column name on anything not a letter or digit and on
// camelCase boundaries, lowercased: "apiKeyHash" and "api_key_hash" are both
// [api key hash]; "HTTPToken" is [http token]; digits stay with the letters
// before them ("card_last4" is [card last4]).
func NameSegments(name string) []string {
	var out []string
	var cur []rune
	rs := []rune(name)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && len(cur) > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// SensitiveWord returns the list entry a column name matches, or "". The
// type exception: a boolean column holds no such value (email_verified).
func SensitiveWord(name, typ string) string {
	switch strings.ToLower(typ) {
	case "boolean", "bool":
		return ""
	}
	seg := NameSegments(name)
	if len(seg) == 0 || sensitiveMetaFirst[seg[0]] {
		return ""
	}
	for _, w := range SensitiveWords {
		ws := strings.Split(w, "_")
		for i := 0; i+len(ws) <= len(seg); i++ {
			if !equal(seg[i:i+len(ws)], ws) {
				continue
			}
			if end := i + len(ws); end < len(seg) && sensitiveMetaLast[seg[len(seg)-1]] {
				continue // the name describes the value (token_count)
			}
			return w
		}
	}
	return ""
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tables merges the db_table facts by qualified name and checks each
// column's name. facts are sorted by kind and key.
func tables(facts []fact.Fact) []Table {
	byName := map[string]*Table{}
	byKey := map[string]*Table{} // "db_column:<source>:<set>:<name>." -> table
	for _, f := range facts {
		if f.Kind != fact.KindDBTable {
			continue
		}
		name := f.Str("name")
		if name == "" {
			continue
		}
		t := byName[name]
		if t == nil {
			t = &Table{Table: name}
			if i := strings.LastIndex(name, "."); i > 0 {
				t.Schema = name[:i]
			}
			byName[name] = t
		}
		t.From = append(t.From, f.Key)
		byKey["db_column:"+strings.TrimPrefix(f.Key, "db_table:")+"."] = t
	}
	if len(byName) == 0 {
		return nil
	}
	checks := map[*Table]map[string]*SensitiveColumn{}
	for _, f := range facts {
		if f.Kind != fact.KindDBColumn {
			continue
		}
		dot := strings.LastIndex(f.Key, ".")
		t := byKey[f.Key[:dot+1]]
		if t == nil {
			continue
		}
		col := f.Str("name")
		w := SensitiveWord(col, f.Str("type"))
		if w == "" {
			continue
		}
		if checks[t] == nil {
			checks[t] = map[string]*SensitiveColumn{}
		}
		c := checks[t][col]
		if c == nil {
			c = &SensitiveColumn{Column: col, Word: w}
			checks[t][col] = c
		}
		c.From = append(c.From, f.Key)
	}
	out := make([]Table, 0, len(byName))
	for _, t := range byName {
		for _, c := range checks[t] {
			sort.Strings(c.From)
			t.Check = append(t.Check, *c)
		}
		sort.Slice(t.Check, func(i, j int) bool { return t.Check[i].Column < t.Check[j].Column })
		sort.Strings(t.From)
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}
