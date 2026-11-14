package prisma

import (
	"testing"

	"github.com/cernly/cernly/internal/prismaschema"
)

func TestMySQLAndNamedMap(t *testing.T) {
	bs, err := prismaschema.Parse("s.prisma", []byte("datasource db {\n  provider = \"mysql\"\n}\nmodel A {\n  id Int @id\n  @@map(name: \"a_table\")\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs := facts("db", bs)
	if len(fs) != 2 || fs[0].Key != "db_table:prisma:db:a_table" || fs[1].Key != "db_column:prisma:db:a_table.id" {
		t.Errorf("facts = %+v (mysql has no schema prefix; @@map(name:) is read)", fs)
	}
}
