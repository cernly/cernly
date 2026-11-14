package sqlddl

import (
	"sort"
	"strings"
)

// Ev is a place in a migration file that shaped a table or column.
type Ev struct {
	File      string
	Line, End int
}

type Column struct {
	Name     string
	Type     string // as written, lower case, spacing normalised; a quoted type name kept as written
	NotNull  bool
	Primary  bool
	Unique   bool
	Enum     bool
	Evidence []Ev
}

type Table struct {
	Schema   string // "" when the set is MySQL-shaped
	Name     string
	RLS      bool
	Columns  []*Column
	Evidence []Ev
}

// Qualified is schema.table, or the table alone without a schema.
func (t *Table) Qualified() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

func (t *Table) column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (t *Table) drop(name string) {
	for i, c := range t.Columns {
		if c.Name == name {
			t.Columns = append(t.Columns[:i], t.Columns[i+1:]...)
			return
		}
	}
}

// Problem is a statement the replay could not read.
type Problem struct {
	File string
	Line int
	Msg  string
}

// File is one migration file's statements, in the order the set applies them.
type File struct {
	Path  string
	Stmts []Stmt
}

// Dialect is what a migration set was read as, and the first statement that
// says so: MySQL, PostgreSQL, or neither ("": table names stay as written, no
// row-level security).
type Dialect struct {
	Name   string // "mysql", "postgresql" or ""
	Signal string // what was seen, e.g. "CREATE EXTENSION"
	File   string
	Line   int
}

const (
	MySQL      = "mysql"
	PostgreSQL = "postgresql"
	SQLServer  = "sqlserver" // a Set only: the EF Core reader's migrationBuilder.Sql
)

// Detect reads a set's dialect from the SQL itself. MySQL: backticks, a
// CREATE TABLE … ENGINE= option, or lexed as MySQL by SplitSet. PostgreSQL:
// a schema-qualified table, CREATE EXTENSION, row-level security, a $$
// body, a SERIAL type or a :: cast. The first statement with a signal
// decides within each kind; MySQL wins over PostgreSQL.
func Detect(files []File) Dialect {
	var pg Dialect
	for _, f := range files {
		for _, st := range f.Stmts {
			switch {
			case st.backticked:
				return Dialect{MySQL, "backticks", f.Path, st.Line}
			case st.mysql:
				return Dialect{MySQL, "MySQL quoting or # comments", f.Path, st.Line}
			case engineOption(st.toks):
				return Dialect{MySQL, "ENGINE=", f.Path, st.Line}
			}
			if pg.Name == "" {
				if sig, line := pgSignal(st); sig != "" {
					pg = Dialect{PostgreSQL, sig, f.Path, line}
				}
			}
		}
	}
	return pg
}

// pgSignal is the first PostgreSQL-only construct in a statement.
func pgSignal(st Stmt) (string, int) {
	if st.dollarLine > 0 {
		return "$$ body", st.dollarLine
	}
	c := &cursor{toks: st.toks}
	switch {
	case c.kw("create", "extension"):
		return "CREATE EXTENSION", st.Line
	case c.kw("create", "table"), c.kw("alter", "table"), c.kw("create", "unlogged", "table"):
		c.kw("if", "not", "exists")
		c.kw("if", "exists")
		c.kw("only")
		if _, ok := c.ident(); ok && c.peek().kind == kPunct && c.peek().text == "." {
			return "schema-qualified table", st.Line
		}
	}
	for i, t := range st.toks {
		switch {
		case t.is("serial"), t.is("bigserial"), t.is("smallserial"):
			return "SERIAL type", t.line
		case t.kind == kOther && t.text == ":" && i+1 < len(st.toks) && st.toks[i+1].kind == kOther && st.toks[i+1].text == ":":
			return ":: cast", t.line
		case t.is("row") && i+2 < len(st.toks) && st.toks[i+1].is("level") && st.toks[i+2].is("security"):
			return "ROW LEVEL SECURITY", t.line
		}
	}
	return "", 0
}

