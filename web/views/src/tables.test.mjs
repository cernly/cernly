// All tables on the Data tab. Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { addedTables, tableGroups } from "./tables-model.ts";

const f = (kind, key, attrs, path, line) => ({ kind, key, attrs, plugin: "x", evidence: [{ path, line }] });
const facts = [
  f("db_table", "db_table:prisma:prisma:public.user", { name: "public.user" }, "prisma/schema.prisma", 12),
  f("db_column", "db_column:prisma:prisma:public.user.id", { name: "id" }, "prisma/schema.prisma", 13),
  f("db_column", "db_column:prisma:prisma:public.user.email", { name: "email" }, "prisma/schema.prisma", 14),
  f("db_table", "db_table:sql:db/migrations:public.user", { name: "public.user" }, "db/migrations/001.sql", 1),
  f("db_column", "db_column:sql:db/migrations:public.user.id", { name: "id" }, "db/migrations/001.sql", 2),
  f("db_column", "db_column:sql:db/migrations:public.user.email", { name: "email" }, "db/migrations/001.sql", 3),
  f("db_column", "db_column:sql:db/migrations:public.user.created_at", { name: "created_at" }, "db/migrations/001.sql", 4),
  f("db_table", "db_table:sql:db/migrations:public.note", { name: "public.note" }, "db/migrations/001.sql", 9),
  f("db_table", "db_table:sql:db/migrations:audit.event", { name: "audit.event" }, "db/migrations/002.sql", 1),
  f("db_table", "db_table:sql:db/migrations:billing.payout", { name: "billing.payout" }, "db/migrations/003.sql", 1),
  f("db_table", "db_table:sql:legacy:orders", { name: "orders" }, "legacy/schema.sql", 1),
];
const map = {
  tables: [
    { table: "audit.event", schema: "audit", from: ["db_table:sql:db/migrations:audit.event"] },
    { table: "billing.payout", schema: "billing", from: ["db_table:sql:db/migrations:billing.payout"] },
    { table: "orders", from: ["db_table:sql:legacy:orders"] },
    { table: "public.note", schema: "public", from: ["db_table:sql:db/migrations:public.note"] },
    { table: "public.user", schema: "public", from: ["db_table:prisma:prisma:public.user", "db_table:sql:db/migrations:public.user"],
      check: [{ column: "email", word: "email", from: [] }] },
  ],
};

test("groups by schema: new or to check first, then by name, no schema last", () => {
  const added = new Set(["db_table:sql:db/migrations:billing.payout"]);
  const { groups, checked, total } = tableGroups(map, facts, added);
  assert.equal(checked, true);
  assert.equal(total, 5);
  assert.deepEqual(groups.map((g) => [g.schema, g.tables.length, g.newCount, g.checkCount]), [
    ["billing", 1, 1, 0], ["public", 2, 0, 1], ["audit", 1, 0, 0], ["", 1, 0, 0],
  ]);
});

test("one table with two sources; columns counted once per name", () => {
  const { groups } = tableGroups(map, facts, null);
  const user = groups.find((g) => g.schema === "public").tables.find((t) => t.name === "user");
  assert.deepEqual(user.sources.map((s) => [s.source, s.set, s.evidence[0].line]), [["prisma", "prisma", 12], ["sql", "db/migrations", 1]]);
  assert.equal(user.columns.length, 5);
  assert.equal(user.columnCount, 3);
  assert.equal(user.isNew, false); // first week: no pills
});

test("a table is new only when every source is new", () => {
  const { groups } = tableGroups(map, facts, new Set(["db_table:sql:db/migrations:public.user"]));
  assert.equal(groups.find((g) => g.schema === "public").tables.find((t) => t.name === "user").isNew, false);
});

test("without tables in the map: merged by name, nothing checked", () => {
  const { groups, checked } = tableGroups({}, facts, null);
  assert.equal(checked, false);
  assert.equal(groups.find((g) => g.schema === "public").tables.find((t) => t.name === "user").sources.length, 2);
});

test("added tables from a drift", () => {
  assert.equal(addedTables(null), null);
  const d = { drift: { facts: [{ change: "added", section: "db_table", key: "a" }, { change: "removed", section: "db_table", key: "b" }, { change: "added", section: "db_column", key: "c" }] } };
  assert.deepEqual([...addedTables(d)], ["a"]);
});
