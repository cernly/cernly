package dotnet

// EF Core migrations as tables: every class deriving from
// Migration, with its [Migration("<id>")] and [DbContext(typeof(X))] from
// the .Designer.cs partial, is replayed per (folder, context) in id order,
// Up only, in memory. The model snapshot and the Designer's model code are
// never replayed: only the body of Up is read. migrationBuilder.Sql literals
// go through sqlddl, the SQL reader of migrations-sql.

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/sqlddl"
	"github.com/cernly/cernly/plugin"
)

// efProviders are the EF provider packages (lower case): the provider's name
// and the schema of a table that names none.
var efProviders = map[string]struct{ name, schema string }{
	"microsoft.entityframeworkcore.sqlserver": {"sqlserver", "dbo"},
	"npgsql.entityframeworkcore.postgresql":   {"postgresql", "public"},
	"pomelo.entityframeworkcore.mysql":        {"mysql", ""},
}

var efDialect = map[string]string{"postgresql": sqlddl.PostgreSQL, "mysql": sqlddl.MySQL, "sqlserver": sqlddl.SQLServer}

// efSigs are the parameters of the migrationBuilder operations that shape
// tables, in EF's declaration order, for positional arguments.
var efSigs = map[string][]string{
	"CreateTable":    {"name", "columns", "schema", "constraints", "comment"},
	"DropTable":      {"name", "schema"},
	"RenameTable":    {"name", "schema", "newName", "newSchema"},
	"AddColumn":      {"name", "table", "type", "unicode", "maxLength", "rowVersion", "schema", "nullable"},
	"AlterColumn":    {"name", "table", "type", "unicode", "maxLength", "rowVersion", "schema", "nullable"},
	"DropColumn":     {"name", "table", "schema"},
	"RenameColumn":   {"name", "table", "newName", "schema"},
	"AddPrimaryKey":  {"name", "table", "columns", "schema"},
	"DropPrimaryKey": {"name", "table", "schema"},
	"Sql":            {"sql", "suppressTransaction"},
	"EnsureSchema":   {"name"},
}

var (
	efColumnSig = []string{"type", "unicode", "maxLength", "rowVersion", "name", "nullable"}
	efPKSig     = []string{"name", "columns"}
)

// efNoShape are operations that leave tables and columns as they are.
var efNoShape = map[string]bool{
	"EnsureSchema": true, "DropSchema": true, "CreateIndex": true, "DropIndex": true, "RenameIndex": true,
	"AddForeignKey": true, "DropForeignKey": true, "AddUniqueConstraint": true, "DropUniqueConstraint": true,
	"AddCheckConstraint": true, "DropCheckConstraint": true, "CreateSequence": true, "DropSequence": true,
	"AlterSequence": true, "RenameSequence": true, "RestartSequence": true, "InsertData": true,
	"UpdateData": true, "DeleteData": true, "AlterDatabase": true, "AlterTable": true,
}

// efClass is one C# class, merged over its partial declarations.
type efClass struct {
	name      string
	migration bool // derives from Migration
	abstract  bool
	id        string
	hasID     bool
	context   string
	file      string // the file whose declaration holds Up
	line      int
	param     string  // Up's MigrationBuilder parameter
	up        []token // Up's body
	consts    map[string]string
}

