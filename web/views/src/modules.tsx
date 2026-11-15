// The module graph of one scan: boxes are the repo's own packages, arrows
// are imports, read from the code without building it.
import { useMemo, useState, type ReactNode } from "react";
import { plural, tail } from "./format";
import { layers, modulesOf, trimPrefix, type ModuleGraph, type Package } from "./metrics";
import type { Fact, MapDoc } from "./types";
import { Info } from "./ui";

export type Where = (path: string, line?: number) => string | undefined;

const COL = 230, ROW = 46, BW = 190, BH = 34, DENSE = 40;

// ModuleGraphView: one service's module graph (all packages without a
// service): the repo's own packages it reaches, the imports between them
// with their lines, and coupling per package. back sits in the panel head
// (a way back, or a service picker); foot ends the coupling table.
export function ModuleGraphView({ map, facts, service, back, foot, where }: {
  map: MapDoc | null; facts: Fact[]; service: string; back: ReactNode; foot: ReactNode; where: Where;
}) {
  const g = modulesOf(map);
  const reach = g?.services.find((s) => s.service === service);
  const only = service ? reach?.packages ?? [] : undefined;
  const [picked, setPicked] = useState<string | null>(null);
  const name = useMemo(() => trimPrefix((only ?? g?.packages.map((p) => p.package)) ?? []), [g, only]);
  if (!g) return <p className="panel empty">No imports between the repo's own packages in this scan. Go is read today; TypeScript, JavaScript and Python come later. {back}</p>;
  if (service && !reach) return <p className="panel empty">{tail(service)} reaches none of the repo's own packages in this scan. {back}</p>;
  const ls = layers(g, only);
  const keep = new Set(ls.flat().map((p) => p.package));
  const edges = g.edges.filter((e) => keep.has(e.from) && keep.has(e.to));
  // A dense graph draws only the picked package's imports.
  const dense = edges.length > DENSE;
  const drawn = dense ? edges.filter((e) => e.from === picked || e.to === picked) : edges;
  const pos = new Map<string, { x: number; y: number }>();
  ls.forEach((l, c) => l.forEach((p, r) => pos.set(p.package, { x: c * COL, y: r * ROW })));
  const width = ls.length * COL - (COL - BW), height = Math.max(...ls.map((l) => l.length)) * ROW;
  const pk = g.packages.find((p) => p.package === picked);
  const cycles = g.cycles.filter((c) => c.packages.some((p) => keep.has(p)));
  return (
    <>
      <section className="panel" aria-label="Module graph">
        <div className="panel-head">
          <h2>Module graph{service ? ` · ${tail(service)}` : ""}<Info label="About the module graph"><span>Boxes are the repo's own packages, arrows are imports, read from the code without building it. Importers are on the left, the packages most depended on to the right.</span><span>{!service ? "Every package in the scan." : reach?.reach === "entrypoints" ? `Followed from the entrypoint the Dockerfile builds (${(reach.roots ?? []).map(name).join(", ")}).` : "Every package in the service's sources: its Dockerfile names no Go entrypoint to follow."}</span></Info></h2>
          <span className="label">{plural(keep.size, "package")} · {plural(edges.length, "import")}{cycles.length ? ` · ${plural(cycles.length, "cycle")}` : ""}</span>
          <div style={{ flex: "1 1 auto" }} />
          {back}
        </div>
        <div className="scroll-x" style={{ padding: 16 }}>
          <div className="plan" style={{ width, height }}>
            <svg width={width} height={height} aria-hidden="true">
              <defs>
                <marker id="mah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                  <path d="M0 0 L10 5 L0 10 z" style={{ fill: "var(--ink-muted)" }} />
                </marker>
              </defs>
              {drawn.map((e) => {
                const a = pos.get(e.from)!, b = pos.get(e.to)!;
                const hot = picked === e.from || picked === e.to;
                const x1 = a.x + BW, y1 = a.y + BH / 2, x2 = b.x, y2 = b.y + BH / 2;
                const d = x2 > x1 ? `M${x1} ${y1} C${(x1 + x2) / 2} ${y1} ${(x1 + x2) / 2} ${y2} ${x2} ${y2}` : `M${a.x + BW / 2} ${a.y + BH} C${a.x + BW / 2} ${a.y + BH + 24} ${b.x + BW / 2} ${b.y + BH + 24} ${b.x + BW / 2} ${b.y + BH}`;
                return <path key={e.from + ">" + e.to} d={d} fill="none" strokeWidth={hot ? 2 : 1.2} style={{ stroke: hot ? "var(--accent)" : "var(--ink-muted)" }} markerEnd="url(#mah)" />;
              })}
            </svg>
            {ls.flat().map((p) => {
              const at = pos.get(p.package)!;
              return (
                <button type="button" key={p.package} className={`box${p.package === picked ? " selected" : ""}${p.cycle ? " floating" : ""}`}
                  style={{ left: at.x, top: at.y, width: BW, height: BH }} onClick={() => setPicked(p.package === picked ? null : p.package)} aria-pressed={p.package === picked} title={p.package}>
                  <b>{name(p.package)}</b>
                </button>
              );
            })}
          </div>
        </div>
        {dense && <p className="panel-foot muted">{picked ? `Showing the imports of ${name(picked)}; pick another package, or the same one again to clear.` : `${plural(edges.length, "import")}: too many to draw at once. Pick a package to see what it imports and what imports it.`}</p>}
        {cycles.length > 0 && <p className="panel-foot warn">Import cycles: {cycles.map((c) => c.packages.map(name).join(" ⇄ ")).join("; ")}</p>}
      </section>
      {pk && <PackageDetail g={g} p={pk} facts={facts} name={name} where={where} />}
      <section className="panel" aria-label="Coupling per package">
        <div className="panel-head"><h2>Coupling per package</h2></div>
        <div className="scroll-x">
          <table>
            <thead><tr><th>Package</th><th>Folder</th><th className="num">Imported by</th><th className="num">Imports</th><th className="num">Depth</th></tr></thead>
            <tbody>
              {[...keep].sort().map((k) => {
                const p = g.packages.find((x) => x.package === k)!;
                return (
                  <tr key={k}>
                    <td><button type="button" className="linkish" onClick={() => setPicked(k)}>{name(k)}</button>{p.cycle && <span className="muted"> · in a cycle</span>}</td>
                    <td className="mono">{p.dir}</td><td className="num">{p.fan_in}</td><td className="num">{p.fan_out}</td><td className="num">{p.depth}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <p className="panel-foot mono" style={{ fontSize: 12 }}>{foot}</p>
      </section>
    </>
  );
}

// What one package imports and what imports it, each with its import lines.
export function PackageDetail({ g, p, facts, name, where }: { g: ModuleGraph; p: Package; facts: Fact[]; name: (s: string) => string; where: Where }) {
  const ev = (e: { path: string; line: number }) => {
    const url = where(e.path, e.line);
    return url ? <a href={url} target="_blank" rel="noreferrer">{e.path}:{e.line}</a> : <span>{e.path}:{e.line}</span>;
  };
  const list = (edges: typeof g.edges, other: (e: (typeof g.edges)[number]) => string) => (
    <ul className="evidence">
      {edges.map((e) => (
        <li key={e.from + e.to}>
          <b>{name(other(e))}</b>{" "}
          {e.facts.flatMap((k) => facts.find((f) => f.key === k)?.evidence ?? []).slice(0, 3).map((x, i) => <span key={i} className="muted"> · {ev(x)}</span>)}
        </li>
      ))}
    </ul>
  );
  const out = g.edges.filter((e) => e.from === p.package), inc = g.edges.filter((e) => e.to === p.package);
  return (
    <section className="panel" aria-label={`Package ${name(p.package)}`}>
      <div className="panel-head"><h2>{name(p.package)}</h2><span className="label mono">{p.package}</span></div>
      <div className="aside-section" style={{ padding: "8px 16px 16px" }}>
        <h3>Imports {out.length}</h3>
        {out.length ? list(out, (e) => e.to) : <p className="muted">None of the repo's own packages.</p>}
        <h3 style={{ marginTop: 12 }}>Imported by {inc.length}</h3>
        {inc.length ? list(inc, (e) => e.from) : <p className="muted">No package of the repo.</p>}
      </div>
    </section>
  );
}