// Replay applies a migration set's files in order and returns the tables
// they leave, sorted by name. hint is the dialect when the SQL itself has
// no signal (a Prisma migration_lock.toml); the SQL's own signal wins.
// Only a PostgreSQL set puts unqualified tables in public and says whether
// row-level security is on.
func Replay(files []File, hint Dialect) (tables []*Table, d Dialect, problems []Problem) {
	d = Detect(files)
	if d.Name == "" {
		d = hint
	}
	r := &replay{tables: map[string]*Table{}, enums: map[string]bool{}, mysql: d.Name == MySQL, pg: d.Name == PostgreSQL}
	for _, f := range files {
		for _, st := range f.Stmts {
			r.file = f.Path
			r.stmt(st)
		}
	}
	for _, t := range r.tables {
		for _, c := range t.Columns {
			c.Enum = r.enums[r.typeKey(c.Type)]
		}
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Qualified() < tables[j].Qualified() })
	return tables, d, r.problems
}

// engineOption reports a CREATE TABLE with an ENGINE = table option after
// its column list. A column or word named engine is not one.
func engineOption(toks []tok) bool {
	c := &cursor{toks: toks}
	if !c.kw("create") {
		return false
	}
	for !c.done() && !c.peek().is("table") {
		if c.peek().kind != kWord { // CREATE [TEMPORARY] TABLE only
			return false
		}
		c.i++
	}
	depth, closed := 0, false
	for ; c.i < len(toks); c.i++ {
		t := toks[c.i]
		switch {
		case t.kind == kPunct && t.text == "(":
			depth++
		case t.kind == kPunct && t.text == ")":
			depth--
			closed = closed || depth == 0
		case closed && depth == 0 && t.is("engine") && c.i+1 < len(toks) && toks[c.i+1].kind == kOther && toks[c.i+1].text == "=":
			return true
		}
	}
	return false
}

type replay struct {
	tables   map[string]*Table
	enums    map[string]bool
	mysql    bool
	pg       bool
	mssql    bool   // names match without regard to case
	unread   bool   // a shaping statement since the last Exec that changed nothing it names
	schema   string // default schema of unqualified names outside PostgreSQL (Set)
	file     string
	problems []Problem
}

func (r *replay) problem(line int, msg string) {
	r.problems = append(r.problems, Problem{r.file, line, msg})
}

// cursor walks a statement's tokens.
type cursor struct {
	toks []tok
	i    int
}

func (c *cursor) done() bool { return c.i >= len(c.toks) }

func (c *cursor) peek() tok {
	if c.done() {
		return tok{}
	}
	return c.toks[c.i]
}

// kw consumes the keywords ws in sequence if they are next.
func (c *cursor) kw(ws ...string) bool {
	if c.i+len(ws) > len(c.toks) {
		return false
	}
	for k, w := range ws {
		if !c.toks[c.i+k].is(w) {
			return false
		}
	}
	c.i += len(ws)
	return true
}

// ident reads one identifier: an unquoted word folds to lower case, a
// quoted one is kept as written.
func (c *cursor) ident() (string, bool) {
	t := c.peek()
	switch t.kind {
	case kWord:
		c.i++
		if t.asIs {
			return t.text, true
		}
		return strings.ToLower(t.text), true
	case kQuoted:
		c.i++
		return t.text, true
	}
	return "", false
}

