// Package prisma reads Prisma schema files into the tables and columns the
// schema declares: one db_table per model, one db_column per field the
// database stores. Relation fields are left out (Prisma keeps them in the
// client, not in a column); enums give a column its type and are not facts
// of their own. Names follow @@map / @map and @@schema. Whether a table has
// row-level security is not in a Prisma schema, so rls is left out rather
// than reported false. Nothing is run; env("…") in the datasource is not
// read. See https://cernly.dev/how/plugins/.
package prisma

import (
	"path"
	"sort"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/prismaschema"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "prisma" }
func (Plugin) Version() string { return "0.1.0" }

func isSchema(p string) bool { return defaults.MatchBase(defaults.Get().Files.Prisma, path.Base(p)) }

func (Plugin) Watches(p string) bool { return isSchema(p) }

// Run reads every schema folder as one schema: Prisma's multi-file schemas
// spread the models of one database over the .prisma files of a folder.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	byDir := map[string][]string{}
	for _, f := range r.Match(func(base string) bool { return isSchema(base) }) {
		byDir[repo.Dir(f)] = append(byDir[repo.Dir(f)], f)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		var blocks []prismaschema.Block
		for _, f := range byDir[d] {
			data, err := r.Read(f)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Message: err.Error()})
				continue
			}
			bs, err := prismaschema.Parse(f, data)
			if err != nil {
				res.Errors = append(res.Errors, fact.LocationError{Path: f, Line: prismaschema.LineOf(err), Message: "prisma: " + err.Error()})
				continue
			}
			blocks = append(blocks, bs...)
		}
		res.Facts = append(res.Facts, facts(d, blocks)...)
	}
	return res
}

func facts(dir string, blocks []prismaschema.Block) []fact.Fact {
	provider := ""
	models := map[string]bool{}
	enums := map[string]bool{}
	for _, b := range blocks {
		switch b.Kind {
		case "datasource":
			for _, l := range b.Lines {
				if len(l.Toks) >= 3 && l.Toks[0].Text == "provider" && l.Toks[1].Text == "=" {
					provider = prismaschema.Unquote(l.Toks[2].Text)
				}
			}
		case "model", "view":
			models[b.Name] = true
		case "type":
			models[b.Name] = true // a composite type, embedded: never a column of its own table
		case "enum":
			enums[b.Name] = true
		}
	}
	var out []fact.Fact
	for _, b := range blocks {
		if b.Kind != "model" {
			continue
		}
		table := b.Name
		schema := ""
		pk := map[string]bool{}
		for _, l := range b.Lines {
			if len(l.Toks) > 0 && l.Toks[0].Text == "@@id" {
				for _, f := range l.Fields() {
					pk[f] = true // composite primary key, by field name
				}
			}
			if a := l.BlockAttr("@@map"); a != "" {
				table = a
			}
			if a := l.BlockAttr("@@schema"); a != "" {
				schema = a
			}
		}
		if schema == "" && (provider == "postgresql" || provider == "postgres" || provider == "cockroachdb") {
			schema = "public" // Prisma's default; a ?schema= in the URL is deployment, not repo
		}
		name := table
		if schema != "" {
			name = schema + "." + table
		}
		out = append(out, fact.Fact{Kind: fact.KindDBTable, Key: "db_table:prisma:" + dir + ":" + name, Plugin: "prisma",
			Attrs:    map[string]any{"name": name, "model": b.Name},
			Evidence: []fact.Evidence{{Path: b.File, Line: b.Line, EndLine: b.End}}})
		for _, l := range b.Lines {
			if len(l.Toks) < 2 || strings.HasPrefix(l.Toks[0].Text, "@") {
				continue
			}
			field, typ := l.Toks[0].Text, l.Toks[1].Text
			nullable, list := false, false
			i := 2
			if typ == "Unsupported" {
				typ = "Unsupported" + l.ArgsAfter(2)
				for i < len(l.Toks) && l.Toks[i].Text != ")" {
					i++
				}
				i++
			}
			for ; i < len(l.Toks); i++ {
				switch l.Toks[i].Text {
				case "?":
					nullable = true
					continue
				case "[":
					if i+1 < len(l.Toks) && l.Toks[i+1].Text == "]" {
						list = true
						i++
						continue
					}
				}
				break
			}
			if models[typ] || l.FieldAttr("@relation") {
				continue // a relation (or an embedded type): no column
			}
			col := field
			if a := l.FieldAttrArg("@map"); a != "" {
				col = a
			}
			t := typ
			if list {
				t += "[]"
			}
			attrs := map[string]any{"table": name, "name": col, "type": t, "nullable": nullable}
			if enums[typ] {
				attrs["enum"] = true
			}
			if l.FieldAttr("@id") || pk[field] {
				attrs["primary_key"] = true
			}
			if l.FieldAttr("@unique") {
				attrs["unique"] = true
			}
			if native := l.Native(); native != "" {
				attrs["native_type"] = native
			}
			out = append(out, fact.Fact{Kind: fact.KindDBColumn, Key: "db_column:prisma:" + dir + ":" + name + "." + col, Plugin: "prisma",
				Attrs: attrs, Evidence: []fact.Evidence{{Path: b.File, Line: l.Toks[0].Line}}})
		}
	}
	return out
}
