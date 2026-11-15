// The floor plan of one stack, laid out deterministically: the
// same scan and map always give the same picture. Nodes are the stack's
// services and data stores; edges are routes (the proxy to the service it
// sends to) and depends_on. Columns by longest path from the nodes nothing
// points at, data stores always last; within a column by name. Back edges of
// a cycle are drawn but do not move a node.

import type { Fact, MapDoc } from "./types";

export type Node = {
  key: string;
  name: string;
  kind: "service" | "datastore";
  image?: string;
  linked: boolean; // built from the repo's code
  thirdParty: boolean;
  floating: boolean; // image on a floating or unreadable tag
  sub: string; // the second line on the box
  col: number;
  row: number;
  x: number;
  y: number;
};

export type Edge = { from: string; to: string; kind: "route" | "depends_on"; label?: string };

export type Plan = { nodes: Node[]; edges: Edge[]; width: number; height: number };

export const BOX = { w: 140, h: 60, colGap: 46, rowGap: 22, pad: 12 };

// floorPlan lays out the stack with id <tool>:<name> (stacksOf).
export function floorPlan(facts: Fact[], map: MapDoc, id: string): Plan {
  const members = facts.filter((f) => (f.kind === "service" || f.kind === "datastore") && stackID(f.key) === id);
  const byName = new Map(members.map((f) => [f.attrs.name as string, f]));
  const linked = new Set(map.links.map((l) => l.service));
  const third = new Set(map.images.filter((i) => i.class === "third_party").map((i) => i.service));
  const floating = new Set(map.findings.filter((f) => f.rule === "image_pinning" && f.grade !== "exact").map((f) => f.subject));

  const edges: Edge[] = [];
  const seen = new Set<string>();
  const add = (e: Edge) => {
    const id = `${e.from}>${e.to}`;
    if (e.from === e.to || seen.has(id)) return;
    seen.add(id);
    edges.push(e);
  };
  for (const r of map.routes ?? []) {
    if (r.stack !== `stack:${id}` || !r.via || !r.to) continue;
    add({ from: r.via, to: r.to, kind: "route" });
  }
  for (const f of members) {
    for (const d of (f.attrs.depends_on as string[] | undefined) ?? []) {
      const t = byName.get(d);
      if (t) add({ from: f.key, to: t.key, kind: "depends_on" });
    }
  }
  edges.sort((a, b) => (a.from + a.to < b.from + b.to ? -1 : 1));

  // Longest-path layering over a DAG; cycles are cut by visiting in name order.
  const keys = members.map((f) => f.key).sort();
  const out = new Map<string, string[]>(keys.map((k) => [k, []]));
  for (const e of edges) out.get(e.from)?.push(e.to);
  const layer = new Map<string, number>();
  const onPath = new Set<string>();
  const indeg = new Map<string, number>(keys.map((k) => [k, 0]));
  for (const e of edges) indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1);
  const visit = (k: string, depth: number) => {
    if (onPath.has(k)) return; // back edge
    if ((layer.get(k) ?? -1) >= depth) return;
    layer.set(k, depth);
    onPath.add(k);
    for (const t of out.get(k) ?? []) visit(t, depth + 1);
    onPath.delete(k);
  };
  for (const k of keys) if ((indeg.get(k) ?? 0) === 0) visit(k, 0);
  for (const k of keys) if (!layer.has(k)) visit(k, 0);
  const isStore = (k: string) => k.startsWith("datastore:");
  const maxApp = Math.max(0, ...keys.filter((k) => !isStore(k)).map((k) => layer.get(k) ?? 0));
  for (const k of keys) if (isStore(k)) layer.set(k, maxApp + 1);

  const cols = new Map<number, string[]>();
  for (const k of keys) {
    const c = layer.get(k) ?? 0;
    cols.set(c, [...(cols.get(c) ?? []), k]);
  }
  const colIndex = [...cols.keys()].sort((a, b) => a - b);
  const nodes: Node[] = [];
  let rows = 0;
  colIndex.forEach((c, ci) => {
    const ks = (cols.get(c) ?? []).sort((a, b) => nameOf(a).localeCompare(nameOf(b)));
    rows = Math.max(rows, ks.length);
    ks.forEach((k, ri) => {
      const f = members.find((m) => m.key === k)!;
      const kind = f.kind as Node["kind"];
      const image = f.attrs.image as string | undefined;
      const ref = f.attrs.image_ref as { tag?: string; repository?: string } | undefined;
      const sub =
        kind === "datastore"
          ? `${f.attrs.engine ?? "data store"}${f.attrs.version ? " " + f.attrs.version : ""}`
          : linked.has(k)
            ? "built from the repo"
            : ref?.tag
              ? `${(ref.repository ?? "").split("/").pop()}:${ref.tag}`
              : image ?? "";
      nodes.push({
        key: k, name: f.attrs.name, kind, image,
        linked: linked.has(k), thirdParty: third.has(k), floating: floating.has(k), sub,
        col: ci, row: ri,
        x: BOX.pad + ci * (BOX.w + BOX.colGap),
        y: BOX.pad + ri * (BOX.h + BOX.rowGap),
      });
    });
  });
  return {
    nodes,
    edges,
    width: BOX.pad * 2 + Math.max(1, colIndex.length) * BOX.w + Math.max(0, colIndex.length - 1) * BOX.colGap,
    height: BOX.pad * 2 + Math.max(1, rows) * BOX.h + Math.max(0, rows - 1) * BOX.rowGap,
  };
}