// name reads a possibly qualified name (db.schema.table) and returns its
// last two parts.
func (c *cursor) name() (schema, name string, ok bool) {
	var parts []string
	for {
		p, ok := c.ident()
		if !ok {
			break
		}
		parts = append(parts, p)
		if c.peek().kind == kPunct && c.peek().text == "." {
			c.i++
			continue
		}
		break
	}
	switch len(parts) {
	case 0:
		return "", "", false
	case 1:
		return "", parts[0], true
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

// key resolves a table name in the set: unqualified names are in public in
// a PostgreSQL set; a MySQL set's tables have no schema; in a set of no
// known dialect names stay as written.
func (r *replay) key(schema, name string) (string, string) {
	if r.mysql {
		return "", name
	}
	if schema == "" && r.pg {
		schema = "public"
	}
	if schema == "" {
		schema = r.schema
	}
	return schema, name
}

// Set is a replay that a caller drives statement by statement and whose
// tables it changes in between: the EF Core reader replays
// migrationBuilder.Sql literals into the tables its own operations built.
type Set struct{ r *replay }

// NewSet starts an empty set read as dialect (MySQL, PostgreSQL, SQLServer
// or ""). defaultSchema qualifies unqualified names outside PostgreSQL (dbo
// for SQL Server); a MySQL set has no schemas. A SQLServer set finds tables
// and columns without regard to case, as SQL Server's default collation does,
// when exactly one matches so.
func NewSet(dialect, defaultSchema string) *Set {
	return &Set{&replay{tables: map[string]*Table{}, enums: map[string]bool{},
		mysql: dialect == MySQL, pg: dialect == PostgreSQL, mssql: dialect == SQLServer, schema: defaultSchema}}
}

// Split lexes SQL for this set: T-SQL for a SQLServer set ([bracketed] names,
// unquoted names as written), otherwise as SplitSet lexes one file.
func (s *Set) Split(src []byte) ([]Stmt, error) {
	if s.r.mssql {
		return lex(src, false, true)
	}
	stmts, errs := SplitSet([][]byte{src})
	return stmts[0], errs[0]
}

// Tables are the set's tables by Qualified name. The caller may add, change
// and remove them, keeping each under its Qualified name.
func (s *Set) Tables() map[string]*Table { return s.r.tables }

// Exec applies stmts from file with every statement placed at line: SQL held
// in a string of another language has no lines of its own. unread is true
// when a statement that shapes tables could not be read, or names a table or
// column the set does not hold, so it changed nothing.
func (s *Set) Exec(file string, line int, stmts []Stmt) (problems []Problem, unread bool) {
	s.r.file, s.r.problems, s.r.unread = file, nil, false
	for _, st := range stmts {
		st.Line, st.End = line, line
		toks := make([]tok, len(st.toks))
		for i, t := range st.toks {
			t.line = line
			toks[i] = t
		}
		st.toks = toks
		s.r.stmt(st)
	}
	return s.r.problems, s.r.unread || len(s.r.problems) > 0
}

// lookup finds the table a name resolves to: its key, and the table when the
// set holds it.
func (r *replay) lookup(schema, name string) (string, *Table) {
	s, n := r.key(schema, name)
	k := qual(s, n)
	if t := r.tables[k]; t != nil || !r.mssql {
		return k, t
	}
	hit := ""
	for tk := range r.tables {
		if strings.EqualFold(tk, k) {
			if hit != "" {
				return k, nil // two match: none is the one
			}
			hit = tk
		}
	}
	if hit == "" {
		return k, nil
	}
	return hit, r.tables[hit]
}

// col finds a column of t by name; in a SQLServer set without regard to case
// when exactly one matches so.
func (r *replay) col(t *Table, name string) *Column {
	if c := t.column(name); c != nil || !r.mssql {
		return c
	}
	var hit *Column
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			if hit != nil {
				return nil
			}
			hit = c
		}
	}
	return hit
}

func (r *replay) dropCol(t *Table, name string) {
	if c := r.col(t, name); c != nil {
		t.drop(c.Name)
	}
}

