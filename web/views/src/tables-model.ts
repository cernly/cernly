// All tables of the Data view: the map's tables grouped by schema, each with
// every source's file and line, its column count, whether it is new since an
// earlier scan, and the columns to check. Pure: tested with `npm test`
// (tables.test.mjs).
import type { Evidence, Fact, MapDoc, MapTable, SensitiveColumn } from "./types";

export type TableSource = { key: string; source: string; set: string; evidence: Evidence[] };
export type TableRow = {
  table: string; // qualified
  name: string; // without the schema
  schema: string; // "" when the name has none
  sources: TableSource[];
  columns: Fact[]; // db_column facts of every source
  columnCount: number; // distinct column names over the sources
  isNew: boolean;
  check: SensitiveColumn[];
};
export type TableGroup = { schema: string; tables: TableRow[]; newCount: number; checkCount: number };

// Without tables in the map (rules before 0.5.0): merge the db_table facts by
// name here, with no check (that is the rules' job).
function mapTables(map: MapDoc, facts: Fact[]): { tables: MapTable[]; checked: boolean } {
  if (map.tables) return { tables: map.tables, checked: true };
  const by = new Map<string, MapTable>();
  for (const f of facts) {
    if (f.kind !== "db_table") continue;
    const name = String(f.attrs.name);
    const dot = name.lastIndexOf(".");
    const t = by.get(name) ?? { table: name, schema: dot > 0 ? name.slice(0, dot) : undefined, from: [] };
    t.from.push(f.key);
    by.set(name, t);
  }
  return { tables: [...by.values()], checked: false };
}

// added: the db_table keys added since the previous stored week, or null for
// the first week (no pills then). A table is new when none of its sources
// existed the week before.
export function tableGroups(map: MapDoc, facts: Fact[], added: Set<string> | null): { groups: TableGroup[]; checked: boolean; total: number } {
  const byKey = new Map(facts.map((f) => [f.key, f]));
  const cols = new Map<string, Fact[]>(); // "db_column:<source>:<set>:<name>." -> columns
  for (const f of facts) {
    if (f.kind !== "db_column") continue;
    const p = f.key.slice(0, f.key.lastIndexOf(".") + 1);
    cols.set(p, [...(cols.get(p) ?? []), f]);
  }
  const { tables, checked } = mapTables(map, facts);
  const groups = new Map<string, TableGroup>();
  for (const t of tables) {
    const schema = t.schema ?? "";
    const sources = t.from.map((k) => {
      const [, source = "", ...rest] = k.split(":");
      const set = rest.slice(0, -1).join(":");
      return { key: k, source, set, evidence: byKey.get(k)?.evidence ?? [] };
    });
    const columns = t.from.flatMap((k) => cols.get(`db_column:${k.slice("db_table:".length)}.`) ?? []);
    const row: TableRow = {
      table: t.table,
      name: schema ? t.table.slice(schema.length + 1) : t.table,
      schema,
      sources,
      columns,
      columnCount: new Set(columns.map((c) => String(c.attrs.name))).size,
      isNew: !!added && t.from.every((k) => added.has(k)),
      check: t.check ?? [],
    };
    const g = groups.get(schema) ?? { schema, tables: [], newCount: 0, checkCount: 0 };
    g.tables.push(row);
    if (row.isNew) g.newCount++;
    if (row.check.length) g.checkCount++;
    groups.set(schema, g);
  }
  for (const g of groups.values()) g.tables.sort((a, b) => a.name.localeCompare(b.name));
  // Groups with something new or to check first, then by name; "no schema" last among equals.
  const out = [...groups.values()].sort((a, b) => {
    const ka = a.newCount + a.checkCount > 0 ? 0 : 1, kb = b.newCount + b.checkCount > 0 ? 0 : 1;
    if (ka !== kb) return ka - kb;
    if (!a.schema !== !b.schema) return a.schema ? -1 : 1;
    return a.schema.localeCompare(b.schema);
  });
  return { groups: out, checked, total: tables.length };
}

// The added db_table keys of a drift, or null without a previous week.
export function addedTables(drift: { drift: { facts: { change: string; section: string; key: string }[] } } | null): Set<string> | null {
  if (!drift) return null;
  return new Set(drift.drift.facts.filter((c) => c.section === "db_table" && c.change === "added").map((c) => c.key));
}

export const sourceLabel = (s: TableSource) => (s.source === "sql" ? "SQL migrations" : s.source === "prisma" ? "Prisma schema" : s.source) + (s.set ? ` · ${s.set}` : "");
