import { usePresence, useGlide, useTween } from "./motion";
import { BOX, stackID, type Plan } from "./floorplan";
import { HEADLINE, delta, deltaWords, languages, scopeOf, short, testLines, type Metrics } from "./metrics";
import { ciLabel, tail } from "./format";
import type { MapDoc } from "./types";
import { Info, Num } from "./ui";

// What the findings of the map are called.
export const RULE_LABELS: Record<string, string> = {
  image_pinning: "Image pinning",
  production_unconfirmed: "Production unconfirmed",
  party_unconfirmed: "Party unconfirmed",
  driver_without_store: "Driver without data store",
  no_code_link: "No code link",
  link_conflict: "Link conflict",
};


// The surface metrics as the headline: what can go wrong and
// where, each with its change against the reference; size beside them, smaller.
export function Surface({ now, before, name }: { now: Metrics; before: Metrics | null; name: string }) {
  const { scope, s } = scopeOf(now);
  return (
    <section aria-label="Surface" className="panel surface">
      <div className="panel-head">
        <h2>Surface<Info label="About the surface"><span>What can go wrong and where, counted from the map. {scope === "production" ? "Counted in the production stacks." : "No production stack yet: counted in all stacks."}</span><span>Each number shows its change since the week or baseline picked under New since, else the previous week.</span></Info></h2>
        <span className="label">{scope === "production" ? "in production" : "all stacks"}{before && name ? ` · change since ${name}` : ""}</span>
      </div>
      <div className="tiles surface-tiles">
        {HEADLINE.map((h) => {
          const d = delta(now, before, h.key);
          return (
            <div className="tile" key={h.key} title={h.hint}>
              <p className="label">{h.label}</p>
              <p className="big"><Num value={s[h.key]} /></p>
              <p className={d && d > 0 ? "warn" : "muted"}>{d === null ? "no earlier count" : deltaWords(d)}</p>
            </div>
          );
        })}
      </div>
      <p className="panel-foot muted size-line">
        Size, for context: {short(s.size.lines)} lines in {s.size.files.toLocaleString("en-US")} files{s.size.test_lines ? `, ${testLines(s.size)}` : ""}{s.size.languages.length ? ` · ${languages(s.size)}` : ""}
        {s.coupling ? ` · ${s.coupling.packages} packages, ${s.coupling.imports} imports, depth ${s.coupling.depth}${s.coupling.cycles ? `, ${s.coupling.cycles} import cycles` : ""}` : ""}
      </p>
    </section>
  );
}

export function FloorPlan({ plan, picked, onPick, known }: { plan: Plan; picked?: string; onPick: (k: string) => void; known?: Set<string> | null }) {
  // Between weeks: new boxes and edges fade and scale in, removed ones fade
  // out, boxes that move glide, and the edges follow them (usePresence,
  // useGlide; nothing moves with reduced motion).
  const nodes = usePresence(plan.nodes, (x) => x.key);
  const edges = usePresence(plan.edges, (e) => `${e.from}>${e.to}`);
  const at = useGlide(plan.nodes);
  const width = useTween(plan.width, 450), height = useTween(plan.height, 450);
  return (
    <div className="plan" style={{ width, height }}>
      <svg width={Math.max(width, plan.width)} height={Math.max(height, plan.height)} aria-hidden="true">
        <defs>
          <marker id="ah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" style={{ fill: "var(--ink-muted)" }} />
          </marker>
        </defs>
        {edges.map(({ item: e, key, phase }) => {
          const a = at.get(e.from), b = at.get(e.to);
          if (!a || !b) return null;
          const back = b.x <= a.x;
          const x1 = back ? a.x + BOX.w / 2 : a.x + BOX.w, y1 = back ? a.y + BOX.h : a.y + BOX.h / 2;
          const x2 = back ? b.x + BOX.w / 2 : b.x, y2 = back ? b.y + BOX.h : b.y + BOX.h / 2;
          const mid = (x1 + x2) / 2;
          const d = back ? `M${x1} ${y1} C${x1} ${y1 + 30} ${x2} ${y2 + 30} ${x2} ${y2}` : `M${x1} ${y1} C${mid} ${y1} ${mid} ${y2} ${x2} ${y2}`;
          return <path key={key} className={phase === "stay" ? undefined : `edge-${phase}`} d={d} style={{ stroke: "var(--ink-muted)" }} strokeWidth={1.2} fill="none" strokeDasharray={e.kind === "depends_on" ? "4 3" : undefined} markerEnd="url(#ah)" />;
        })}
      </svg>
      {nodes.map(({ item: x, phase }) => {
        const p = at.get(x.key) ?? x;
        return (
          <button
            type="button"
            key={x.key}
            className={`box${x.thirdParty ? " third" : ""}${x.floating ? " floating" : ""}${x.kind === "datastore" ? " store" : ""}${x.key === picked ? " selected" : ""}${known && !known.has(x.key) ? " since-new" : ""}${phase === "stay" ? "" : ` ${phase}`}`}
            style={{ left: p.x, top: p.y, width: BOX.w, height: BOX.h }}
            onClick={() => onPick(x.key)}
            aria-pressed={x.key === picked}
            data-pick={x.key}
            disabled={phase === "leave"}
            aria-hidden={phase === "leave" || undefined}
          >
            <b>{x.name}</b>
            <span>{known && !known.has(x.key) && <span className="new-word">new · </span>}{x.sub}</span>
          </button>
        );
      })}
    </div>
  );
}

export function Parties({ map, stack }: { map: MapDoc; stack: string }) {
  const ci = (sv: string) => sv.startsWith("ci_image:");
  const ps = map.parties.filter((p) => p.services.length === 0 || p.services.some((sv) => stackID(sv) === stack || ci(sv)));
  const shown = usePresence(ps, (p) => p.id);
  if (shown.length === 0) return null;
  return (
    <>
      <p className="label" style={{ padding: "0 16px 8px" }}>Outside parties · catalogue {map.catalogue_version}</p>
      <div className="parties">
        {shown.map(({ item: p, phase }) => {
          const svc = [...new Set(p.services.filter((sv) => stackID(sv) === stack || ci(sv)).map((sv) => (ci(sv) ? `${ciLabel(sv).split(", stage ")[0]} (built in CI)` : tail(sv))))];
          const kinds = [...new Set(p.signals.map((g) => g.kind))].join(" + ");
          return (
            <div key={p.id} className={`party${p.grade === "single" ? " single" : ""}${phase === "stay" ? "" : ` ${phase}`}`} aria-hidden={phase === "leave" || undefined}>
              <b>{p.name}</b>
              <span>{svc.length ? svc.join(", ") : "no service"} · {kinds}</span>
            </div>
          );
        })}
      </div>
    </>
  );
}