// efTables reads the EF Core migrations of every project.
func efTables(s *state, res *plugin.Result) {
	classes := map[string]*efClass{}
	var order []string
	for _, dir := range slices.Sorted(maps.Keys(s.sources)) {
		for _, f := range s.sources[dir] {
			if strings.ToLower(path.Ext(f)) != ".cs" {
				continue
			}
			toks, err := s.tokens(f)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
				continue
			}
			consts := constStrings(toks)
			for _, d := range classDecls(toks) {
				key := dir + "|" + d.ns + "|" + d.name
				c := classes[key]
				if c == nil {
					c = &efClass{name: d.name}
					classes[key] = c
					order = append(order, key)
				}
				c.migration = c.migration || d.migration
				c.abstract = c.abstract || d.abstract
				if d.hasID {
					c.id, c.hasID = d.id, true
				}
				if d.context != "" {
					c.context = d.context
				}
				if d.up != nil && c.up == nil {
					c.file, c.line, c.param, c.up, c.consts = f, d.line, d.param, d.up, consts
				}
			}
		}
	}
	type group struct {
		folder, context, dir string
		migs                 []*efClass
	}
	groups := map[string]*group{}
	for _, key := range order {
		c := classes[key]
		if !c.migration || c.abstract || c.up == nil {
			continue
		}
		if !c.hasID {
			res.Errors = append(res.Errors, fact.LocationError{Path: c.file, Line: c.line,
				Message: "efcore: migration " + c.name + " has no [Migration(\"<id>\")] attribute in its partials; skipped"})
			continue
		}
		folder := path.Dir(c.file)
		gk := folder + "|" + c.context
		g := groups[gk]
		if g == nil {
			g = &group{folder: folder, context: c.context, dir: strings.SplitN(key, "|", 2)[0]}
			groups[gk] = g
		}
		g.migs = append(g.migs, c)
	}
	gks := make([]string, 0, len(groups))
	for k := range groups {
		gks = append(gks, k)
	}
	sort.Strings(gks)
	for _, gk := range gks {
		g := groups[gk]
		sort.SliceStable(g.migs, func(i, j int) bool {
			if g.migs[i].id != g.migs[j].id {
				return g.migs[i].id < g.migs[j].id
			}
			return g.migs[i].file < g.migs[j].file
		})
		prov, provEv, known := s.efProvider(g.dir)
		r := &efReplay{set: sqlddl.NewSet(efDialect[prov.name], prov.schema), schema: prov.schema,
			dialect: efDialect[prov.name], clr: map[*sqlddl.Column]string{}, maxLen: map[*sqlddl.Column]int{}}
		for _, m := range g.migs {
			r.migration(m)
		}
		res.Errors = append(res.Errors, r.errs...)
		ext := map[string]any{}
		if g.context != "" {
			ext["context"] = g.context
		}
		if known {
			ext["provider"] = prov.name
			ext["provider_at"] = fmt.Sprintf("%s:%d", provEv.Path, provEv.Line)
		} else {
			ext["provider_unknown"] = true
		}
		if n := len(r.unread); n > 0 {
			ext["unread_operations"] = n
			at := make([]any, 0, n)
			for i, e := range r.unread {
				if i == maxEvidence {
					break
				}
				at = append(at, fmt.Sprintf("%s:%d", e.Path, e.Line))
			}
			ext["unread_at"] = at
		}
		res.Facts = append(res.Facts, r.facts(g.folder, g.context, g.dir, ext)...)
	}
}

// efProvider is the EF provider among the packages of the projects in dir and
// of every project they reference, transitively. known is false for none, or
// for more than one.
func (s *state) efProvider(dir string) (prov struct{ name, schema string }, ev fact.Evidence, known bool) {
	seen := map[string]bool{}
	var queue []string
	for _, p := range s.projects {
		if p.Dir == dir {
			queue = append(queue, p.Path)
		}
	}
	found := map[string]fact.Evidence{}
	for len(queue) > 0 {
		pp := queue[0]
		queue = queue[1:]
		p := s.byPath[pp]
		if p == nil || seen[pp] {
			continue
		}
		seen[pp] = true
		for _, pr := range p.Packages {
			if v, ok := efProviders[strings.ToLower(pr.ID)]; ok {
				if _, dup := found[v.name]; !dup {
					e := fact.Evidence{Path: p.Path}
					if len(pr.Ev) > 0 {
						e = pr.Ev[0]
					}
					found[v.name] = e
					prov = v
				}
			}
		}
		for _, ref := range p.ProjectRefs {
			if ref.Path != "" {
				queue = append(queue, ref.Path)
			}
		}
	}
	if len(found) != 1 {
		return struct{ name, schema string }{}, fact.Evidence{}, false
	}
	return prov, found[prov.name], true
}

// classDecl is one declaration of a class in one file.
type classDecl struct {
	ns, name            string
	line                int
	migration, abstract bool
	id                  string
	hasID               bool
	context             string
	param               string
	up                  []token
}

