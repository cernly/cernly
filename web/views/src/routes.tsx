// The Routes view of one scan: web server routes placed in the stack whose
// service mounts the file, the hosts the repo serves, and the hosts nobody
// claims. Deciding about a host is the hosted app's; it passes its buttons
// and decided list in `decide`.
import type { ReactNode } from "react";
import { plural, stackLabel, tail } from "./format";
import type { Evidence, Fact, MapDoc } from "./types";
import { Info } from "./ui";
import { STATE_WORDS, USAGE_CHIP, unclaimedRows, type HostRow } from "./unclaimed-model";
import "./hosts.css";
import "./routes.css";

export { STATE_WORDS, type HostRow };

export type Where = (path: string, line?: number) => string | undefined;

// RoutesPanels: what the outside world reaches, per host, and every route
// per stack. title follows the heading (a week, a commit).
export function RoutesPanels({ map, labels, title }: { map: MapDoc; labels: Record<string, string>; title: string }) {
  const routes = map.routes ?? [];
  const groups = new Map<string, typeof routes>();
  for (const r of routes) {
    const g = r.stack ? `stack ${stackLabel(labels, tail(r.stack))}, through ${tail(r.via ?? "")}` : `${r.route.split(":")[2]}: no compose service mounts it`;
    groups.set(g, [...(groups.get(g) ?? []), r]);
  }
  const byHost = new Map<string, typeof routes>();
  for (const r of routes) for (const site of r.sites.length ? r.sites : ["(any host)"]) {
    const h = site.replace(/^https?:\/\//, "");
    byHost.set(h, [...(byHost.get(h) ?? []), r]);
  }
  const hosts = [...byHost].sort((a, b) => a[0].localeCompare(b[0]));
  return (
    <>
      <section id="routes" aria-label="Routes" className="panel">
        <div className="panel-head"><h2>What the outside world reaches · {title}</h2><span className="label">{plural(hosts.length, "host")} · {plural(routes.length, "route")}</span></div>
        {routes.length === 0 ? (
          <p className="empty">No web server routes in this scan.</p>
        ) : (
          <div className="flow">
            {hosts.map(([host, rs]) => {
              const targets = [...new Set(rs.map((r) => (r.to ? tail(r.to) : r.upstream ? `↗ ${r.upstream}` : r.action)))];
              return (
                <div key={host} style={{ display: "contents" }}>
                  <div className="node host">{host}</div>
                  <div className="arrow">→</div>
                  <div className="paths">{rs.slice(0, 4).map((r) => `${r.match || "/"} ${r.action}`).join(" · ")}{rs.length > 4 ? ` · and ${rs.length - 4} more` : ""}</div>
                  <div className="arrow">→</div>
                  <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                    {targets.map((t) => <div key={t} className={`node ${t.startsWith("↗") ? "ext" : "svc"}`}>{t}</div>)}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </section>
      <section aria-label="All routes" className="panel">
        <details className="more" style={{ borderTop: 0 }}>
          <summary>{routes.length === 1 ? "The one route" : `All ${plural(routes.length, "route")}`}, per stack</summary>
          {[...groups].map(([g, rs]) => (
            <div key={g} className="scroll-x">
              <p className="label" style={{ padding: "12px 16px 0" }}>{g}</p>
              <table style={{ minWidth: 560 }}>
                <thead><tr><th>Site</th><th>Path</th><th>Action</th><th>To</th></tr></thead>
                <tbody>
                  {rs.map((r, k) => (
                    <tr key={k}>
                      <td>{r.sites.map((s) => s.replace(/^https?:\/\//, "")).join(", ")}</td>
                      <td className="code">{r.match}</td>
                      <td>{r.action}</td>
                      <td style={{ fontWeight: 600 }}>{r.to ? tail(r.to) : <span className="muted">{r.upstream ?? ""}</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
        </details>
      </section>
    </>
  );
}

// OwnHosts: the hosts the repo's routes serve or its config declares; note
// says where they come from.
export function OwnHosts({ map, note }: { map: MapDoc; note: string }) {
  return (
    <section aria-label="Own hosts" className="panel">
      <div className="panel-head"><h2>Own hosts ({map.own.length})</h2><span className="label">{note}</span></div>
      {map.own.length === 0 ? <p className="empty">None served or declared.</p> : (
        <div className="scroll-x">
          <table>
            <tbody>
              {map.own.map((o) => (
                <tr key={o.host}><td className="code">{o.host}</td><td className="muted">{[o.served_by?.length ? "served" : "", o.declared ? `declared (${o.declared.by})` : "", o.confirmed ? `claimed in the app (#${o.confirmed.id})` : ""].filter(Boolean).join(", ")}</td></tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

const OWN_SNIPPET = "version: 1\ndomains:\n  own: [example.be]   # your domains; their subdomains count too\n";

function EvidenceLinks({ evidence, where }: { evidence: Evidence[]; where: Where }) {
  return (
    <>
      {evidence.map((e, k) => {
        const text = `${e.path}:${e.line}`;
        const url = where(e.path, e.line);
        return (
          <span key={k}>
            {k > 0 && ", "}
            {url ? <a className="code" href={url} target="_blank" rel="noreferrer">{text}</a> : <span className="code">{text}</span>}
          </span>
        );
      })}
    </>
  );
}

// HostSources: where the code names the host: per fact, the services it is
// attached to and its file and line.
export function HostSources({ row, where }: { row: HostRow; where: Where }) {
  return (
    <ul className="host-sources">
      {row.sources.map((s) => (
        <li key={s.fact}>
          <span className="host-service">{s.services.length ? [...new Set(s.services.map(tail))].join(", ") : "on no service"}</span>
          {" · "}
          {s.evidence.length ? <EvidenceLinks evidence={s.evidence} where={where} /> : <span className="muted">{s.fact}</span>}
        </li>
      ))}
    </ul>
  );
}

export function UsageChip({ usage }: { usage: string }) {
  const called = usage === "request" || usage === "default";
  return <span className={`host-chip${called ? " called" : ""}`}>{USAGE_CHIP[usage] ?? usage}</span>;
}

// Decide is what the hosted app adds: the choices per open host, a note
// under them, and the hosts already decided.
export type Decide = { actions: (row: HostRow) => ReactNode; note?: ReactNode; decided: (rows: HostRow[]) => ReactNode };

// UnclaimedHosts: what it means, why it matters and what to do; one row per
// host the code calls or configures, with its evidence; hosts only
// mentioned folded away. No choice is highlighted: Cernly does not guess
// which one applies. Without decide, the list is read-only and points to
// .cernly.yml.
export function UnclaimedHosts({ map, facts, where, decide }: { map: MapDoc; facts: Fact[]; where: Where; decide?: Decide }) {
  const { open, decided, mentioned } = unclaimedRows(map, facts);
  return (
    <section id="unclaimed" aria-label="Hosts nobody claims" className="panel unclaimed">
      <div className="panel-head">
        <h2>
          Hosts nobody claims
          <Info label="What this means">
            <span>The code calls or configures these hosts, and Cernly cannot match them to your own domains or to a known supplier in its catalogue.</span>
            <span>It may be a supplier nobody registered, which the supply-chain duties of NIS2 ask you to know about. It may also be your own old system, or a test address.</span>
            {decide ? <span>Each choice goes into the audit trail and can be undone.</span> : <span>Declare your own domains in .cernly.yml; the next scan counts them as yours.</span>}
          </Info>
        </h2>
        <span className="label">{open.length} to decide · {decided.length} decided · {mentioned.length} only mentioned</span>
      </div>
      {open.length > 0 && (decide
        ? <p className="unclaimed-todo"><b>To do:</b> per host, choose Approved supplier, Our own domain or Not a supplier, with a reason.</p>
        : <p className="unclaimed-todo"><b>To do:</b> per host, find out who runs it. Declare your own domains in .cernly.yml (below).</p>)}
      {open.length === 0 ? (
        <p className="empty">No open hosts: every host the code calls is claimed, or decided below.</p>
      ) : (
        <ul className="host-rows">
          {open.map((r) => (
            <li key={r.host} className="host-row">
              <div className="host-main">
                <span className="host-name">{r.host}</span>
                <span className="host-line"><UsageChip usage={r.usage} /></span>
                <HostSources row={r} where={where} />
              </div>
              {decide && <div className="host-actions">{decide.actions(r)}</div>}
            </li>
          ))}
        </ul>
      )}
      {open.length > 0 && decide?.note}
      {decided.length > 0 && decide?.decided(decided)}
      {mentioned.length > 0 && (
        <details className="more host-mentioned">
          <summary>{mentioned.length === 1 ? "1 host only mentioned" : `${mentioned.length} hosts only mentioned`} in code (docs links, examples)</summary>
          <p className="muted unclaimed-note">The address appears in code or config; nothing shows it is called. No decision is needed.</p>
          <ul className="host-rows">
            {mentioned.map((r) => (
              <li key={r.host} className="host-row quiet">
                <div className="host-main">
                  <span className="host-name">{r.host}</span>
                  <HostSources row={r} where={where} />
                </div>
              </li>
            ))}
          </ul>
        </details>
      )}
      <details className="snippet-alt unclaimed-alt">
        <summary>{decide ? "or declare your own domains in the repository" : "declare your own domains in the repository"}</summary>
        <div className="snippet">
          <span className="label">Add to .cernly.yml at the repository root; the next scan picks it up and it wins over the app</span>
          <pre className="code">{OWN_SNIPPET}</pre>
        </div>
      </details>
    </section>
  );
}
