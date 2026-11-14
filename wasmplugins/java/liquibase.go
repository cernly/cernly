package main

import (
	"bytes"
	"encoding/xml"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cernly/cernly/fact"
)

// Liquibase changelogs (XML and YAML) replayed in include order, as
// migrations-sql replays SQL sets: the tables and columns the set leaves. Read:
// createTable, addColumn, dropTable, dropColumn, renameTable, renameColumn,
// include, includeAll. Other change types (sql, modifyDataType, views) are not
// read. Formatted SQL changelogs are SQL and read by migrations-sql.

type lbChange struct {
	op                  string
	schema, table, to   string
	column, toColumn    string
	cols                []lbColumn
	line                int
	file                string
	include, includeAll string
	relative            bool
}

type lbColumn struct {
	name, typ            string
	nullable, pk, unique *bool
	line                 int
}

func boolp(s string) *bool {
	switch strings.ToLower(s) {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	}
	return nil
}

func isChangelogCandidate(f string) bool {
	switch path.Ext(f) {
	case ".xml", ".yaml", ".yml":
		return strings.Contains(strings.ToLower(f), "changelog") || strings.Contains(f, "/db/")
	}
	return false
}

func liquibase(p *project, paths []string, e *emitter) {
	logs := map[string][]lbChange{}
	for _, f := range paths {
		if !isChangelogCandidate(f) || isSpringConfig(f) {
			continue
		}
		var ch []lbChange
		var ok bool
		if path.Ext(f) == ".xml" {
			ch, ok = lbXML(f, p.files[f])
		} else {
			ch, ok = lbYAML(f, p.files[f])
		}
		if ok {
			logs[f] = ch
		}
	}
	if len(logs) == 0 {
		return
	}
	included := map[string]bool{}
	resolve := func(from string, c lbChange) []string {
		target := c.include
		if c.includeAll != "" {
			target = strings.TrimSuffix(c.includeAll, "/")
		}
		var base string
		if c.relative {
			base = path.Dir(from)
		} else if i := strings.Index(from, "src/main/resources/"); i >= 0 {
			base = from[:i+len("src/main/resources")]
		} else {
			base = path.Dir(from)
		}
		full := path.Join(base, strings.TrimPrefix(target, "classpath:"))
		if c.include != "" {
			return []string{full}
		}
		var out []string
		for f := range logs {
			if path.Dir(f) == full || strings.HasPrefix(f, full+"/") {
				out = append(out, f)
			}
		}
		sort.Strings(out)
		return out
	}
	for f, ch := range logs {
		for _, c := range ch {
			if c.op == "include" {
				for _, t := range resolve(f, c) {
					included[t] = true
				}
			}
		}
	}
	var masters []string
	for f := range logs {
		if !included[f] {
			masters = append(masters, f)
		}
	}
	sort.Strings(masters)
	for _, m := range masters {
		var flat []lbChange
		seen := map[string]bool{}
		var walk func(f string)
		walk = func(f string) {
			if seen[f] {
				return
			}
			seen[f] = true
			for _, c := range logs[f] {
				if c.op == "include" {
					for _, t := range resolve(f, c) {
						if _, ok := logs[t]; ok {
							walk(t)
						} else {
							e.fail(f, c.line, "include "+t+": not a tracked changelog")
						}
					}
					continue
				}
				flat = append(flat, c)
			}
		}
		walk(m)
		replayLiquibase(p, path.Dir(m), p.module(m), flat, e)
	}
}

type lbTable struct {
	name string
	ev   []fact.Evidence
	cols map[string]*lbCol
	ord  []string
}

type lbCol struct {
	typ                  string
	nullable, pk, unique bool
	ev                   []fact.Evidence
}

func replayLiquibase(p *project, set, mod string, ch []lbChange, e *emitter) {
	tables := map[string]*lbTable{}
	qual := func(schema, t string) string { return p.qualify(mod, schema, t) }
	evd := func(c lbChange, line int) fact.Evidence { return fact.Evidence{Path: c.file, Line: line, Unit: set} }
	addCols := func(t *lbTable, c lbChange) {
		for _, col := range c.cols {
			lc := &lbCol{typ: strings.ToLower(col.typ), nullable: true, ev: []fact.Evidence{evd(c, col.line)}}
			if col.nullable != nil {
				lc.nullable = *col.nullable
			}
			if col.pk != nil && *col.pk {
				lc.pk, lc.nullable = true, false
			}
			if col.unique != nil {
				lc.unique = *col.unique
			}
			if _, ok := t.cols[col.name]; !ok {
				t.ord = append(t.ord, col.name)
			}
			t.cols[col.name] = lc
		}
	}
	for _, c := range ch {
		key := qual(c.schema, c.table)
		t := tables[key]
		switch c.op {
		case "createTable":
			t = &lbTable{name: key, cols: map[string]*lbCol{}, ev: []fact.Evidence{evd(c, c.line)}}
			tables[key] = t
			addCols(t, c)
		case "addColumn":
			if t != nil {
				t.ev = append(t.ev, evd(c, c.line))
				addCols(t, c)
			}
		case "dropTable":
			delete(tables, key)
		case "dropColumn":
			if t != nil {
				delete(t.cols, c.column)
				t.ev = append(t.ev, evd(c, c.line))
			}
		case "renameTable":
			if t != nil {
				delete(tables, key)
				nk := qual(c.schema, c.to)
				t.name = nk
				t.ev = append(t.ev, evd(c, c.line))
				tables[nk] = t
			}
		case "renameColumn":
			if t != nil {
				if col, ok := t.cols[c.column]; ok {
					delete(t.cols, c.column)
					col.ev = append(col.ev, evd(c, c.line))
					t.cols[c.toColumn] = col
					t.ord = append(t.ord, c.toColumn)
				}
			}
		}
	}
	for name, t := range tables {
		e.add(fact.KindDBTable, "db_table:liquibase:"+set+":"+name, map[string]any{"name": name}, t.ev...)
		for cn, c := range t.cols {
			attrs := map[string]any{"table": name, "name": cn, "type": c.typ, "nullable": c.nullable}
			if c.pk {
				attrs["primary_key"] = true
			}
			if c.unique {
				attrs["unique"] = true
			}
			e.add(fact.KindDBColumn, "db_column:liquibase:"+set+":"+name+"."+cn, attrs, c.ev...)
		}
	}
}