function nameOf(key: string) {
  return key.slice(key.lastIndexOf(":") + 1);
}

// The stacks of a scan, the production one first, then by size.
// stacksOf lists the map's stacks for the picker. name is the stack's id,
// <tool>:<name>, since a helm and a kubernetes stack can share a name
// (validation-1); stack is the name in the scan, and shared says another
// tool's stack has it too, so the picker adds the tool.
export function stacksOf(facts: Fact[], map: MapDoc): { name: string; stack: string; tool: string; shared: boolean; role: string; size: number }[] {
  const size = new Map<string, number>();
  for (const f of facts) {
    if (f.kind === "service" || f.kind === "datastore") {
      const id = stackID(f.key);
      size.set(id, (size.get(id) ?? 0) + 1);
    }
  }
  const all = map.stacks.map((s) => {
    const [, tool = "", ...rest] = s.stack.split(":");
    const stack = rest.join(":");
    return { name: `${tool}:${stack}`, stack, tool, shared: false, role: s.role, size: size.get(`${tool}:${stack}`) ?? 0 };
  });
  for (const s of all) s.shared = all.some((o) => o !== s && o.stack === s.stack);
  return all.sort((a, b) => (a.role === "none") !== (b.role === "none") ? (a.role === "none" ? 1 : -1) : b.size - a.size || a.name.localeCompare(b.name));
}

// stackID is <tool>:<stack> of a service or data store key
// (service:<tool>:<stack>:<name>), "" for another key.
export function stackID(key: string): string {
  const p = key.split(":");
  return (p[0] === "service" || p[0] === "datastore") && p.length >= 4 ? `${p[1]}:${p[2]}` : "";
}

// CI_VIEW is the floor plan's picker value for the "Built in CI" group
// (an images group, not a stack). Compose stack names never start with "#".
export const CI_VIEW = "#ci";

// openView is what the floor plan opens on: the production stack when there
// is one (confirmed, unconfirmed or declared); else the "Built in CI" group
// when CI builds an image that is not test-only, since that is where the
// application is; else the largest stack. stacks is in stacksOf's order.
export function openView(stacks: { name: string; role: string }[], ciImages: { test_only: boolean }[]): string {
  if (stacks.some((s) => s.role !== "none")) return stacks.find((s) => s.role !== "none")!.name;
  if (ciImages.some((c) => !c.test_only)) return CI_VIEW;
  return stacks[0]?.name ?? "";
}
