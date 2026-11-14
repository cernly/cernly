package sqlddl

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// pgHint: these tests were written for PostgreSQL sets; a set's own SQL
// still decides when it shows a dialect (TestDialect).
var pgHint = Dialect{Name: PostgreSQL, Signal: "test", File: "m/hint", Line: 1}

func replayText(t *testing.T, files ...string) ([]*Table, bool, []Problem) {
	t.Helper()
	var fs []File
	for i, src := range files {
		st, err := Split([]byte(src))
		if err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
		fs = append(fs, File{Path: fmt.Sprintf("m/%03d.sql", i+1), Stmts: st})
	}
	tables, d, problems := Replay(fs, pgHint)
	return tables, d.Name == MySQL, problems
}

// dump writes tables as one line each: name [rls] then columns with flags.
func dump(tables []*Table) string {
	var b strings.Builder
	for _, t := range tables {
		b.WriteString(t.Qualified())
		if t.RLS {
			b.WriteString(" rls")
		}
		b.WriteString(":")
		for _, c := range t.Columns {
			b.WriteString(" " + c.Name + "=" + c.Type)
			for _, f := range []struct {
				on   bool
				name string
			}{{c.NotNull, "!"}, {c.Primary, "pk"}, {c.Unique, "u"}, {c.Enum, "enum"}} {
				if f.on {
					b.WriteString("," + f.name)
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

const pg1 = `-- the first migration
CREATE TYPE "Role" AS ENUM ('ADMIN', 'USER');
CREATE TABLE IF NOT EXISTS public.users (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email VARCHAR ( 320 ) NOT NULL UNIQUE,
  "role" "Role" NOT NULL DEFAULT 'USER',
  key text,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
  amount numeric(10,2),
  tags text[],
  CONSTRAINT users_email_ck CHECK (email <> '')
);
CREATE TEMP TABLE scratch (x int);
CREATE FUNCTION f() RETURNS trigger AS $body$ BEGIN; CREATE TABLE fake (x int); END $body$ LANGUAGE plpgsql;
/* nested /* comment */ CREATE TABLE ghost (x int); */
CREATE TABLE orders (id serial, user_id bigint REFERENCES users(id), note text DEFAULT E'it\'s; fine');
`

const pg2 = `ALTER TABLE orders ADD COLUMN total numeric(12, 2) NOT NULL, ADD CONSTRAINT orders_pk PRIMARY KEY (id);
ALTER TABLE ONLY orders RENAME COLUMN note TO remark;
ALTER TABLE orders ALTER COLUMN remark TYPE varchar(200) USING remark::varchar, ALTER COLUMN remark SET NOT NULL;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE legacy ADD COLUMN x int;
ALTER TABLE orders DROP COLUMN IF EXISTS user_id;
CREATE TABLE tmp_old (a int);
ALTER TABLE tmp_old RENAME TO archive;
CREATE TABLE gone (a int);
DROP TABLE IF EXISTS gone, also_gone CASCADE;
-- +goose Down
DROP TABLE users;
`

func TestPostgres(t *testing.T) {
	tables, mysql, problems := replayText(t, pg1, pg2)
	if mysql || len(problems) > 0 {
		t.Fatalf("mysql %v, problems %v", mysql, problems)
	}
	want := `public.archive: a=int
public.orders: id=serial,!,pk remark=varchar(200),! total=numeric(12, 2),!
public.users rls: id=bigint,!,pk email=varchar(320),!,u role=Role,!,enum key=text created_at=timestamp with time zone,! amount=numeric(10, 2) tags=text[]
`
	if got := dump(tables); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	var orders *Table
	for _, tb := range tables {
		if tb.Name == "orders" {
			orders = tb
		}
	}
	remark := orders.column("remark")
	var lines []string
	for _, e := range remark.Evidence {
		lines = append(lines, fmt.Sprintf("%s:%d", e.File, e.Line))
	}
	if strings.Join(lines, " ") != "m/001.sql:16 m/002.sql:2 m/002.sql:3" {
		t.Errorf("remark evidence %v", lines)
	}
	if e := orders.Evidence[0]; e.File != "m/001.sql" || e.Line != 16 {
		t.Errorf("orders created at %+v", e)
	}
}

func TestMySQL(t *testing.T) {
	tables, mysql, _ := replayText(t, "CREATE TABLE `users` (\n"+
		"  `id` int NOT NULL AUTO_INCREMENT,\n"+
		"  `name` varchar(255) CHARACTER SET utf8mb4 DEFAULT NULL,\n"+
		"  PRIMARY KEY (`id`),\n"+
		"  UNIQUE KEY `uniq_name` (`name`),\n"+
		"  KEY `idx` (`name`)\n"+
		") ENGINE=InnoDB;\n"+
		"ALTER TABLE `users` MODIFY COLUMN `name` varchar(100) NOT NULL;\n"+
		"RENAME TABLE users TO people;\n")
	if !mysql {
		t.Error("not seen as MySQL")
	}
	if got, want := dump(tables), "people: id=int,!,pk name=varchar(100),!,u\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Only an ENGINE= table option marks a set MySQL-shaped; a column named
// engine keeps schemas apart.
func TestEngineMarker(t *testing.T) {
	tables, mysql, _ := replayText(t, "CREATE TABLE auth.users (id int); CREATE TABLE public.users (id int, email text);\n"+
		"CREATE TABLE cars (id int, engine text, CHECK (engine <> '')) WITH (fillfactor = 70);\nALTER TABLE cars ADD COLUMN engine2 int;")
	if mysql {
		t.Error("engine column seen as MySQL")
	}
	if got, want := dump(tables), "auth.users: id=int\npublic.cars: id=int engine=text engine2=int\npublic.users: id=int email=text\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	for _, src := range []string{"CREATE TABLE t (id int) ENGINE=InnoDB;", "CREATE TEMPORARY TABLE t (id int)\n  DEFAULT CHARSET=utf8 ENGINE = MyISAM;"} {
		if _, mysql, _ := replayText(t, src); !mysql {
			t.Errorf("%q: not seen as MySQL", src)
		}
	}
}

// A MySQL-shaped set is lexed with backslash escapes and # comments; a
// PostgreSQL set keeps the backslash literal outside E'…'.
func TestSplitSetMySQL(t *testing.T) {
	set := func(srcs ...string) ([]*Table, bool, []error) {
		var in [][]byte
		for _, s := range srcs {
			in = append(in, []byte(s))
		}
		stmts, errs := SplitSet(in)
		var fs []File
		for i, st := range stmts {
			if errs[i] == nil {
				fs = append(fs, File{Path: fmt.Sprintf("m/%03d.sql", i+1), Stmts: st})
			}
		}
		tables, d, _ := Replay(fs, pgHint)
		mysql := d.Name == MySQL
		var bad []error
		for _, err := range errs {
			if err != nil {
				bad = append(bad, err)
			}
		}
		return tables, mysql, bad
	}
	// No other marker: only MySQL's lexing reads the file.
	tables, mysql, errs := set("CREATE TABLE users (id int, note varchar(9) DEFAULT 'it\\'s', q text DEFAULT \"a\\\"b\");\nCREATE TABLE t (x int);")
	if got, want := dump(tables), "t: x=int\nusers: id=int note=varchar(9) q=text\n"; !mysql || len(errs) > 0 || got != want {
		t.Errorf("got %q want %q, mysql %v, errors %v", got, want, mysql, errs)
	}
	// A line starting with # is a comment, even an even number of quotes.
	tables, mysql, errs = set("# users\nCREATE TABLE users (id int, note text DEFAULT 'it\\'s');\n# don't\nCREATE TABLE t (x int);")
	if got, want := dump(tables), "t: x=int\nusers: id=int note=text\n"; !mysql || len(errs) > 0 || got != want {
		t.Errorf("got %q want %q, mysql %v, errors %v", got, want, mysql, errs)
	}
	// A backticked file elsewhere in the set makes the # comment one here.
	tables, _, errs = set("CREATE TABLE `a` (id int);", "# b\nCREATE TABLE b (id int);")
	if got, want := dump(tables), "a: id=int\nb: id=int\n"; len(errs) > 0 || got != want {
		t.Errorf("got %q want %q, errors %v", got, want, errs)
	}
	// PostgreSQL: 'a\' is a whole string.
	tables, mysql, errs = set("CREATE TABLE p (path text DEFAULT 'C:\\', e text DEFAULT E'x\\'y');")
	if got, want := dump(tables), "public.p: path=text e=text\n"; mysql || len(errs) > 0 || got != want {
		t.Errorf("got %q want %q, mysql %v, errors %v", got, want, mysql, errs)
	}
	// Unreadable either way: still an error.
	if _, _, errs := set("CREATE TABLE x (a text DEFAULT 'open);"); len(errs) != 1 {
		t.Errorf("errors %v", errs)
	}
}

// Prisma's generated migrations quote every name.
func TestPrismaMigration(t *testing.T) {
	tables, _, _ := replayText(t, `-- CreateEnum
CREATE TYPE "Plan" AS ENUM ('FREE', 'TEAM');

-- CreateTable
CREATE TABLE "Account" (
    "id" TEXT NOT NULL,
    "plan" "Plan" NOT NULL DEFAULT 'FREE',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "Account_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "Account_id_key" ON "Account"("id");
`, `-- AlterTable
ALTER TABLE "Account" ADD COLUMN     "name" TEXT;
`)
	if got, want := dump(tables), "public.Account: id=text,!,pk plan=Plan,!,enum createdAt=timestamp(3),! name=text\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// ALTER TABLE … ADD with nothing after it changes nothing.
func TestEmptyAdd(t *testing.T) {
	tables, _, _ := replayText(t, "CREATE TABLE t (id int); ALTER TABLE t ADD;")
	if got, want := dump(tables), "public.t: id=int\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSplitErrors(t *testing.T) {
	for _, src := range []string{"SELECT 'open", "/* open", "SELECT $x$ open", `CREATE TABLE "open (`} {
		if _, err := Split([]byte(src)); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
	st, err := Split([]byte("\\connect db\nSELECT 1;\n-- migrate:down\nDROP TABLE x;"))
	if err != nil || len(st) != 1 {
		t.Errorf("meta-command or dbmate down: %v %d", err, len(st))
	}
}

// Only SQL that shows PostgreSQL puts unqualified tables in public and
// says whether row-level security is on: a generic schema
// file (HSQLDB, H2, SQLite) keeps its names.
func TestDialect(t *testing.T) {
	read := func(hint Dialect, srcs ...string) (string, Dialect) {
		var fs []File
		for i, src := range srcs {
			st, err := Split([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			fs = append(fs, File{Path: fmt.Sprintf("m/%03d.sql", i+1), Stmts: st})
		}
		tables, d, _ := Replay(fs, hint)
		var names []string
		for _, tb := range tables {
			names = append(names, tb.Qualified())
		}
		return strings.Join(names, ","), d
	}
	for _, c := range []struct {
		srcs   []string
		names  string
		signal string
		line   int
	}{
		{[]string{"DROP TABLE pets IF EXISTS;\nCREATE TABLE pets (id INTEGER IDENTITY PRIMARY KEY, name VARCHAR(30));"}, "pets", "", 0},
		{[]string{"CREATE TABLE owners (id int);", "\nCREATE EXTENSION IF NOT EXISTS citext;"}, "public.owners", "CREATE EXTENSION", 2},
		{[]string{"CREATE TABLE owners (id bigserial);"}, "public.owners", "SERIAL type", 1},
		{[]string{"CREATE TABLE owners (id int);\nALTER TABLE owners ENABLE ROW LEVEL SECURITY;"}, "public.owners", "ROW LEVEL SECURITY", 2},
		{[]string{"CREATE TABLE owners (id int, at timestamp DEFAULT '2020-01-01'::timestamp);"}, "public.owners", ":: cast", 1},
		{[]string{"CREATE TABLE owners (id int);\nCREATE FUNCTION f() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql;"}, "public.owners", "$$ body", 2},
		{[]string{"CREATE TABLE billing.invoices (id int);\nCREATE TABLE owners (id int);"}, "billing.invoices,public.owners", "schema-qualified table", 1},
		{[]string{"CREATE TABLE `owners` (id int);"}, "owners", "backticks", 1},
	} {
		names, d := read(Dialect{}, c.srcs...)
		if names != c.names || d.Signal != c.signal || d.Line != c.line {
			t.Errorf("%q: %s %+v, want %s %q line %d", c.srcs, names, d, c.names, c.signal, c.line)
		}
	}
	// A hint (Prisma's migration_lock.toml) counts when the SQL says nothing.
	if names, d := read(pgHint, "CREATE TABLE owners (id int);"); names != "public.owners" || d.Signal != "test" {
		t.Errorf("hint: %s %+v", names, d)
	}
}

func TestSet(t *testing.T) {
	s := NewSet("", "dbo")
	s.Tables()["dbo.orders"] = &Table{Schema: "dbo", Name: "orders"}
	st, err := Split([]byte("CREATE TABLE audit (id int);\n\nALTER TABLE Orders ADD note text;"))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Exec("m.cs", 7, st); len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
	audit := s.Tables()["dbo.audit"]
	if audit == nil || audit.Evidence[0] != (Ev{"m.cs", 7, 7}) || audit.Columns[0].Evidence[0].Line != 7 {
		t.Fatalf("audit: %+v", audit)
	}
	if o := s.Tables()["dbo.orders"]; len(o.Columns) != 1 || o.Columns[0].Name != "note" || o.Columns[0].Evidence[0].Line != 7 {
		t.Errorf("orders: %+v", o)
	}
	pg := NewSet(PostgreSQL, "")
	pg.Exec("m.cs", 1, st[:1])
	if pg.Tables()["public.audit"] == nil {
		t.Errorf("pg: %v", pg.Tables())
	}
}

// A SQL Server set reads [bracketed] names, keeps names as written, and
// finds tables and columns without regard to case, as SQL Server's default
// collation does. A shaping statement it cannot apply is reported unread.
func TestSetSQLServer(t *testing.T) {
	s := NewSet(SQLServer, "dbo")
	s.Tables()["dbo.Orders"] = &Table{Schema: "dbo", Name: "Orders", Columns: []*Column{{Name: "Id", Type: "int"}, {Name: "Memo", Type: "nvarchar(50)"}}}
	st, err := s.Split([]byte(`ALTER TABLE [dbo].[Orders] ADD [Note] nvarchar(max) NULL;
ALTER TABLE Orders ADD Note2 int NULL;
ALTER TABLE orders ALTER COLUMN memo nvarchar(200) NOT NULL;
CREATE TABLE [dbo].[Audit] ([Id] int NOT NULL, [When]] At] datetime2 NULL);
UPDATE Orders SET Note = N'x';`))
	if err != nil {
		t.Fatal(err)
	}
	p, unread := s.Exec("m.cs", 3, st)
	if len(p) != 0 || unread {
		t.Fatalf("problems %v, unread %v", p, unread)
	}
	o := s.Tables()["dbo.Orders"]
	var got []string
	for _, c := range o.Columns {
		got = append(got, c.Name+" "+c.Type+" "+strconv.FormatBool(c.NotNull))
	}
	if want := "Id int false|Memo nvarchar(200) true|Note nvarchar(max) false|Note2 int false"; strings.Join(got, "|") != want {
		t.Errorf("orders: %s, want %s", strings.Join(got, "|"), want)
	}
	a := s.Tables()["dbo.Audit"]
	if a == nil || len(a.Columns) != 2 || a.Columns[0].Name != "Id" || a.Columns[1].Name != "When] At" || !a.Columns[0].NotNull {
		t.Fatalf("audit: %+v (tables %v)", a, s.Tables())
	}
	for _, sql := range []string{
		"ALTER TABLE Missing ADD X int",
		"CREATE TABLE ([X] int)",
		"DROP TABLE Missing",
		"ALTER TABLE Orders DROP COLUMN Missing",
		"ALTER TABLE Orders ALTER COLUMN Missing int NULL",
		"EXEC sp_rename 'dbo.Orders.Note', 'Memo2', 'COLUMN'",
	} {
		st, err := s.Split([]byte(sql))
		if err != nil {
			t.Fatal(err)
		}
		if _, unread := s.Exec("m.cs", 9, st); !unread {
			t.Errorf("%s: not reported unread", sql)
		}
	}
	// Other dialects keep their folding: an unquoted name is lower case.
	pg := NewSet(PostgreSQL, "")
	pst, _ := pg.Split([]byte("CREATE TABLE Audit (Id int)"))
	pg.Exec("m.cs", 1, pst)
	if pg.Tables()["public.audit"] == nil {
		t.Errorf("pg: %v", pg.Tables())
	}
}

// T-SQL lists several columns after one ADD or DROP COLUMN; each piece after
// the first carries that action. A piece the replay cannot place is unread.
func TestSetSQLServerMultiColumn(t *testing.T) {
	names := func(tb *Table) string {
		var out []string
		for _, c := range tb.Columns {
			out = append(out, c.Name)
		}
		return strings.Join(out, " ")
	}
	run := func(sql string) (*Table, bool) {
		s := NewSet(SQLServer, "dbo")
		s.Tables()["dbo.Orders"] = &Table{Schema: "dbo", Name: "Orders", Columns: []*Column{{Name: "Id", Type: "int"}, {Name: "Memo", Type: "int"}, {Name: "Note", Type: "int"}}}
		st, err := s.Split([]byte(sql))
		if err != nil {
			t.Fatal(err)
		}
		_, unread := s.Exec("m.cs", 1, st)
		return s.Tables()["dbo.Orders"], unread
	}
	for _, c := range []struct{ sql, want string }{
		{"ALTER TABLE Orders ADD A int NULL, B int NULL", "Id Memo Note A B"},
		{"ALTER TABLE Orders ADD A int NULL, CONSTRAINT PK_O PRIMARY KEY (Id), B int", "Id Memo Note A B"},
		{"ALTER TABLE Orders DROP COLUMN Id, Memo", "Note"},
		{"ALTER TABLE Orders DROP CONSTRAINT PK_O, COLUMN Memo, Note", "Id"},
		{"ALTER TABLE Orders DROP PK_O, Other", "Id Memo Note"},
		{"ALTER TABLE Orders WITH CHECK ADD CONSTRAINT FK_O FOREIGN KEY (Id) REFERENCES X (Id)", "Id Memo Note"},
	} {
		tb, unread := run(c.sql)
		if got := names(tb); got != c.want || unread {
			t.Errorf("%s: columns %q unread %v, want %q", c.sql, got, unread, c.want)
		}
	}
	if _, unread := run("ALTER TABLE Orders SWITCH TO Archive"); !unread {
		t.Error("an action the replay does not know must be unread")
	}
}

func TestAddEvDedupesOnFileAndLine(t *testing.T) {
	evs := AddEv(nil, Ev{File: "a.sql", Line: 3, End: 3})
	evs = AddEv(evs, Ev{File: "a.sql", Line: 3, End: 7})
	evs = AddEv(evs, Ev{File: "a.sql", Line: 4})
	evs = AddEv(evs, Ev{File: "b.sql", Line: 3})
	if len(evs) != 3 {
		t.Errorf("AddEv kept %d entries, want 3: %v", len(evs), evs)
	}
}