func lbXML(f string, data []byte) ([]lbChange, bool) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out []lbChange
	var cur *lbChange
	var col *lbColumn
	root := true
	for {
		tok, err := dec.Token()
		if err != nil {
			return out, !root
		}
		line, _ := dec.InputPos()
		switch t := tok.(type) {
		case xml.StartElement:
			at := map[string]string{}
			for _, a := range t.Attr {
				at[a.Name.Local] = a.Value
			}
			if root {
				if t.Name.Local != "databaseChangeLog" {
					return nil, false
				}
				root = false
				continue
			}
			switch n := t.Name.Local; n {
			case "include":
				out = append(out, lbChange{op: "include", include: at["file"], relative: at["relativeToChangelogFile"] == "true", line: line, file: f})
			case "includeAll":
				out = append(out, lbChange{op: "include", includeAll: at["path"], relative: at["relativeToChangelogFile"] == "true", line: line, file: f})
			case "createTable", "addColumn", "dropTable", "dropColumn", "renameTable", "renameColumn":
				c := lbChange{op: n, schema: at["schemaName"], table: at["tableName"], column: at["columnName"], line: line, file: f}
				switch n {
				case "renameTable":
					c.table, c.to = at["oldTableName"], at["newTableName"]
				case "renameColumn":
					c.column, c.toColumn = at["oldColumnName"], at["newColumnName"]
				}
				out = append(out, c)
				cur = &out[len(out)-1]
			case "column":
				if cur != nil && (cur.op == "createTable" || cur.op == "addColumn") {
					cur.cols = append(cur.cols, lbColumn{name: at["name"], typ: at["type"], line: line})
					col = &cur.cols[len(cur.cols)-1]
				}
			case "constraints":
				if col != nil {
					col.nullable, col.pk, col.unique = boolp(at["nullable"]), boolp(at["primaryKey"]), boolp(at["unique"])
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "column":
				col = nil
			case "createTable", "addColumn":
				cur = nil
			}
		}
	}
}

func lbYAML(f string, data []byte) ([]lbChange, bool) {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil, false
	}
	top := doc.Content[0]
	seq := mapGet(top, "databaseChangeLog")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil, false
	}
	var out []lbChange
	for _, item := range seq.Content {
		if inc := mapGet(item, "include"); inc != nil {
			out = append(out, lbChange{op: "include", include: scalar(mapGet(inc, "file")), relative: scalar(mapGet(inc, "relativeToChangelogFile")) == "true", line: inc.Line, file: f})
		}
		if inc := mapGet(item, "includeAll"); inc != nil {
			out = append(out, lbChange{op: "include", includeAll: scalar(mapGet(inc, "path")), relative: scalar(mapGet(inc, "relativeToChangelogFile")) == "true", line: inc.Line, file: f})
		}
		cs := mapGet(item, "changeSet")
		changes := mapGet(cs, "changes")
		if changes == nil {
			continue
		}
		for _, chg := range changes.Content {
			if chg.Kind != yaml.MappingNode || len(chg.Content) < 2 {
				continue
			}
			op, body := chg.Content[0].Value, chg.Content[1]
			c := lbChange{op: op, schema: scalar(mapGet(body, "schemaName")), table: scalar(mapGet(body, "tableName")),
				column: scalar(mapGet(body, "columnName")), line: chg.Content[0].Line, file: f}
			switch op {
			case "renameTable":
				c.table, c.to = scalar(mapGet(body, "oldTableName")), scalar(mapGet(body, "newTableName"))
			case "renameColumn":
				c.column, c.toColumn = scalar(mapGet(body, "oldColumnName")), scalar(mapGet(body, "newColumnName"))
			case "createTable", "addColumn":
				if cols := mapGet(body, "columns"); cols != nil {
					for _, ci := range cols.Content {
						cn := mapGet(ci, "column")
						if cn == nil {
							continue
						}
						lc := lbColumn{name: scalar(mapGet(cn, "name")), typ: scalar(mapGet(cn, "type")), line: cn.Line}
						if k := mapGet(cn, "constraints"); k != nil {
							lc.nullable, lc.pk, lc.unique = boolp(scalar(mapGet(k, "nullable"))), boolp(scalar(mapGet(k, "primaryKey"))), boolp(scalar(mapGet(k, "unique")))
						}
						c.cols = append(c.cols, lc)
					}
				}
			case "dropTable", "dropColumn":
			default:
				continue
			}
			out = append(out, c)
		}
	}
	return out, true
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}