// classDecls finds the class declarations of a file with the Migration and
// DbContext attributes placed on them and the body of their Up method.
func classDecls(toks []token) []classDecl {
	var out []classDecl
	var id, context string
	hasID := false
	ns := ""
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case isID(t, "namespace"):
			name, j := dotted(toks, i+1)
			ns, i = name, j-1
		case isP(t, "[") && (i == 0 || isP(toks[i-1], ";") || isP(toks[i-1], "{") || isP(toks[i-1], "}") || isP(toks[i-1], "]")):
			end := closing(toks, i, "[", "]")
			if end < 0 {
				continue // never closes: no attribute
			}
			for _, a := range splitTop(toks[i+1 : end]) {
				aid, actx, ok := attribute(a)
				if aid != nil {
					id, hasID = *aid, true
				}
				if ok {
					context = actx
				}
			}
			i = end
		case isID(t, "class") && i+1 < len(toks) && toks[i+1].kind == tokIdent:
			d := classDecl{ns: ns, name: toks[i+1].text, line: toks[i+1].line, id: id, hasID: hasID, context: context}
			for j := i - 1; j >= 0 && toks[j].kind == tokIdent; j-- {
				if toks[j].text == "abstract" {
					d.abstract = true
				}
			}
			j := i + 2
			if j < len(toks) && isP(toks[j], "<") {
				if j = closing(toks, j, "<", ">") + 1; j == 0 {
					continue // the type parameters never close: no class
				}
			}
			if j < len(toks) && isP(toks[j], ":") {
				for j++; j < len(toks) && !isP(toks[j], "{") && !isID(toks[j], "where"); j++ {
					if isID(toks[j], "Migration") && (j+1 >= len(toks) || !isP(toks[j+1], ".")) {
						d.migration = true
					}
				}
			}
			for j < len(toks) && !isP(toks[j], "{") {
				j++
			}
			if j >= len(toks) {
				continue
			}
			end := closing(toks, j, "{", "}")
			if end < 0 {
				continue // the body never closes: no class
			}
			d.param, d.up = upMethod(toks[j+1 : end])
			out = append(out, d)
			id, hasID, context = "", false, ""
		case isP(t, ";") || isP(t, "{") || isP(t, "}"):
			id, hasID, context = "", false, ""
		}
	}
	return out
}

// attribute reads [Migration("<id>")] and [DbContext(typeof(X))]; X is kept
// as written.
func attribute(a []token) (id *string, context string, isContext bool) {
	if len(a) > 2 && isP(a[1], ":") {
		return nil, "", false // assembly:, return: …
	}
	name, j := dotted(a, 0)
	if k := strings.LastIndexByte(name, '.'); k >= 0 {
		name = name[k+1:]
	}
	name = strings.TrimSuffix(name, "Attribute")
	if j >= len(a) || !isP(a[j], "(") {
		return nil, "", false
	}
	end := closing(a, j, "(", ")")
	if end < 0 {
		return nil, "", false
	}
	args := splitTop(a[j+1 : end])
	if len(args) == 0 {
		return nil, "", false
	}
	switch name {
	case "Migration":
		if len(args[0]) == 1 && args[0][0].kind == tokString && args[0][0].literal {
			v := args[0][0].text
			return &v, "", false
		}
	case "DbContext":
		x := args[0]
		if e := closing(x, 1, "(", ")"); len(x) > 3 && isID(x[0], "typeof") && e > 0 {
			var b strings.Builder
			for _, t := range x[2:e] {
				b.WriteString(t.text)
			}
			return nil, b.String(), true
		}
	}
	return nil, "", false
}

// upMethod finds `void Up(MigrationBuilder p) { … }` among a class body's
// members and returns p and the body's tokens.
func upMethod(body []token) (string, []token) {
	depth := 0
	for i := 0; i < len(body); i++ {
		t := body[i]
		switch {
		case isP(t, "{"):
			depth++
		case isP(t, "}"):
			depth--
		case depth == 0 && isID(t, "Up") && i > 0 && isID(body[i-1], "void") && i+1 < len(body) && isP(body[i+1], "("):
			end := closing(body, i+1, "(", ")")
			if end < 0 {
				continue
			}
			param := ""
			ps := body[i+2 : end]
			for k := 0; k+1 < len(ps); k++ {
				if ps[k].kind == tokIdent && strings.HasSuffix(ps[k].text, "MigrationBuilder") && ps[k+1].kind == tokIdent {
					param = ps[k+1].text
				}
			}
			if param == "" || end+1 >= len(body) || !isP(body[end+1], "{") {
				continue
			}
			close := closing(body, end+1, "{", "}")
			if close < 0 {
				continue
			}
			return param, body[end+2 : close]
		}
	}
	return "", nil
}

