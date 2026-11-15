// The Dependencies view of one scan: per service, the direct dependencies the
// attach rule puts on it and the transitive ones its manifest folders reach
// through the lockfiles. A comparison with an earlier scan and who added a
// dependency are the hosted app's; it passes them in.
import { useState, type ReactNode } from "react";
import { changeText, filterServices, licenceText, type Dep, type Filter, type ServiceDeps } from "./deps-model";
import { plural } from "./format";
import { Info } from "./ui";
import "./deps.css";
import "./tables.css";

export type Where = (path: string, line?: number) => string | undefined;

// DependencyTiles: direct, transitive and declared licences; since is a
// tile for a comparison, placed third.
export function DependencyTiles({ counts: c, since }: { counts: Record<string, number>; since?: ReactNode }) {
  return (
    <section aria-label="Dependencies summary" className="panel tiles">
      <div className="tile"><p className="label">Direct</p><p className="big">{c.direct ?? 0}</p><p className="muted">declared in manifests</p></div>
      <div className="tile"><p className="label">Transitive</p><p className="big">{c.transitive ?? 0}</p><p className="muted">from lockfiles</p></div>
      {since}
      <div className="tile">
        <p className="label">Licences declared</p>
        <p className="big">{c.licence_declared ?? 0} <small>of {(c.direct ?? 0) + (c.transitive ?? 0)}</small></p>
        <p className="muted">as the lockfile states; never looked up</p>
      </div>
    </section>
  );
}

// DependenciesPanel: the services' dependencies with a filter and a search.
// since names the earlier scan compared with (null: none chosen; undefined:
// this page compares nothing, so "changed" is not offered). tools go before
// the filter, notes under the tools; addedBy gives a row's added-by line.
export function DependenciesPanel<D extends Dep>({ services, title, since, tools, notes, addedBy, where }: {
  services: ServiceDeps<D>[]; title: string; since?: string | null; tools?: ReactNode; notes?: ReactNode;
  addedBy?: (d: D) => ReactNode; where: Where;
}) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const shown = filterServices(services, filter, query);
  const at = since ?? null;
  return (
    <section aria-label="Dependencies by service" className="panel">
      <div className="panel-head">
        <h2>
          Dependencies by service · {title}
          <Info label="Where these come from">
            <span><b>Direct</b> dependencies are the ones a manifest declares (go.mod, package.json, pyproject.toml, requirements files); the attach rule puts them on the service whose build reads that folder.</span>
            <span><b>Transitive</b> ones come from the lockfile only (package-lock.json, go.mod indirect, uv.lock, poetry.lock, Pipfile.lock). Without a lockfile there are none.</span>
            {addedBy && <span><b>Added by</b> names the change on the default branch that added a new direct dependency, and its proof: an agent, a bot, or neither. It is never a guess; when the change cannot be found, it says so.</span>}
            <span><b>Licence</b> is shown only where a file declares it (the npm lockfile today).</span>
          </Info>
        </h2>
        <span className="label">{plural(services.length, "group")}</span>
      </div>
      <div className="deps-tools">
        {tools}
        <label className="label" htmlFor="dep-filter">Show</label>
        <select id="dep-filter" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
          <option value="all">direct and transitive</option>
          <option value="direct">direct only</option>
          {since !== undefined && <option value="changed" disabled={!since}>changed since {since ?? "a week"}</option>}
        </select>
        <label className="sr-only" htmlFor="dep-q">Search packages</label>
        <input id="dep-q" type="text" placeholder="Search packages" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>
      {notes}
      {shown.length === 0 ? (
        <p className="empty">{services.length ? "Nothing matches." : "No dependencies: the repo has no manifest the readers know."}</p>
      ) : (
        <div className="dep-groups">
          {shown.map((s) => (
            <ServiceGroup key={s.service || "-"} s={s} since={at} where={where} addedBy={addedBy}
              startOpen={shown.length <= 3} showTransitive={query !== "" || filter === "changed"} />
          ))}
        </div>
      )}
    </section>
  );
}

