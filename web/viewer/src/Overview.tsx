import { useMemo, useState } from "react";
import { floorPlan, openView, stacksOf, CI_VIEW } from "@cernly/views/floorplan";
import { stackLabel, plural } from "@cernly/views/format";
import { metricsOf } from "@cernly/views/metrics";
import { FloorPlan, Parties, Surface } from "@cernly/views/overview";
import { AttentionList, Drawer, Info, Verdict, type Attention } from "@cernly/views/ui";
import type { Explain, MapDoc } from "@cernly/views/types";
import type { View } from "./cernly";

// The map's findings grouped by rule, each subject read with its stack's label.
function attention(map: MapDoc, labels: Record<string, string>): Attention[] {
  const by = new Map<string, Attention>();
  for (const f of map.findings) {
    const a = by.get(f.rule) ?? { rule: f.rule, count: 0, items: [] };
    a.count++;
    const m = /^(service|datastore|stack):compose:([^:]+)(.*)$/.exec(f.subject);
    const what = m ? stackLabel(labels, m[2]!) + m[3]!.replace(/:/g, " · ") : f.rule === "driver_without_store" ? f.subject.slice(f.subject.lastIndexOf(":") + 1) : f.subject.replace(/:/g, " · ");
    a.items.push(<><b>{what}</b>{f.grade ? <span className="muted"> · {f.grade.replace(/_/g, " ")}</span> : null}{f.detail ? <span className="muted"> · {f.detail}</span> : null}</>);
    by.set(f.rule, a);
  }
  return [...by.values()];
}

export function Overview({ v }: { v: View }) {
  const { scan, map, stack_labels: labels } = v;
  const explain = useMemo(() => new Map<string, Explain>(v.explain.map((r) => [r.id, r])), [v.explain]);
  const items = useMemo(() => attention(map, labels), [map, labels]);
  const stacks = useMemo(() => stacksOf(scan.facts, map), [scan, map]);
  const ciImages = map.ci_images ?? [];
  const [pick, setPick] = useState<string | null>(null);
  const stack = pick === CI_VIEW && ciImages.length ? CI_VIEW : stacks.find((x) => x.name === pick)?.name ?? openView(stacks, ciImages);
  const ciView = stack === CI_VIEW;
  const plan = useMemo(() => (ciView ? null : floorPlan(scan.facts, map, stack)), [scan, map, stack, ciView]);
  const [picked, setPicked] = useState<string | null>(null);
  const selected = plan?.nodes.find((x) => x.key === picked);
  const metrics = metricsOf(map);
  const when = scan.commit.committed_at.slice(0, 10);

  return (
    <>
      <Verdict items={items} explain={explain} of={`${scan.repo} · ${scan.commit.sha.slice(0, 7)} · all stacks`} clear="Nothing to act on or confirm in this scan." />
      {metrics && <Surface now={metrics} before={null} name="" />}
      <section id="attention" aria-label="Needs attention" className="panel">
        <div className="panel-head">
          <h2>Needs attention</h2>
          <span className="label">{plural(map.findings.length, "finding")} in all stacks</span>
        </div>
        <AttentionList items={items} explain={explain} empty="Nothing needs attention in this scan." />
      </section>
      <section aria-label="Architecture map" className="panel" style={{ display: "flex", flexDirection: "column" }}>
        <div className="panel-head">
          <h2>Floor plan<Info label="About the floor plan"><span>Each box is a service or data store in the stack you pick; click one for what it is and where it is defined.</span><span>Lines are routes; dashed lines are depends_on.</span></Info></h2>
          <div className="stack-pick">
            <label className="label" htmlFor="stack">Stack</label>
            <select id="stack" value={stack} onChange={(e) => { setPick(e.target.value); setPicked(null); }}>
              {stacks.map((x) => <option key={x.name} value={x.name}>{stackLabel(labels, x.stack)}{x.shared ? ` (${x.tool})` : ""} · {x.size} · {x.role}</option>)}
              {ciImages.length > 0 && <option value={CI_VIEW}>Built in CI · {ciImages.length} · no stack</option>}
            </select>
          </div>
        </div>
        {ciView ? (
          <div style={{ padding: 16 }}>
            <p className="muted">No production stack in this scan. The images CI builds:</p>
            <ul className="evidence" style={{ marginTop: 8 }}>{ciImages.map((c) => <li key={c.image}>{c.image_repos.find((r) => !r.includes("$")) ?? c.dockerfile}</li>)}</ul>
          </div>
        ) : plan && plan.nodes.length > 0 ? (
          <div className="scroll-x" style={{ padding: 16 }}><FloorPlan plan={plan} picked={selected?.key} onPick={setPicked} /></div>
        ) : (
          <p className="empty">{stacks.length === 0 ? "This scan has no stacks." : "This stack has no services."}</p>
        )}
        <Parties map={map} stack={stack} />
        <p className="panel-foot mono" style={{ fontSize: 12 }}>Scan {scan.commit.sha.slice(0, 7)} of {when} · rules {map.rules_version} · catalogue {map.catalogue_version} · edges: routes and depends_on</p>
      </section>
      {selected && <Selected v={v} nodeKey={selected.key} onClose={() => setPicked(null)} />}
    </>
  );
}

// A picked box: what it is and the facts' evidence as path:line text. The page
// knows no source host, so there are no links.
function Selected({ v, nodeKey, onClose }: { v: View; nodeKey: string; onClose: () => void }) {
  const f = v.scan.facts.find((x) => x.key === nodeKey);
  if (!f) return null;
  const labels = v.stack_labels;
  const findings = v.map.findings.filter((x) => x.subject === nodeKey);
  const parties = v.map.parties.filter((p) => p.services.includes(nodeKey));
  const proof = [...f.evidence].sort((a, b) => Number(b.unit === f.attrs.name) - Number(a.unit === f.attrs.name));
  return (
    <Drawer title={String(f.attrs.name)} label={`${f.kind === "datastore" ? "Data store" : "Service"}${f.attrs.stack ? ` · stack ${stackLabel(labels, f.attrs.stack)}` : ""}`} onClose={onClose} returnTo={`[data-pick="${CSS.escape(nodeKey)}"]`}>
      <div className="aside-section">
        <h3>Facts</h3>
        <ul className="evidence">
          <li>{f.kind}</li>
          {f.attrs.image && <li>image {String(f.attrs.image)}</li>}
          {parties.map((p) => <li key={p.id}>outside party {p.name}</li>)}
        </ul>
      </div>
      <div className="aside-section">
        <h3>Evidence</h3>
        <ul className="evidence">
          {proof.slice(0, 20).map((e, k) => <li key={k}>{e.path}:{e.line}{e.unit ? <span className="muted"> · {e.unit}</span> : null}</li>)}
          {proof.length > 20 && <li className="muted">and {proof.length - 20} more</li>}
        </ul>
      </div>
      {findings.length > 0 && (
        <div className="aside-section">
          <h3>Findings</h3>
          <ul className="evidence">{findings.map((x, k) => <li key={k}>{x.rule.replace(/_/g, " ")}{x.grade ? ` · ${x.grade.replace(/_/g, " ")}` : ""}{x.detail ? <span className="muted"> · {x.detail}</span> : null}</li>)}</ul>
        </div>
      )}
    </Drawer>
  );
}
