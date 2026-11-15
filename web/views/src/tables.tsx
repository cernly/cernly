// The Data view of one scan: the tables and columns the repo's schema
// sources declare (SQL migrations replayed in order, Prisma schemas), one
// table per qualified name with every source.
import { n, plural } from "./format";
import type { Fact, Scan } from "./types";
import { Drawer, Info } from "./ui";
import { sourceLabel, type TableGroup, type TableRow } from "./tables-model";
import "./tables.css";

export type Where = (path: string, line?: number) => string | undefined;

// DataSummary: the tiles over the tables.
export function DataSummary({ scan, rows, groups, total, checked }: { scan: Scan; rows: TableRow[]; groups: TableGroup[]; total: number; checked: boolean }) {
  const dbTables = scan.facts.filter((f: Fact) => f.kind === "db_table");
  const sets = new Set(dbTables.map((f) => f.key.split(":").slice(1, -1).join(":")));
  const withRLS = dbTables.filter((f) => typeof f.attrs.rls === "boolean");
  const rlsOn = withRLS.filter((f) => f.attrs.rls === true).length;
  const columns = rows.reduce((k, t) => k + t.columnCount, 0);
  const toCheck = rows.filter((t) => t.check.length > 0).length;
  const schemas = groups.filter((g) => g.schema).length;
  return (
    <section aria-label="Data summary" className="panel tiles">
      <div className="tile"><p className="label">Tables</p><p className="big">{n(total)}</p><p className="muted">{plural(columns, "column")}{schemas ? ` · ${plural(schemas, "schema")}` : ""}</p></div>
      <div className="tile"><p className="label">Schema sources</p><p className="big">{sets.size}</p><p className="muted">migration sets and Prisma schemas</p></div>
      <div className="tile">
        <p className="label">Row-level security</p>
        <p className="big">{withRLS.length ? rlsOn : "–"} {withRLS.length > 0 && <small>of {plural(withRLS.length, "table")}</small>}</p>
        <p className="muted">{withRLS.length ? "enabled in the migrations" : "not stated by the sources"}</p>
      </div>
      <div className="tile">
        <p className="label">Sensitive-looking columns</p>
        <p className="big">{checked ? toCheck : "–"} {checked && total > 0 && <small>of {plural(total, "table")}</small>}</p>
        <p className="muted">{checked ? "tables to check for encryption and access" : "derive the map again to check"}</p>
      </div>
    </section>
  );
}

