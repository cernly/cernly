// Package migrations reads SQL migrations and schema dumps into the tables
// and columns they leave behind: each migration set is replayed in order, in
// memory, by package sqlddl. Nothing is run. Down migrations are never
// applied; statements it does not know are skipped. See
// https://cernly.dev/how/plugins/.
package migrations

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/sqlddl"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "migrations-sql" }
func (Plugin) Version() string { return "0.2.0" }

func (Plugin) Watches(p string) bool { return candidate(p) }

// candidate is any tracked.sql file outside test folders that is not a down
// migration. Whether it is a migration is decided by its content (Shapes),
// so a repo needs no particular folder name.
func candidate(p string) bool {
	if path.Ext(p) != ".sql" {
		return false
	}
	m := defaults.Get().Files.SQLMigrations
	if defaults.MatchBase(m.Down, path.Base(p)) {
		return false
	}
	dirs := strings.Split(path.Dir(p), "/")
	for _, d := range dirs {
		for _, s := range m.SkipDirs {
			if d == s {
				return false
			}
		}
	}
	return true
}

// known reports a file in a usual migration location: one that cannot be read
// is reported as an error there, and skipped silently elsewhere (a .sql file
// in another dialect is not our concern).
func known(p string) bool {
	for _, pat := range defaults.Get().Files.SQLMigrations.Paths {
		if config.Matches(pat, p) {
			return true
		}
	}
	return false
}

// perFolder are file names that mean one migration per folder (Prisma,
// some golang-migrate layouts): the set is the folder above.
var perFolder = map[string]bool{"migration.sql": true, "up.sql": true}

// setOf is the migration set a file belongs to: the nearest folder named in
// set_dirs above it; else the folder above a one-migration-per-folder file;
// else the file's own folder.
func setOf(p string) string {
	dirs := defaults.Get().Files.SQLMigrations.SetDirs
	parts := strings.Split(p, "/")
	for i := len(parts) - 2; i >= 0; i-- {
		for _, d := range dirs {
			if parts[i] == d {
				return strings.Join(parts[:i+1], "/")
			}
		}
	}
	dir := path.Dir(p)
	if perFolder[path.Base(p)] && dir != "." {
		dir = path.Dir(dir)
	}
	return dir
}

func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	sets := map[string][]string{}
	raw := map[string][]byte{}
	for _, f := range r.Files {
		if !candidate(f) {
			continue
		}
		data, err := r.Read(f)
		if err != nil {
			res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
			continue
		}
		// One file alone decides whether it is a migration; a file only
		// MySQL's lexing reads still counts (SplitSet tries both).
		one, errs := sqlddl.SplitSet([][]byte{data})
		st, err := one[0], errs[0]
		if err != nil {
			if known(f) {
				line := 0
				if se, ok := err.(*sqlddl.SyntaxError); ok {
					line = se.Line
				}
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Line: line, Message: "sql: " + err.Error()})
			}
			continue
		}
		if !sqlddl.Shapes(st) {
			continue
		}
		raw[f] = data
		s := setOf(f)
		sets[s] = append(sets[s], f)
	}
	names := make([]string, 0, len(sets))
	for s := range sets {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, set := range names {
		files := sets[set]
		sort.Slice(files, func(i, j int) bool { return before(rel(set, files[i]), rel(set, files[j])) })
		// The set is lexed together: one MySQL-shaped file makes the whole
		// set MySQL (backslash escapes, # comments).
		srcs := make([][]byte, len(files))
		for i, f := range files {
			srcs[i] = raw[f]
		}
		stmts, errs := sqlddl.SplitSet(srcs)
		var in []sqlddl.File
		for i, f := range files {
			if err := errs[i]; err != nil {
				line := 0
				if se, ok := err.(*sqlddl.SyntaxError); ok {
					line = se.Line
				}
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Line: line, Message: "sql: " + err.Error()})
				continue
			}
			in = append(in, sqlddl.File{Path: f, Stmts: stmts[i]})
		}
		tables, dialect, problems := sqlddl.Replay(in, prismaLock(r, set))
		for _, p := range problems {
			res.Errors = append(res.Errors, fact.LocationError{Path: p.File, Line: p.Line, Message: "sql: " + p.Msg})
		}
		res.Facts = append(res.Facts, facts(set, tables, dialect)...)
	}
	return res
}

func rel(set, f string) string {
	if set == "." {
		return f
	}
	return strings.TrimPrefix(f, set+"/")
}