func qual(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// typeKey is the enum lookup key of a column type: a bare or qualified name.
func (r *replay) typeKey(typ string) string {
	t := strings.TrimSuffix(typ, "[]")
	if strings.ContainsAny(t, " ()") {
		return ""
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t
	}
	s, n := r.key("", t)
	return qual(s, n)
}

// split cuts tokens at commas outside parentheses and brackets.
func split(toks []tok) [][]tok {
	var out [][]tok
	depth, start := 0, 0
	for i, t := range toks {
		if t.kind != kPunct {
			continue
		}
		switch t.text {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// paren returns the tokens inside the parenthesis that opens at toks[i] and
// the index after its close, or ok false when it does not close.
func paren(toks []tok, i int) (inner []tok, next int, ok bool) {
	depth := 0
	for j := i; j < len(toks); j++ {
		if toks[j].kind != kPunct {
			continue
		}
		switch toks[j].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return toks[i+1 : j], j + 1, true
			}
		}
	}
	return nil, 0, false
}

func (r *replay) stmt(st Stmt) {
	c := &cursor{toks: st.toks}
	ev := Ev{r.file, st.Line, st.End}
	switch {
	case c.kw("create"):
		c.kw("or", "replace")
		if c.kw("type") {
			r.createType(c)
			return
		}
		for c.kw("global") || c.kw("local") {
		}
		if c.kw("temporary") || c.kw("temp") {
			return
		}
		c.kw("unlogged")
		if c.kw("table") {
			r.createTable(c, ev)
		}
	case c.kw("alter", "table"):
		r.alterTable(c, ev)
	case c.kw("drop", "table"):
		exists := c.kw("if", "exists")
		for _, part := range split(c.toks[c.i:]) {
			pc := &cursor{toks: part}
			s, n, ok := pc.name()
			if !ok {
				r.unread = true
				continue
			}
			k, t := r.lookup(s, n)
			if t == nil && !exists {
				r.unread = true
			}
			delete(r.tables, k)
		}
	case c.kw("drop", "type"):
		c.kw("if", "exists")
		for _, part := range split(c.toks[c.i:]) {
			pc := &cursor{toks: part}
			if s, n, ok := pc.name(); ok {
				s, n = r.key(s, n)
				delete(r.enums, qual(s, n))
			}
		}
	case c.kw("rename", "table"):
		// MySQL: RENAME TABLE a TO b, c TO d
		for _, part := range split(c.toks[c.i:]) {
			pc := &cursor{toks: part}
			s, n, ok := pc.name()
			if !ok || !pc.kw("to") {
				continue
			}
			ns, nn, ok := pc.name()
			if ok {
				r.rename(s, n, ns, nn, ev)
			}
		}
	case r.mssql && (c.kw("exec") || c.kw("execute")):
		// sp_rename renames a table or a column; the replay does not run it.
		if _, n, ok := c.name(); ok && strings.EqualFold(n, "sp_rename") {
			r.unread = true
		}
	}
}

func (r *replay) createType(c *cursor) {
	s, n, ok := c.name()
	if ok && c.kw("as", "enum") {
		s, n = r.key(s, n)
		r.enums[qual(s, n)] = true
	}
}

func (r *replay) createTable(c *cursor, ev Ev) {
	exists := c.kw("if", "not", "exists")
	s, n, ok := c.name()
	if !ok {
		r.problem(ev.Line, "CREATE TABLE without a name")
		return
	}
	if _, held := r.lookup(s, n); exists && held != nil {
		return
	}
	s, n = r.key(s, n)
	k := qual(s, n)
	t := &Table{Schema: s, Name: n, Evidence: []Ev{ev}}
	r.tables[k] = t
	if c.peek().kind != kPunct || c.peek().text != "(" {
		return // AS SELECT, PARTITION OF, OF type: no columns of its own here
	}
	inner, _, ok := paren(c.toks, c.i)
	if !ok {
		r.problem(ev.Line, "CREATE TABLE "+k+": unbalanced parentheses")
		return
	}
	for _, el := range split(inner) {
		if len(el) == 0 {
			continue
		}
		if r.constraint(t, el, ev) {
			continue
		}
		if el[0].is("like") {
			continue
		}
		if col := r.column(el); col != nil {
			t.drop(col.Name)
			t.Columns = append(t.Columns, col)
		}
	}
}

// constraint applies a table constraint (PRIMARY KEY (…), UNIQUE (…)) and
// reports whether the element was one, of any kind.
func (r *replay) constraint(t *Table, el []tok, ev Ev) bool {
	if len(el) == 0 {
		return false // ALTER TABLE t ADD with nothing after it
	}
	c := &cursor{toks: el}
	if c.kw("constraint") {
		c.ident()
	}
	switch {
	case c.kw("primary", "key"):
		for _, name := range r.columnList(c) {
			if col := t.column(name); col != nil {
				col.Primary, col.NotNull = true, true
				col.Evidence = AddEv(col.Evidence, ev)
			}
		}
		return true
	case c.kw("unique"):
		c.kw("nulls", "not", "distinct")
		c.kw("nulls", "distinct")
		if c.kw("key") || c.kw("index") {
			if c.peek().kind != kPunct {
				c.ident()
			}
		}
		if names := r.columnList(c); len(names) == 1 {
			if col := t.column(names[0]); col != nil {
				col.Unique = true
				col.Evidence = AddEv(col.Evidence, ev)
			}
		}
		return true
	}
	first := el[0]
	if c.i > 0 || first.is("foreign") || first.is("check") || first.is("exclude") {
		return true
	}
	// MySQL's KEY / INDEX / FULLTEXT / SPATIAL: reserved words there, but key
	// and index are fine column names in PostgreSQL.
	if r.mysql && (first.is("key") || first.is("index") || first.is("fulltext") || first.is("spatial")) {
		return true
	}
	return first.is("period") && len(el) > 1 && el[1].is("for")
}

func (r *replay) columnList(c *cursor) []string {
	if c.peek().kind != kPunct || c.peek().text != "(" {
		return nil
	}
	inner, next, ok := paren(c.toks, c.i)
	if !ok {
		return nil
	}
	c.i = next
	var out []string
	for _, part := range split(inner) {
		pc := &cursor{toks: part}
		if n, ok := pc.ident(); ok {
			out = append(out, n)
		}
	}
	return out
}

// stops are the words that end a column's type and start its constraints.
var stops = map[string]bool{
	"constraint": true, "not": true, "null": true, "default": true, "primary": true, "unique": true, "references": true,
	"check": true, "generated": true, "collate": true, "auto_increment": true, "comment": true, "on": true,
	"identity": true, "storage": true, "compression": true,
}

// column reads a column definition: a name, its type, its constraints.
func (r *replay) column(el []tok) *Column {
	c := &cursor{toks: el}
	name, ok := c.ident()
	if !ok {
		return nil
	}
	col := &Column{Name: name, Evidence: []Ev{{r.file, el[0].line, 0}}}
	start := c.i
	depth := 0
	for !c.done() {
		t := c.peek()
		if t.kind == kPunct && (t.text == "(" || t.text == "[") {
			depth++
		} else if t.kind == kPunct && (t.text == ")" || t.text == "]") {
			depth--
		} else if depth == 0 && t.kind == kWord {
			w := strings.ToLower(t.text)
			if stops[w] || (w == "character" || w == "charset") && c.i+1 < len(el) && el[c.i+1].is("set") || w == "charset" {
				break
			}
		}
		c.i++
	}
	col.Type = typeText(el[start:c.i])
	if col.Type == "" {
		return nil
	}
	switch strings.TrimSuffix(col.Type, "[]") {
	case "serial", "bigserial", "smallserial", "serial4", "serial8", "serial2":
		col.NotNull = true
	}
	for !c.done() {
		switch {
		case c.kw("not", "null"):
			col.NotNull = true
		case c.kw("null"):
			col.NotNull = false
		case c.kw("primary", "key"):
			col.Primary, col.NotNull = true, true
		case c.kw("unique"):
			col.Unique = true
		default:
			c.i++
		}
	}
	return col
}

// typeText writes a type's tokens as one string: words in lower case,
// quoted names as written, no space around brackets, one after a comma.
func typeText(toks []tok) string {
	var b strings.Builder
	for i, t := range toks {
		text := t.text
		switch t.kind {
		case kWord:
			text = strings.ToLower(text)
		case kString:
			text = "'" + text + "'"
		}
		if i > 0 {
			prev := toks[i-1]
			glued := t.kind == kPunct || prev.kind == kPunct && (prev.text == "(" || prev.text == "[" || prev.text == ".")
			if !glued {
				b.WriteByte(' ')
			}
		}
		b.WriteString(text)
	}
	return b.String()
}

// AddEv appends ev unless evs already holds one at the same file and line.
func AddEv(evs []Ev, ev Ev) []Ev {
	for _, e := range evs {
		if e.File == ev.File && e.Line == ev.Line {
			return evs
		}
	}
	return append(evs, ev)
}

func (r *replay) rename(s, n, ns, nn string, ev Ev) {
	k, t := r.lookup(s, n)
	if t == nil {
		r.unread = true
		return
	}
	if ns == "" {
		ns = t.Schema // RENAME TO keeps the schema
	}
	ns, nn = r.key(ns, nn)
	delete(r.tables, k)
	t.Schema, t.Name = ns, nn
	t.Evidence = AddEv(t.Evidence, ev)
	r.tables[qual(ns, nn)] = t
}

func (r *replay) alterTable(c *cursor, ev Ev) {
	c.kw("if", "exists")
	c.kw("only")
	s, n, ok := c.name()
	if !ok {
		r.unread = true
		return
	}
	if c.peek().kind == kOther && c.peek().text == "*" {
		c.i++
	}
	_, t := r.lookup(s, n)
	if t == nil {
		r.unread = true // a table the set never creates: a baseline outside the repo
		return
	}
	if !r.mssql {
		for _, action := range split(c.toks[c.i:]) {
			r.alterAction(t, action, ev)
		}
		return
	}
	// T-SQL: WITH CHECK | NOCHECK before ADD CONSTRAINT changes no shape.
	_ = c.kw("with", "check") || c.kw("with", "nocheck")
	// One ADD or DROP lists several items: `ADD a int, b int`, `DROP COLUMN
	// a, b`, `DROP CONSTRAINT x, COLUMN y, z`. A piece without an action of
	// its own carries the one before it.
	var carry []tok
	for _, action := range split(c.toks[c.i:]) {
		if len(action) == 0 {
			continue
		}
		first := action[0]
		switch {
		case first.is("add"):
			carry = action[:1]
		case first.is("drop"):
			carry = action[:1:1]
			if len(action) > 1 && (action[1].is("column") || action[1].is("constraint")) {
				carry = action[:2:2]
			} else { // DROP [IF EXISTS] name: a constraint, T-SQL's default
				carry = append(carry, tok{kind: kWord, text: "constraint", line: first.line})
				action = append(append([]tok{}, carry...), action[1:]...)
			}
		case len(carry) > 0 && carry[0].is("drop") && (first.is("column") || first.is("constraint")):
			carry = []tok{carry[0], first}
			action = append([]tok{carry[0]}, action...)
		case len(carry) > 0:
			action = append(append([]tok{}, carry...), action...)
		}
		r.alterAction(t, action, ev)
	}
}

func (r *replay) alterAction(t *Table, action []tok, ev Ev) {
	if len(action) == 0 {
		return
	}
	c := &cursor{toks: action}
	at := Ev{r.file, action[0].line, 0}
	switch {
	case c.kw("rename", "to"):
		ns, nn, ok := c.name()
		if ok {
			if ns == "" {
				ns = t.Schema
			}
			r.rename(t.Schema, t.Name, ns, nn, ev)
		}
	case c.kw("set", "schema"):
		if ns, ok := c.ident(); ok {
			r.rename(t.Schema, t.Name, ns, t.Name, ev)
		}
	case c.kw("rename", "constraint"):
	case c.kw("rename"):
		c.kw("column")
		old, ok := c.ident()
		if !ok || !c.kw("to") {
			r.unread = true
			return
		}
		nw, ok := c.ident()
		if col := r.col(t, old); ok && col != nil {
			col.Name = nw
			col.Evidence = AddEv(col.Evidence, at)
			t.Evidence = AddEv(t.Evidence, ev)
		} else {
			r.unread = true
		}
	case c.kw("add"):
		if r.constraint(t, action[c.i:], at) {
			t.Evidence = AddEv(t.Evidence, ev)
			return
		}
		c.kw("column")
		exists := c.kw("if", "not", "exists")
		col := r.column(action[c.i:])
		if col == nil {
			r.unread = true
			return
		}
		if exists && r.col(t, col.Name) != nil {
			return
		}
		r.dropCol(t, col.Name)
		t.Columns = append(t.Columns, col)
		t.Evidence = AddEv(t.Evidence, ev)
	case c.kw("drop", "constraint"), c.kw("drop", "index"), c.kw("drop", "key"), c.kw("drop", "foreign"):
	case c.kw("drop", "primary", "key"):
		for _, col := range t.Columns {
			col.Primary = false
		}
		t.Evidence = AddEv(t.Evidence, ev)
	case c.kw("drop"):
		c.kw("column")
		exists := c.kw("if", "exists")
		if name, ok := c.ident(); ok && r.col(t, name) != nil {
			r.dropCol(t, name)
			t.Evidence = AddEv(t.Evidence, ev)
		} else if !exists {
			r.unread = true
		}
	case c.kw("alter"):
		c.kw("column")
		at0 := c.i
		name, ok := c.ident()
		col := r.col(t, name)
		if !ok || col == nil {
			r.unread = true
			return
		}
		changed := true
		switch {
		case c.kw("type"), c.kw("set", "data", "type"):
			end := c.i
			for end < len(action) && !action[end].is("using") && !action[end].is("collate") {
				end++
			}
			col.Type = typeText(action[c.i:end])
		case c.kw("set", "not", "null"):
			col.NotNull = true
		case c.kw("drop", "not", "null"):
			col.NotNull = false
		case r.mssql && !c.done() && !c.peek().is("add") && !c.peek().is("drop"):
			// T-SQL: ALTER COLUMN name type [NULL | NOT NULL]; without either
			// the column allows nulls.
			def := r.column(action[at0:])
			if def == nil {
				r.unread = true
				return
			}
			col.Type, col.NotNull = def.Type, def.NotNull || col.Primary
		default:
			changed = false // defaults, statistics, storage: not a column's shape
		}
		if changed {
			col.Evidence = AddEv(col.Evidence, at)
			t.Evidence = AddEv(t.Evidence, ev)
		}
	case c.kw("modify"):
		// MySQL: MODIFY [COLUMN] definition
		c.kw("column")
		if col := r.column(action[c.i:]); col != nil && t.column(col.Name) != nil {
			r.replace(t, col.Name, col, ev)
		}
	case c.kw("change"):
		// MySQL: CHANGE [COLUMN] old definition
		c.kw("column")
		old, ok := c.ident()
		if col := r.column(action[c.i:]); ok && col != nil && t.column(old) != nil {
			r.replace(t, old, col, ev)
		}
	case c.kw("enable", "row", "level", "security"):
		t.RLS = true
		t.Evidence = AddEv(t.Evidence, ev)
	case c.kw("disable", "row", "level", "security"):
		t.RLS = false
		t.Evidence = AddEv(t.Evidence, ev)
	case r.mssql && (c.kw("check", "constraint") || c.kw("nocheck", "constraint")):
		// T-SQL: enables or disables a constraint; no shape
	default:
		if r.mssql {
			r.unread = true // an action the replay does not know may change the shape
		}
	}
}

// replace puts a redefined column where the old one was.
func (r *replay) replace(t *Table, old string, col *Column, ev Ev) {
	for i, c := range t.Columns {
		if c.Name == old {
			// MySQL keeps the column's indexes: a key stays a key
			col.Primary, col.Unique = col.Primary || c.Primary, col.Unique || c.Unique
			col.NotNull = col.NotNull || c.Primary
			col.Evidence = append(c.Evidence, col.Evidence...)
			t.Columns[i] = col
			t.Evidence = AddEv(t.Evidence, ev)
			return
		}
	}
}

// Shapes reports whether a file's statements shape tables: CREATE TABLE (not
// temporary), ALTER, DROP or RENAME TABLE, or CREATE TYPE … AS ENUM. A .sql
// file without any is a query, a seed or a function, not a migration.
func Shapes(stmts []Stmt) bool {
	for _, st := range stmts {
		c := &cursor{toks: st.toks}
		switch {
		case c.kw("alter", "table"), c.kw("drop", "table"), c.kw("rename", "table"):
			return true
		case c.kw("create"):
			c.kw("or", "replace")
			if c.kw("type") {
				c.name()
				if c.kw("as", "enum") {
					return true
				}
				continue
			}
			for c.kw("global") || c.kw("local") {
			}
			if c.kw("temporary") || c.kw("temp") {
				continue
			}
			c.kw("unlogged")
			if c.kw("table") {
				return true
			}
		}
	}
	return false
}