// AllTables: groups are closed by default; each says how many tables it
// has, how many are new since the earlier scan and how many have columns to
// check. of follows the heading; since says what "new" is measured against.
export function AllTables({ groups, total, checked, of, since, picked, onPick, where }: {
  groups: TableGroup[]; total: number; checked: boolean; of: string; since: string;
  picked: string | null; onPick: (table: string) => void; where: Where;
}) {
  return (
    <section aria-label="All tables" className="panel all-tables">
      <div className="panel-head">
        <h2>
          All tables · {of}
          <Info label="How tables are grouped and checked">
            <span><b>Schemas.</b> A table is grouped by the schema in its qualified name; its Prisma schema and SQL migrations are one row. Unqualified names on Postgres go to <span className="code">public</span>; MySQL and SQLite tables are under "no schema". Found in shows the file and line that creates it; later migrations are in the table's drawer.</span>
            <span><b>Check</b> names columns whose name matches a fixed word list (password, secret, token, iban, email, ...), on whole name segments: <span className="code">refresh_token</span> matches, <span className="code">token_count</span> does not. It means: check encryption and who can read it. The code cannot show either, so it never says insecure.</span>
          </Info>
        </h2>
        <span className="label">{plural(total, "table")} · grouped by schema · {since}</span>
      </div>
      {total === 0 ? (
        <p className="empty">No tables: the repo has no SQL migrations or Prisma schema that create one.</p>
      ) : (
        <div className="table-groups">
          {groups.map((g) => (
            <details key={g.schema || "-"} className="table-group">
              <summary>
                <span className="tg-name">{g.schema || "no schema"}</span>
                <span className="tg-meta">
                  <span>{plural(g.tables.length, "table")}</span>
                  {g.newCount > 0 && <span className="tg-new">{g.newCount} new</span>}
                  {g.checkCount > 0 && <span className="tg-check">{g.checkCount} to check</span>}
                </span>
              </summary>
              <div className="scroll-x">
                <table className="tables-list">
                  <colgroup><col style={{ width: "26%" }} /><col style={{ width: "10%" }} /><col /><col style={{ width: "24%" }} /></colgroup>
                  <thead><tr><th>Table</th><th className="num">Columns</th><th>Found in</th><th>Check</th></tr></thead>
                  <tbody>
                    {g.tables.map((t) => (
                      <tr key={t.table} className={`pick${picked === t.table ? " on" : ""}`} onClick={() => onPick(t.table)}>
                        <td style={{ fontWeight: 600 }}>
                          <button type="button" className="rowpick" aria-expanded={picked === t.table}>{t.name}</button>
                          {t.isNew && <span className="new-pill">New</span>}
                        </td>
                        <td className="num">{t.columnCount}</td>
                        <td><div className="found-in">
                          {t.sources.map((s) => (
                            <span key={s.key} title={sourceLabel(s)}>
                              {s.evidence.slice(0, 1).map((e, k) => {
                                const url = where(e.path, e.line);
                                const text = `${e.path}:${e.line}`;
                                return url
                                  ? <a key={k} className="code" href={url} target="_blank" rel="noreferrer" onClick={(ev) => ev.stopPropagation()}>{text}</a>
                                  : <span key={k} className="code">{text}</span>;
                              })}
                              {s.evidence.length > 1 && <span className="muted">+{s.evidence.length - 1} more (in the drawer)</span>}
                            </span>
                          ))}
                        </div></td>
                        <td>
                          {t.check.length > 0
                            ? <span className="check-flag" title="Column names that look sensitive: check encryption and access">{t.check.map((c) => c.column).join(", ")}</span>
                            : <span className="muted">{checked ? "nothing seen" : "–"}</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
          ))}
        </div>
      )}
    </section>
  );
}

export function TableDrawer({ t, where, onClose }: { t: TableRow; where: Where; onClose: () => void }) {
  const flagged = new Set(t.check.map((c) => c.column));
  const many = t.sources.length > 1;
  return (
    <Drawer title={t.name} label={`Table · ${t.schema || "no schema"}`} onClose={onClose}>
      <div className="aside-section">
        <h3>Defined in</h3>
        <ul className="evidence">
          {t.sources.flatMap((s) => s.evidence.map((e, k) => {
            const url = where(e.path, e.line);
            return <li key={`${s.key}-${k}`}>{url ? <a href={url} target="_blank" rel="noreferrer">{e.path}:{e.line}</a> : `${e.path}:${e.line}`} <span className="muted">({sourceLabel(s)})</span></li>;
          }))}
        </ul>
      </div>
      {t.check.length > 0 && (
        <div className="aside-section">
          <h3>To check</h3>
          <p style={{ fontSize: 13 }}>Check encryption and who can read {t.check.length === 1 ? "it" : "them"}.</p>
          <ul className="evidence">{t.check.map((c) => <li key={c.column}><b className="code">{c.column}</b> <span className="muted">matches "{c.word}"</span></li>)}</ul>
        </div>
      )}
      <div className="scroll-x">
        <table>
          <thead><tr><th>Column</th><th>Type</th><th>Null</th><th>Key</th>{many && <th>Source</th>}</tr></thead>
          <tbody>
            {t.columns.map((c) => {
              const e = c.evidence[0];
              const url = e ? where(e.path, e.line) : undefined;
              const src = t.sources.find((s) => c.key.startsWith(`db_column:${s.key.slice("db_table:".length)}.`));
              return (
                <tr key={c.key}>
                  <td style={{ fontWeight: 600 }}>
                    {url ? <a href={url} target="_blank" rel="noreferrer">{String(c.attrs.name)}</a> : String(c.attrs.name)}
                    {flagged.has(String(c.attrs.name)) && <span className="check-flag" title="Name looks sensitive: check encryption and access"> check</span>}
                  </td>
                  <td className="code">{String(c.attrs.type)}{c.attrs.enum ? " (enum)" : ""}</td>
                  <td>{c.attrs.nullable ? "yes" : "no"}</td>
                  <td>{[c.attrs.primary_key ? "primary" : "", c.attrs.unique ? "unique" : ""].filter(Boolean).join(", ")}</td>
                  {many && <td className="muted">{src?.source ?? ""}</td>}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Drawer>
  );
}