// efArgs maps a call's arguments to parameter names: positional ones by sig,
// then named ones (`name: value`) by their name.
func efArgs(toks []token, sig []string) map[string][]token {
	out := map[string][]token{}
	for i, a := range splitTop(toks) {
		if len(a) > 2 && a[0].kind == tokIdent && isP(a[1], ":") && !isP(a[2], ":") {
			out[a[0].text] = a[2:]
		} else if i < len(sig) {
			out[sig[i]] = a
		}
	}
	return out
}

// efCall is one migrationBuilder.<Method>[<T>](…) in Up.
type efCall struct {
	method  string
	generic string
	args    []token
	line    int
}

type efReplay struct {
	set     *sqlddl.Set
	schema  string // the provider's default
	dialect string
	clr     map[*sqlddl.Column]string
	maxLen  map[*sqlddl.Column]int
	unread  []fact.Evidence
	errs    []fact.LocationError
	file    string
	consts  map[string]string
}

func (r *efReplay) migration(m *efClass) {
	r.file, r.consts = m.file, m.consts
	b := m.up
	for i := 0; i < len(b); i++ {
		if !isID(b[i], m.param) {
			continue
		}
		if i > 0 && isP(b[i-1], ".") {
			continue // x.migrationBuilder: another object's member
		}
		if i+2 >= len(b) || !isP(b[i+1], ".") || b[i+2].kind != tokIdent {
			r.unreadAt(b[i].line) // the builder handed elsewhere
			continue
		}
		c := efCall{method: b[i+2].text, line: b[i].line}
		j := i + 3
		if j < len(b) && isP(b[j], "<") {
			end := closing(b, j, "<", ">")
			if end < 0 {
				r.unreadAt(c.line) // never closes
				i = j
				continue
			}
			c.generic = joinText(b[j+1 : end])
			j = end + 1
		}
		if j >= len(b) || !isP(b[j], "(") {
			i = j - 1 // a property, such as ActiveProvider
			continue
		}
		end := closing(b, j, "(", ")")
		if end < 0 {
			r.unreadAt(c.line) // never closes
			i = j
			continue
		}
		c.args = b[j+1 : end]
		i = end
		r.call(c)
	}
}

func (r *efReplay) unreadAt(line int) {
	r.unread = append(r.unread, fact.Evidence{Path: r.file, Line: line})
}

// str reads a string argument: a literal, a const string of the file, or
// null.
func (r *efReplay) str(a []token) (v string, null, ok bool) {
	if len(a) != 1 {
		return "", false, false
	}
	switch t := a[0]; {
	case t.kind == tokString && t.literal:
		return t.text, false, true
	case isID(t, "null"):
		return "", true, true
	case t.kind == tokIdent:
		if v, ok := r.consts[t.text]; ok {
			return v, false, true
		}
	}
	return "", false, false
}

// name is a required string argument.
func (r *efReplay) name(args map[string][]token, k string) (string, bool) {
	v, null, ok := r.str(args[k])
	return v, ok && !null && v != ""
}

// schemaOf is the schema argument k, else the provider's default.
func (r *efReplay) schemaOf(args map[string][]token, k string) (string, bool) {
	a, given := args[k]
	if !given {
		return r.schema, true
	}
	v, null, ok := r.str(a)
	if !ok {
		return "", false
	}
	if null || v == "" {
		return r.schema, true
	}
	return v, true
}

func boolArg(a []token) (bool, bool) {
	if len(a) == 1 && (isID(a[0], "true") || isID(a[0], "false")) {
		return a[0].text == "true", true
	}
	return false, false
}

