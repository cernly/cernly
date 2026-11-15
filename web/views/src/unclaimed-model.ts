// Hosts nobody claims, as rows for the Routes tab: the open ones (called or
// configured, not yet decided), the ones a person decided in the app, and
// the ones only mentioned. Pure: tested with `npm test`
// (unclaimed.test.mjs). Every row carries the facts it rests on, so
// the page can link each to its file and line.
import type { ConfirmRef, Evidence, Fact, MapDoc } from "./types";

export type Source = { fact: string; services: string[]; evidence: Evidence[] };
export type HostRow = {
  host: string; // the host, or for an own-domain claim the domain
  hosts: string[]; // the hosts a row covers (more than one for an own-domain claim)
  subject: string; // the confirmation subject: host:<host or domain>
  usage: string; // the strongest usage of its facts: request, default, env_hint, mention
  sources: Source[];
  confirmed?: ConfirmRef;
  state?: "approved" | "not_supplier" | "own";
};

const RANK: Record<string, number> = { mention: 1, env_hint: 2, default: 3, request: 4 };

// How a fact names a host: an outbound_host fact says its usage; an env name
// whose example value is the host is env_hint (as the rules grade it).
function usageOf(f: Fact | undefined): string {
  if (!f) return "";
  if (f.kind === "outbound_host") return String(f.attrs.usage ?? "mention");
  if (f.kind === "env_var") return "env_hint";
  return "mention";
}

export function strongest(usages: string[]): string {
  return usages.reduce((a, b) => ((RANK[b] ?? 0) > (RANK[a] ?? 0) ? b : a), "");
}

export function unclaimedRows(map: MapDoc, facts: Fact[]) {
  const byKey = new Map(facts.map((f) => [f.key, f]));
  const servicesOf = new Map<string, string[]>();
  for (const a of map.attached ?? []) servicesOf.set(a.fact, [...(servicesOf.get(a.fact) ?? []), a.service]);
  const sources = (from: string[]): Source[] =>
    [...new Set(from)].sort().map((k) => ({ fact: k, services: [...new Set(servicesOf.get(k) ?? [])].sort(), evidence: byKey.get(k)?.evidence ?? [] }));
  const usage = (from: string[], given?: string) => given ?? strongest(from.map((k) => usageOf(byKey.get(k))));

  const open: HostRow[] = [];
  const decided: HostRow[] = [];
  const mentioned: HostRow[] = [];
  for (const u of map.unknown) {
    const row: HostRow = { host: u.host, hosts: [u.host], subject: `host:${u.host}`, usage: u.usage, sources: sources(u.from), confirmed: u.confirmed };
    if (u.confirmed) decided.push({ ...row, state: "approved" });
    else if (u.usage === "mention") mentioned.push(row);
    else open.push(row);
  }
  for (const d of map.dismissed ?? []) {
    if (d.kind !== "host") continue;
    decided.push({ host: d.name, hosts: [d.name], subject: d.subject, usage: usage(d.from), sources: sources(d.from), confirmed: d.confirmed, state: "not_supplier" });
  }
  // One own-domain claim can cover several hosts: one row per claim.
  const claims = new Map<number, HostRow>();
  for (const o of map.own) {
    if (!o.confirmed) continue;
    const c = claims.get(o.confirmed.id);
    if (c) {
      c.hosts.push(o.host);
      c.sources = sources([...c.sources.map((s) => s.fact), ...o.from]);
      c.usage = strongest([c.usage, usage(o.from)]);
    } else {
      claims.set(o.confirmed.id, { host: o.domain, hosts: [o.host], subject: `host:${o.domain}`, usage: usage(o.from), sources: sources(o.from), confirmed: o.confirmed, state: "own" });
    }
  }
  decided.push(...claims.values());
  const byHost = (a: HostRow, b: HostRow) => a.host.localeCompare(b.host);
  return { open: open.sort(byHost), decided: decided.sort(byHost), mentioned: mentioned.sort(byHost) };
}

// The chip: how the code uses the host, in words. Only request and default
// say "called".
export const USAGE_CHIP: Record<string, string> = {
  request: "called",
  default: "called (default address)",
  env_hint: "example value of an env name",
  mention: "only mentioned",
};

export const STATE_WORDS: Record<NonNullable<HostRow["state"]>, string> = {
  approved: "Approved supplier",
  not_supplier: "Not a supplier",
  own: "Our own domain",
};
