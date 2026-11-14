package main

import (
	"strings"
	"unicode"

	"github.com/cernly/cernly/fact"
)

// JPA entities in Java sources: @Entity classes become tables, their
// persistent fields columns. Names follow Spring Boot's default physical
// naming (CamelCaseToUnderscoresNamingStrategy): camelCase becomes
// snake_case in lower case, explicit names included. Not read yet:
// @MappedSuperclass fields, @Embedded values, inheritance strategies,
// Kotlin entities.

var primitives = map[string]bool{"int": true, "long": true, "short": true, "byte": true, "boolean": true,
	"double": true, "float": true, "char": true}

var modifiers = map[string]bool{"public": true, "private": true, "protected": true, "final": true,
	"static": true, "transient": true, "volatile": true, "abstract": true}

// snake is Spring's physical naming: a _ before each upper-case letter that
// follows a lower-case letter or digit, dots to _, all lower case.
func snake(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if c == '.' {
			b.WriteByte('_')
			continue
		}
		if i > 0 && unicode.IsUpper(c) && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1])) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

func hasAnn(as []annotation, name string) (annotation, bool) {
	for _, a := range as {
		if a.name == name {
			return a, true
		}
	}
	return annotation{}, false
}

func jpa(p *project, f, mod string, ts []token, e *emitter) {
	var pending []annotation
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		switch {
		case t.is("@"):
			a, next := readAnnotation(ts, i)
			pending = append(pending, a)
			i = next - 1
		case t.is("class") && i+1 < len(ts) && ts[i+1].kind == tIdent:
			_, isEntity := hasAnn(pending, "Entity")
			// the body
			j := i + 2
			for j < len(ts) && !ts[j].is("{") {
				j++
			}
			if j >= len(ts) {
				return
			}
			end := closing(ts, j)
			if isEntity {
				entity(p, f, mod, ts[i+1], pending, ts[j+1:end], ts[end].line, e)
				i = end
			} else {
				i = j // nested classes are read from inside
			}
			pending = nil
		case t.is(";") || t.is("{") || t.is("}"):
			pending = nil
		}
	}
}

func entity(p *project, f, mod string, class token, anns []annotation, body []token, endLine int, e *emitter) {
	tableName := snake(class.text)
	schemaName := ""
	if t, ok := hasAnn(anns, "Table"); ok {
		if n, ok := t.arg("name"); ok && n != "" {
			tableName = snake(n)
		}
		if s, ok := t.arg("schema"); ok && s != "" {
			schemaName = s
		}
	}
	q := p.qualify(mod, schemaName, tableName)
	attrs := map[string]any{"name": q, "ext": map[string]any{"entity": class.text}}
	if tableName != snake(class.text) {
		attrs["model"] = class.text
	}
	e.add(fact.KindDBTable, "db_table:jpa:"+mod+":"+q, attrs, fact.Evidence{Path: f, Line: anns[0].line, EndLine: endLine, Unit: mod})
	var pending []annotation
	for i := 0; i < len(body); i++ {
		t := body[i]
		switch {
		case t.is("@"):
			a, next := readAnnotation(body, i)
			pending = append(pending, a)
			i = next - 1
		case t.is("{"):
			i = closing(body, i) // initialiser block or nested type
			pending = nil
		case t.is("class") || t.is("enum") || t.is("interface") || t.is("record"):
			for i < len(body) && !body[i].is("{") {
				i++
			}
			if i < len(body) {
				i = closing(body, i)
			}
			pending = nil
		default:
			// A member: tokens up to ; = ( or {.
			j := i
			for j < len(body) && !body[j].is(";") && !body[j].is("=") && !body[j].is("(") && !body[j].is("{") {
				j++
			}
			if j >= len(body) {
				return
			}
			decl := body[i:j]
			switch {
			case body[j].is("("): // method or constructor
				k := closing(body, j) + 1
				for k < len(body) && !body[k].is("{") && !body[k].is(";") {
					k++
				}
				if k < len(body) && body[k].is("{") {
					k = closing(body, k)
				}
				i = k
				pending = nil
				continue
			case body[j].is("{"):
				i = closing(body, j)
				pending = nil
				continue
			case body[j].is("="):
				k := j
				for k < len(body) && !body[k].is(";") {
					if body[k].is("(") || body[k].is("{") || body[k].is("[") {
						k = closing(body, k)
					}
					k++
				}
				j = k
			}
			field(f, mod, q, decl, pending, e)
			pending = nil
			i = j
		}
	}
}

func field(f, mod, table string, decl []token, anns []annotation, e *emitter) {
	if len(decl) < 2 {
		return
	}
	var typ []string
	for _, t := range decl[:len(decl)-1] {
		if t.kind == tIdent && modifiers[t.text] {
			if t.text == "static" || t.text == "transient" {
				return
			}
			continue
		}
		typ = append(typ, t.text)
	}
	nameTok := decl[len(decl)-1]
	if nameTok.kind != tIdent || len(typ) == 0 {
		return
	}
	for _, skip := range []string{"Transient", "OneToMany", "ManyToMany", "Embedded", "EmbeddedId", "ElementCollection"} {
		if _, ok := hasAnn(anns, skip); ok {
			return
		}
	}
	col := snake(nameTok.text)
	typeText := strings.Join(typ, "")
	attrs := map[string]any{"table": table, "type": typeText}
	nullable := !primitives[typeText]
	line := nameTok.line
	if len(anns) > 0 {
		line = anns[0].line
	}
	_, many := hasAnn(anns, "ManyToOne")
	one, oneOK := hasAnn(anns, "OneToOne")
	if many || oneOK {
		if oneOK && one.has("mappedBy") {
			return // the other side holds the key
		}
		col = snake(nameTok.text) + "_id"
		if jc, ok := hasAnn(anns, "JoinColumn"); ok {
			if n, ok := jc.arg("name"); ok && n != "" {
				col = snake(n)
			}
			if v, ok := jc.boolArg("nullable"); ok {
				nullable = v
			}
		}
		attrs["ext"] = map[string]any{"references": typeText}
	}
	if c, ok := hasAnn(anns, "Column"); ok {
		if n, ok := c.arg("name"); ok && n != "" {
			col = snake(n)
		}
		if v, ok := c.boolArg("nullable"); ok {
			nullable = v
		}
		if v, ok := c.boolArg("unique"); ok && v {
			attrs["unique"] = true
		}
		if d, ok := c.arg("columnDefinition"); ok && d != "" {
			attrs["native_type"] = d
		}
	}
	if _, ok := hasAnn(anns, "Id"); ok {
		attrs["primary_key"] = true
		nullable = false
	}
	if _, ok := hasAnn(anns, "Enumerated"); ok {
		attrs["enum"] = true
	}
	attrs["name"] = col
	attrs["nullable"] = nullable
	evd := fact.Evidence{Path: f, Line: line, Unit: mod}
	if nameTok.line > line {
		evd.EndLine = nameTok.line
	}
	e.add(fact.KindDBColumn, "db_column:jpa:"+mod+":"+table+"."+col, attrs, evd)
}
