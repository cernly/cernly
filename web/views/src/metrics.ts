// Surface metrics: the headline of the overview, with a trend
// against an earlier week or a baseline. Size is context, never a headline.
import type { EndpointCounts, MapDoc } from "./types";

export type Coupling = { packages: number; imports: number; max_fan_in: number; max_fan_out: number; cycles: number; depth: number };
export type Size = { files: number; lines: number; test_files?: number; test_lines?: number; languages: { language: string; files: number; lines: number }[] };
// What each count counts (rules 0.10.0), so a number traces to its facts.
export type ServiceItems = {
  outside_parties: string[];
  data_stores: string[];
  columns_to_check: string[];
  unpinned_images: { subject: string; detail: string }[];
  routes: string[];
};
export type Surface = {
  outside_parties: number;
  data_stores: number;
  columns_to_check: number;
  unpinned_images: number;
  production_stacks: number;
  routes: number;
  size: Size;
  coupling?: Coupling;
  items?: ServiceItems & { production_stacks: string[] };
};
export type ServiceMetrics = Omit<Surface, "production_stacks" | "items"> & { service: string; role: string; endpoints?: EndpointCounts; items?: ServiceItems };
export type Metrics = { total: Surface; production: Surface; services: ServiceMetrics[] };

export type Package = { package: string; language: string; dir: string; module?: string; fan_in: number; fan_out: number; depth: number; cycle?: boolean };
export type ImportEdge = { from: string; to: string; lines: number; facts: string[] };
export type ModuleGraph = {
  packages: Package[];
  edges: ImportEdge[];
  cycles: { packages: string[]; from: string[] }[];
  services: { service: string; reach: "entrypoints" | "sources"; roots?: string[]; packages: string[]; from: string[] }[];
};

type Key = "outside_parties" | "data_stores" | "columns_to_check" | "unpinned_images" | "production_stacks" | "routes";

// The headline, in this order.
export const HEADLINE: { key: Key; label: string; hint: string }[] = [
  { key: "outside_parties", label: "Outside parties", hint: "Suppliers the code, config or images point at" },
  { key: "data_stores", label: "Data stores", hint: "Databases, caches and queues in the stacks" },
  { key: "columns_to_check", label: "Columns to check", hint: "Column names that look sensitive" },
  { key: "unpinned_images", label: "Unpinned images", hint: "Image references not pinned by digest" },
  { key: "production_stacks", label: "Production stacks", hint: "Stacks a job on the default branch deploys" },
  { key: "routes", label: "Routes", hint: "Ways in through the web servers" },
];

export const metricsOf = (m: MapDoc | null | undefined): Metrics | null => m?.metrics ?? null;
export const modulesOf = (m: MapDoc | null | undefined): ModuleGraph | null => m?.modules ?? null;

// The scope the headline counts: production when the repo has a production
// stack, else everything.
export function scopeOf(x: Metrics): { scope: "production" | "total"; s: Surface } {
  return x.total.production_stacks > 0 ? { scope: "production", s: x.production } : { scope: "total", s: x.total };
}

// The change of one metric against an earlier map's same scope; null when the
// earlier map has no metrics (rules before 0.8.0) or there is none.
export function delta(now: Metrics, before: Metrics | null, key: Key): number | null {
  if (!before) return null;
  const { scope } = scopeOf(now);
  return now[scope][key] - before[scope][key];
}

// A change in words: "+2", "-1", "no change".
export function deltaWords(d: number | null): string {
  if (d === null) return "";
  if (d === 0) return "no change";
  return d > 0 ? `+${d}` : `−${-d}`;
}

// Lines read short: 12,345 -> "12.3k".
export function short(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

// Languages in a sentence, largest first: "go 12.3k, typescript 4.1k and 2 more".
export function languages(s: Size, top = 3): string {
  const ls = s.languages.slice(0, top).map((l) => `${l.language} ${short(l.lines)}`);
  const more = s.languages.length - top;
  return ls.join(", ") + (more > 0 ? ` and ${more} more` : "");
}

// The test code among the lines, as words: "of which 1.2k test lines"; empty
// without any.
export function testLines(s: Size): string {
  return s.test_lines ? `of which ${short(s.test_lines)} test lines` : "";
}

// Layers for drawing a graph: by depth, deepest (imported most) on the
// right, each layer sorted by name. Only the packages given.
export function layers(g: ModuleGraph, only?: string[]): Package[][] {
  const keep = only ? new Set(only) : null;
  const ps = g.packages.filter((p) => !keep || keep.has(p.package));
  const max = Math.max(0, ...ps.map((p) => p.depth));
  const out: Package[][] = Array.from({ length: max + 1 }, () => []);
  for (const p of ps) out[max - p.depth]!.push(p);
  for (const l of out) l.sort((a, b) => a.package.localeCompare(b.package));
  return out.filter((l) => l.length > 0);
}

// A package's name without the part every package shares.
export function trimPrefix(names: string[]): (p: string) => string {
  if (names.length < 2) return (p) => p.split("/").slice(-2).join("/");
  let pre = names[0]!.split("/");
  for (const n of names.slice(1)) {
    const parts = n.split("/");
    let i = 0;
    while (i < pre.length && i < parts.length - 1 && pre[i] === parts[i]) i++;
    pre = pre.slice(0, i);
  }
  const cut = pre.join("/");
  return (p) => (cut && p.startsWith(cut + "/") ? p.slice(cut.length + 1) : p) || p;
}
