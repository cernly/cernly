import { useCallback, useMemo, useState } from "react";
import { AISetup } from "@cernly/views/ai";
import { DependenciesPanel, DependencyTiles } from "@cernly/views/deps";
import { short, tail } from "@cernly/views/format";
import { modulesOf } from "@cernly/views/metrics";
import { ModuleGraphView } from "@cernly/views/modules";
import { OwnHosts, RoutesPanels, UnclaimedHosts } from "@cernly/views/routes";
import { AllTables, DataSummary, TableDrawer } from "@cernly/views/tables";
import { tableGroups } from "@cernly/views/tables-model";
import type { View } from "./cernly";

// The page knows no source host: evidence stays path:line text.
const nowhere = () => undefined;

const of = (v: View) => short(v.scan.commit.sha);

export function Routes({ v }: { v: View }) {
  return (
    <>
      <RoutesPanels map={v.map} labels={v.stack_labels} title={of(v)} />
      <UnclaimedHosts map={v.map} facts={v.scan.facts} where={nowhere} />
      <OwnHosts map={v.map} note="Served by the repo's routes or declared in .cernly.yml" />
    </>
  );
}

export function Data({ v }: { v: View }) {
  const [picked, setPicked] = useState<string | null>(null);
  const close = useCallback(() => setPicked(null), []);
  const { groups, checked, total } = useMemo(() => tableGroups(v.map, v.scan.facts, null), [v]);
  const rows = groups.flatMap((g) => g.tables);
  const current = rows.find((t) => t.table === picked);
  return (
    <>
      <DataSummary scan={v.scan} rows={rows} groups={groups} total={total} checked={checked} />
      <AllTables groups={groups} total={total} checked={checked} of={of(v)} since="one scan: nothing marked new" picked={picked} onPick={setPicked} where={nowhere} />
      {current && <TableDrawer t={current} where={nowhere} onClose={close} />}
    </>
  );
}

export function Modules({ v }: { v: View }) {
  const [service, setService] = useState("");
  const facts = useMemo(() => v.scan.facts.filter((f) => f.kind === "import"), [v]);
  const services = modulesOf(v.map)?.services ?? [];
  const pick = services.length > 0 && (
    <span className="stack-pick">
      <label className="label" htmlFor="module-service">Service</label>
      <select id="module-service" value={service} onChange={(e) => setService(e.target.value)}>
        <option value="">All packages</option>
        {services.map((s) => <option key={s.service} value={s.service}>{tail(s.service)}</option>)}
      </select>
    </span>
  );
  return <ModuleGraphView map={v.map} facts={facts} service={service} back={pick} where={nowhere} foot={<>Scan {of(v)} · rules {v.map.rules_version}</>} />;
}

export function Dependencies({ v }: { v: View }) {
  const d = v.dependencies;
  return (
    <>
      <DependencyTiles counts={d.counts} />
      <DependenciesPanel services={d.services} title={of(v)} where={nowhere} />
    </>
  );
}

export function AI({ v }: { v: View }) {
  return (
    <>
      <AISetup tools={v.map.ai_tools} of={of(v)} empty="No AI tool configuration files in this scan." />
      <p className="panel panel-body muted">Which change an AI tool wrote needs the history of changes, which a single scan does not have.</p>
    </>
  );
}
