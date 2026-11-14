package rules

import (
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
)

func TestNameSegments(t *testing.T) {
	for in, want := range map[string][]string{
		"api_key_hash": {"api", "key", "hash"},
		"apiKeyHash":   {"api", "key", "hash"},
		"HTTPToken":    {"http", "token"},
		"card_last4":   {"card", "last4"},
		"dateOfBirth":  {"date", "of", "birth"},
		"user-email":   {"user", "email"},
		"tokenizer":    {"tokenizer"},
		"":             nil,
	} {
		if got := NameSegments(in); !reflect.DeepEqual(got, want) {
			t.Errorf("NameSegments(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSensitiveWord(t *testing.T) {
	positives := map[string]string{
		"password":          "password",
		"password_hash":     "password",
		"passwordHash":      "password",
		"client_secret":     "secret",
		"refresh_token":     "token",
		"accessToken":       "token",
		"api_key":           "api_key",
		"apiKey":            "api_key",
		"apikey":            "apikey",
		"private_key":       "private_key",
		"iban":              "iban",
		"bank_bic":          "bic",
		"card_last4":        "card",
		"cvv":               "cvv",
		"ssn":               "ssn",
		"national_id":       "national_id",
		"email":             "email",
		"contact_email":     "email",
		"phone":             "phone",
		"date_of_birth":     "birth",
		"birthDate":         "birth",
		"birthdate":         "birthdate",
		"salary":            "salary",
		"two_factor_secret": "secret",
	}
	for name, want := range positives {
		if got := SensitiveWord(name, "text"); got != want {
			t.Errorf("SensitiveWord(%q) = %q, want %q", name, got, want)
		}
	}
	negatives := []string{
		"token_count",         // ends with a segment that describes the value
		"tokenizer",           // not a whole segment
		"tokens",              // not a whole segment
		"password_changed_at", // describes the value
		"email_verified",      // describes the value
		"email_verified_at",
		"emailId",         // points to the value
		"card_id",         // points to the value
		"emailSettings",   // configures it
		"emailRateLimits", // configures it
		"is_email",        // starts with is/has
		"has_password",    // starts with is/has
		"cardinality",
		"discard",
		"keyboard",
		"api",
		"key",
		"phonetic",
		"secretary_name",
		"name",
		"",
	}
	for _, name := range negatives {
		if got := SensitiveWord(name, "text"); got != "" {
			t.Errorf("SensitiveWord(%q) = %q, want no match", name, got)
		}
	}
	for _, typ := range []string{"boolean", "Boolean", "bool"} {
		if got := SensitiveWord("email_opt_in", typ); got != "" {
			t.Errorf("a %s column matched: %q", typ, got)
		}
	}
}

func TestTables(t *testing.T) {
	tbl := func(key, name string) fact.Fact {
		return fact.Fact{Kind: fact.KindDBTable, Key: key, Attrs: map[string]any{"name": name}, Evidence: []fact.Evidence{{Path: "x", Line: 1}}}
	}
	col := func(key, name, typ string) fact.Fact {
		return fact.Fact{Kind: fact.KindDBColumn, Key: key, Attrs: map[string]any{"name": name, "type": typ}, Evidence: []fact.Evidence{{Path: "x", Line: 2}}}
	}
	s := &fact.Scan{Facts: []fact.Fact{
		tbl("db_table:prisma:prisma:public.user", "public.user"),
		col("db_column:prisma:prisma:public.user.email", "email", "String"),
		col("db_column:prisma:prisma:public.user.email_verified", "email_verified", "Boolean"),
		col("db_column:prisma:prisma:public.user.id", "id", "Int"),
		tbl("db_table:sql:db/migrations:public.user", "public.user"),
		col("db_column:sql:db/migrations:public.user.email", "email", "text"),
		col("db_column:sql:db/migrations:public.user.token_count", "token_count", "integer"),
		tbl("db_table:sql:db/migrations:billing.invoice", "billing.invoice"),
		col("db_column:sql:db/migrations:billing.invoice.total", "total", "bigint"),
		tbl("db_table:sql:mysql:orders", "orders"),
	}}
	m := Derive(s, "", Settings{DefaultBranch: "main"})
	want := []Table{
		{Table: "billing.invoice", Schema: "billing", From: []string{"db_table:sql:db/migrations:billing.invoice"}},
		{Table: "orders", From: []string{"db_table:sql:mysql:orders"}},
		{Table: "public.user", Schema: "public",
			From: []string{"db_table:prisma:prisma:public.user", "db_table:sql:db/migrations:public.user"},
			Check: []SensitiveColumn{{Column: "email", Word: "email",
				From: []string{"db_column:prisma:prisma:public.user.email", "db_column:sql:db/migrations:public.user.email"}}}},
	}
	if !reflect.DeepEqual(m.Tables, want) {
		t.Errorf("tables:\n got %+v\nwant %+v", m.Tables, want)
	}
	if m := Derive(&fact.Scan{}, "", Settings{}); m.Tables != nil {
		t.Errorf("no db_table facts gave %v, want absent", m.Tables)
	}
}