// PAGE rows per list: a monorepo has thousands of locked packages, and
// rendering them all made one tab 45,000 DOM nodes.
const PAGE = 50;

// One service: its direct dependencies, paged, and its transitive ones in a
// part that is closed by default (open while searching or showing changes).
// A closed part renders no rows.
function ServiceGroup<D extends Dep>({ s, since, where, startOpen, showTransitive, addedBy }: {
  s: ServiceDeps<D>; since: string | null; where: Where; startOpen: boolean; showTransitive: boolean; addedBy?: (d: D) => ReactNode;
}) {
  const [open, setOpen] = useState(startOpen);
  const [transOpen, setTransOpen] = useState(false);
  const [nDirect, setNDirect] = useState(PAGE);
  const [nTrans, setNTrans] = useState(PAGE);
  const trans = transOpen || showTransitive;
  return (
    <details className="table-group" open={open} onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}>
      <summary>
        <span className="tg-name">{s.name}</span>
        <span className="tg-meta">
          <span>{s.direct.length} direct</span>
          <span>{s.transitive.length} transitive</span>
          {since && <span className="tg-new">{[...s.direct, ...s.transitive].filter((d) => d.change === "new").length} new</span>}
        </span>
      </summary>
      {open && (
        <div className="scroll-x">
          <table className="deps-list">
            <colgroup><col style={{ width: "26%" }} /><col style={{ width: "13%" }} /><col style={{ width: "21%" }} /><col style={{ width: "12%" }} /><col /></colgroup>
            <thead><tr><th>Package</th><th>Version</th><th>Change</th><th>Licence</th><th>Found in</th></tr></thead>
            <tbody>
              {s.direct.slice(0, nDirect).map((d) => <DepRow key={d.key} d={d} where={where} addedBy={addedBy} />)}
              <More shown={nDirect} total={s.direct.length} onMore={() => setNDirect(nDirect + PAGE * 4)} what="direct" />
              {s.transitive.length > 0 && (
                <tr className="sub">
                  <th colSpan={5}>
                    <button type="button" className="linkish" aria-expanded={trans} disabled={showTransitive} onClick={() => setTransOpen(!transOpen)}>
                      {trans ? "Hide" : "Show"} transitive ({s.transitive.length})
                    </button>
                  </th>
                </tr>
              )}
              {trans && s.transitive.slice(0, nTrans).map((d) => <DepRow key={d.key} d={d} where={where} addedBy={addedBy} />)}
              {trans && <More shown={nTrans} total={s.transitive.length} onMore={() => setNTrans(nTrans + PAGE * 4)} what="transitive" />}
            </tbody>
          </table>
        </div>
      )}
    </details>
  );
}

function More({ shown, total, onMore, what }: { shown: number; total: number; onMore: () => void; what: string }) {
  if (total <= shown) return null;
  return (
    <tr className="more">
      <td colSpan={5}>
        <span className="muted">{shown} of {total} {what} shown · </span>
        <button type="button" className="linkish" onClick={onMore}>Show {Math.min(PAGE * 4, total - shown)} more</button>
      </td>
    </tr>
  );
}

function DepRow<D extends Dep>({ d, where, addedBy }: { d: D; where: Where; addedBy?: (d: D) => ReactNode }) {
  const url = where(d.path, d.line);
  return (
    <tr>
      <td style={{ fontWeight: 600 }}>
        {d.name}
        {d.scope === "dev" && <span className="muted"> · dev</span>}
        {!d.direct && d.via && d.via.length > 0 && <span className="dep-by">via {d.via.join(", ")}</span>}
      </td>
      <td className="code">{d.version || "–"}</td>
      <td>
        {d.change ? <span className={`dep-change ${d.change}`}>{changeText(d)}</span> : <span className="muted">–</span>}
        {addedBy?.(d)}
      </td>
      <td className={d.licence ? "code" : "muted"}>{licenceText(d)}</td>
      <td>{url ? <a className="code" href={url} target="_blank" rel="noreferrer">{d.path}:{d.line}</a> : <span className="code">{d.path}:{d.line}</span>}</td>
    </tr>
  );
}