// before orders the files of a set: digit runs compare as numbers, so V2__
// comes before V10__; Flyway's repeatable R__ files come after the rest, and
// Flyway versions compare part by part, so V1__ comes before V1.1__.
func before(a, b string) bool {
	ra, rb := repeatable(a), repeatable(b)
	if ra != rb {
		return rb
	}
	if va, vb := flywayVersion(a), flywayVersion(b); va != nil && vb != nil {
		for i := 0; i < len(va) || i < len(vb); i++ {
			pa, pb := "", ""
			if i < len(va) {
				pa = va[i]
			}
			if i < len(vb) {
				pb = vb[i]
			}
			if len(pa) != len(pb) {
				return len(pa) < len(pb)
			}
			if pa != pb {
				return pa < pb
			}
		}
	}
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da != "" && db != "" {
			na, nb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func repeatable(p string) bool { return strings.HasPrefix(path.Base(p), "R__") }

// flywayVersion reads V<version>__ from a file name: the version's numeric
// parts, split on dots or underscores, leading zeros and trailing zero parts
// dropped (V1.0 is V1, as in Flyway). Nil if it is not a versioned migration.
func flywayVersion(p string) []string {
	b := path.Base(p)
	end := strings.Index(b, "__")
	if !strings.HasPrefix(b, "V") || end < 2 || digits(b[1:2]) == "" {
		return nil
	}
	parts := []string{}
	for _, s := range strings.FieldsFunc(b[1:end], func(r rune) bool { return r == '.' || r == '_' }) {
		if digits(s) != s {
			return nil
		}
		parts = append(parts, strings.TrimLeft(s, "0"))
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func digits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// prismaLock is the dialect a Prisma migration_lock.toml in the set's
// folder names (provider = "postgresql" or "mysql"), the only proof outside
// the SQL that counts.
func prismaLock(r *repo.Repo, set string) sqlddl.Dialect {
	p, ok := repo.Join(set, "migration_lock.toml")
	if !ok || !r.Has(p) {
		return sqlddl.Dialect{}
	}
	data, err := r.Read(p)
	if err != nil {
		return sqlddl.Dialect{}
	}
	for i, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.TrimSpace(k) != "provider" {
			continue
		}
		switch strings.Trim(strings.TrimSpace(v), `"'`) {
		case "postgresql", "postgres":
			return sqlddl.Dialect{Name: sqlddl.PostgreSQL, Signal: "Prisma provider postgresql", File: p, Line: i + 1}
		case "mysql":
			return sqlddl.Dialect{Name: sqlddl.MySQL, Signal: "Prisma provider mysql", File: p, Line: i + 1}
		}
	}
	return sqlddl.Dialect{}
}

func facts(set string, tables []*sqlddl.Table, d sqlddl.Dialect) []fact.Fact {
	var out []fact.Fact
	// Which dialect the set was read as, and the line that says so; none
	// when the SQL does not tell (names as written, no rls).
	var ext map[string]any
	if d.Name != "" {
		ext = map[string]any{"dialect": d.Name, "dialect_signal": d.Signal, "dialect_at": fmt.Sprintf("%s:%d", d.File, d.Line)}
	} else {
		ext = map[string]any{"dialect": "unknown"}
	}
	for _, t := range tables {
		name := t.Qualified()
		x := map[string]any{}
		for k, v := range ext {
			x[k] = v
		}
		attrs := map[string]any{"name": name, "ext": x}
		if d.Name == sqlddl.PostgreSQL {
			attrs["rls"] = t.RLS
		}
		out = append(out, fact.Fact{Kind: fact.KindDBTable, Key: "db_table:sql:" + set + ":" + name, Plugin: "migrations-sql",
			Attrs: attrs, Evidence: evidence(t.Evidence)})
		for _, c := range t.Columns {
			a := map[string]any{"table": name, "name": c.Name, "type": c.Type, "nullable": !c.NotNull}
			if c.Enum {
				a["enum"] = true
			}
			if c.Primary {
				a["primary_key"] = true
			}
			if c.Unique {
				a["unique"] = true
			}
			out = append(out, fact.Fact{Kind: fact.KindDBColumn, Key: "db_column:sql:" + set + ":" + name + "." + c.Name, Plugin: "migrations-sql",
				Attrs: a, Evidence: evidence(c.Evidence)})
		}
	}
	return out
}

func evidence(evs []sqlddl.Ev) []fact.Evidence {
	out := make([]fact.Evidence, 0, len(evs))
	for _, e := range evs {
		ev := fact.Evidence{Path: e.File, Line: e.Line}
		if e.End > e.Line {
			ev.EndLine = e.End
		}
		out = append(out, ev)
	}
	return out
}