func qualify(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// table is the table named by the name and schema arguments; ok false when
// they cannot be read.
func (r *efReplay) table(args map[string][]token, nameK string) (*sqlddl.Table, bool) {
	n, ok := r.name(args, nameK)
	s, sok := r.schemaOf(args, "schema")
	if !ok || !sok {
		return nil, false
	}
	if r.dialect == sqlddl.MySQL {
		s = ""
	}
	return r.set.Tables()[qualify(s, n)], true
}

func column(t *sqlddl.Table, name string) *sqlddl.Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func dropColumn(t *sqlddl.Table, name string) {
	for i, c := range t.Columns {
		if c.Name == name {
			t.Columns = append(t.Columns[:i], t.Columns[i+1:]...)
			return
		}
	}
}

// call applies one operation; one it cannot read is counted as unread.
func (r *efReplay) call(c efCall) {
	ev := sqlddl.Ev{File: r.file, Line: c.line}
	sig, shapes := efSigs[c.method]
	if !shapes {
		if !efNoShape[c.method] {
			r.unreadAt(c.line)
		}
		return
	}
	args := efArgs(c.args, sig)
	tables := r.set.Tables()
	ok := true
	switch c.method {
	case "EnsureSchema":
	case "CreateTable":
		ok = r.createTable(args, ev)
	case "DropTable":
		var t *sqlddl.Table
		if t, ok = r.table(args, "name"); ok && t != nil {
			delete(tables, t.Qualified())
		}
	case "RenameTable":
		var t *sqlddl.Table
		if t, ok = r.table(args, "name"); ok && t != nil {
			nn, nnull, nok := r.str(args["newName"])
			ns, nsnull, sok := r.str(args["newSchema"])
			if _, given := args["newName"]; !given {
				nok, nnull = true, true
			}
			if _, given := args["newSchema"]; !given {
				sok, nsnull = true, true
			}
			if !nok || !sok {
				ok = false
				break
			}
			delete(tables, t.Qualified())
			if !nnull && nn != "" {
				t.Name = nn
			}
			if !nsnull && ns != "" && r.dialect != sqlddl.MySQL {
				t.Schema = ns
			}
			t.Evidence = sqlddl.AddEv(t.Evidence, ev)
			tables[t.Qualified()] = t
		}
	case "AddColumn", "AlterColumn":
		var t *sqlddl.Table
		if t, ok = r.table(args, "table"); ok && t != nil {
			n, nok := r.name(args, "name")
			if !nok {
				ok = false
				break
			}
			col := column(t, n)
			if c.method == "AddColumn" || col == nil {
				if c.method == "AlterColumn" {
					break // a column the replay never saw: a baseline outside the repo
				}
				dropColumn(t, n)
				col = &sqlddl.Column{Name: n}
				t.Columns = append(t.Columns, col)
			}
			ok = r.columnArgs(col, c.generic, args)
			col.Evidence = sqlddl.AddEv(col.Evidence, ev)
			t.Evidence = sqlddl.AddEv(t.Evidence, ev)
		}
	case "DropColumn":
		var t *sqlddl.Table
		if t, ok = r.table(args, "table"); ok && t != nil {
			n, nok := r.name(args, "name")
			if !nok {
				ok = false
				break
			}
			if column(t, n) != nil {
				dropColumn(t, n)
				t.Evidence = sqlddl.AddEv(t.Evidence, ev)
			}
		}
	case "RenameColumn":
		var t *sqlddl.Table
		if t, ok = r.table(args, "table"); ok && t != nil {
			n, nok := r.name(args, "name")
			nn, nnok := r.name(args, "newName")
			if !nok || !nnok {
				ok = false
				break
			}
			if col := column(t, n); col != nil {
				col.Name = nn
				col.Evidence = sqlddl.AddEv(col.Evidence, ev)
				t.Evidence = sqlddl.AddEv(t.Evidence, ev)
			}
		}
	case "AddPrimaryKey":
		if a, single := args["column"]; single {
			args["columns"] = a
		}
		var t *sqlddl.Table
		if t, ok = r.table(args, "table"); ok && t != nil {
			names, nok := r.strings(args["columns"])
			if !nok {
				ok = false
				break
			}
			for _, n := range names {
				if col := column(t, n); col != nil {
					col.Primary, col.NotNull = true, true
					col.Evidence = sqlddl.AddEv(col.Evidence, ev)
				}
			}
			t.Evidence = sqlddl.AddEv(t.Evidence, ev)
		}
	case "DropPrimaryKey":
		var t *sqlddl.Table
		if t, ok = r.table(args, "table"); ok && t != nil {
			for _, col := range t.Columns {
				col.Primary = false
			}
			t.Evidence = sqlddl.AddEv(t.Evidence, ev)
		}
	case "Sql":
		ok = r.sql(args, c.line)
	}
	if !ok {
		r.unreadAt(c.line)
	}
}

// strings reads a string, or new[] { "a", "b" } (new string[] too).
func (r *efReplay) strings(a []token) ([]string, bool) {
	if v, null, ok := r.str(a); ok && !null {
		return []string{v}, true
	}
	i := 0
	for i < len(a) && !isP(a[i], "{") {
		if !isID(a[i], "new") && !isID(a[i], "string") && !isP(a[i], "[") && !isP(a[i], "]") {
			return nil, false
		}
		i++
	}
	if i == len(a) || !isP(a[len(a)-1], "}") {
		return nil, false
	}
	var out []string
	for _, el := range splitTop(a[i+1 : len(a)-1]) {
		v, null, ok := r.str(el)
		if !ok || null {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// columnArgs sets a column's types, nullability and length from
// AddColumn/AlterColumn or table.Column arguments; nullable defaults to
// false, as in EF's signatures.
func (r *efReplay) columnArgs(col *sqlddl.Column, clr string, args map[string][]token) bool {
	ok := true
	r.clr[col] = clr
	if a, given := args["type"]; given {
		v, null, vok := r.str(a)
		ok = ok && vok
		if vok && !null {
			col.Type = v
		}
	}
	col.NotNull = true
	if a, given := args["nullable"]; given {
		v, vok := boolArg(a)
		ok = ok && vok
		col.NotNull = !v || col.Primary
	}
	delete(r.maxLen, col)
	if a, given := args["maxLength"]; given && !(len(a) == 1 && isID(a[0], "null")) {
		n, err := strconv.Atoi(joinText(a))
		if err != nil {
			ok = false
		} else {
			r.maxLen[col] = n
		}
	}
	return ok
}

// createTable reads CreateTable: the columns lambda `t => new { P =
// t.Column<T>(…), … }` and the constraints lambda's PrimaryKey.
func (r *efReplay) createTable(args map[string][]token, ev sqlddl.Ev) bool {
	n, ok := r.name(args, "name")
	s, sok := r.schemaOf(args, "schema")
	if !ok || !sok {
		return false
	}
	if r.dialect == sqlddl.MySQL {
		s = ""
	}
	t := &sqlddl.Table{Schema: s, Name: n, Evidence: []sqlddl.Ev{ev}}
	r.set.Tables()[t.Qualified()] = t
	param, body := lambda(args["columns"])
	if param == "" || len(body) < 3 || !isID(body[0], "new") || !isP(body[1], "{") {
		return false
	}
	props := map[string]*sqlddl.Column{}
	end := closing(body, 1, "{", "}")
	if end < 0 {
		return false
	}
	for _, el := range splitTop(body[2:end]) {
		// Prop = t.Column<T>(args)[.Annotation(…)…]
		if len(el) < 7 || el[0].kind != tokIdent || !isP(el[1], "=") || !isID(el[2], param) || !isP(el[3], ".") || !isID(el[4], "Column") {
			ok = false
			continue
		}
		j, clr := 5, ""
		if isP(el[j], "<") {
			e := closing(el, j, "<", ">")
			if e < 0 {
				ok = false
				continue
			}
			clr, j = joinText(el[j+1:e]), e+1
		}
		if j >= len(el) || !isP(el[j], "(") {
			ok = false
			continue
		}
		ce := closing(el, j, "(", ")")
		if ce < 0 {
			ok = false
			continue
		}
		cargs := efArgs(el[j+1:ce], efColumnSig)
		name := el[0].text
		if a, given := cargs["name"]; given {
			if v, null, vok := r.str(a); vok && !null && v != "" {
				name = v
			} else if !vok {
				ok = false
				continue
			}
		}
		col := &sqlddl.Column{Name: name, Evidence: []sqlddl.Ev{{File: r.file, Line: el[0].line}}}
		ok = r.columnArgs(col, clr, cargs) && ok
		dropColumn(t, name)
		t.Columns = append(t.Columns, col)
		props[el[0].text] = col
	}
	if a, given := args["constraints"]; given {
		cp, cb := lambda(a)
		for i := 0; i+3 < len(cb); i++ {
			if !isID(cb[i], cp) || !isP(cb[i+1], ".") || !isID(cb[i+2], "PrimaryKey") || !isP(cb[i+3], "(") {
				continue
			}
			e := closing(cb, i+3, "(", ")")
			if e < 0 {
				ok = false
				break
			}
			pk := efArgs(cb[i+4:e], efPKSig)
			_, keyBody := lambda(pk["columns"])
			if keyBody == nil {
				ok = false
			}
			for k := 1; k < len(keyBody); k++ {
				if isP(keyBody[k-1], ".") && keyBody[k].kind == tokIdent {
					if col := props[keyBody[k].text]; col != nil {
						col.Primary, col.NotNull = true, true
					}
				}
			}
			i = e
		}
	}
	return ok
}

// lambda splits `p => body` (or `(p) => body`) into p and body.
func lambda(a []token) (string, []token) {
	for i := 0; i+1 < len(a); i++ {
		if isP(a[i], "=") && isP(a[i+1], ">") {
			for k := i - 1; k >= 0; k-- {
				if a[k].kind == tokIdent {
					return a[k].text, a[i+2:]
				}
			}
			return "", nil
		}
	}
	return "", nil
}

// sql replays migrationBuilder.Sql("…") with a literal or const string
// through sqlddl, every statement at the call's line.
func (r *efReplay) sql(args map[string][]token, line int) bool {
	v, null, ok := r.str(args["sql"])
	if !ok || null {
		return false
	}
	stmts, err := r.set.Split([]byte(v))
	if err != nil {
		r.errs = append(r.errs, fact.LocationError{Path: r.file, Line: line, Message: "efcore: sql: " + err.Error()})
		return false
	}
	problems, unread := r.set.Exec(r.file, line, stmts)
	for _, p := range problems {
		r.errs = append(r.errs, fact.LocationError{Path: p.File, Line: p.Line, Message: "efcore: sql: " + p.Msg})
	}
	return !unread // a statement it could not apply: the call counts as unread
}

func (r *efReplay) facts(folder, context, unit string, ext map[string]any) []fact.Fact {
	tables := make([]*sqlddl.Table, 0, len(r.set.Tables()))
	for _, t := range r.set.Tables() {
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Qualified() < tables[j].Qualified() })
	prefix := folder + ":" + context + ":"
	evidence := func(evs []sqlddl.Ev) []fact.Evidence {
		out := make([]fact.Evidence, 0, len(evs))
		for _, e := range evs {
			out = append(out, fact.Evidence{Path: e.File, Line: e.Line, Unit: unit})
		}
		return out
	}
	copyExt := func() map[string]any {
		x := make(map[string]any, len(ext)+1)
		for k, v := range ext {
			x[k] = v
		}
		return x
	}
	var out []fact.Fact
	for _, t := range tables {
		q := t.Qualified()
		out = append(out, fact.Fact{Kind: fact.KindDBTable, Key: "db_table:efcore:" + prefix + q, Plugin: "dotnet",
			Attrs: map[string]any{"name": q, "ext": copyExt()}, Evidence: evidence(t.Evidence)})
		for _, c := range t.Columns {
			typ, fromEF := r.clr[c]
			if !fromEF {
				typ = c.Type // created by SQL: the type as written
			}
			a := map[string]any{"table": q, "name": c.Name, "type": typ, "nullable": !c.NotNull}
			if c.Type != "" {
				a["native_type"] = c.Type
			}
			if c.Primary {
				a["primary_key"] = true
			}
			if c.Unique {
				a["unique"] = true
			}
			x := copyExt()
			if n, ok := r.maxLen[c]; ok {
				x["max_length"] = n
			}
			a["ext"] = x
			out = append(out, fact.Fact{Kind: fact.KindDBColumn, Key: "db_column:efcore:" + prefix + q + "." + c.Name, Plugin: "dotnet",
				Attrs: a, Evidence: evidence(c.Evidence)})
		}
	}
	return out
}
