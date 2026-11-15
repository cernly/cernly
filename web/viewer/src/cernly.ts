import type { DependencySummary } from "@cernly/views/deps-model";
import type { DriftChange, DriftGroup, Explain, MapDoc, Scan } from "@cernly/views/types";
import type { Folder } from "./folder";

// boot.js starts the WebAssembly module (core/cmd/cernly-viewer) in a Worker,
// puts `cernly` on globalThis and fires "cernly-ready". Every call is a
// promise. When the module did not start, `failed` says why.
type Raw = {
  view(facts: string): Promise<View>;
  compare(a: string, b: string): Promise<Changes>;
  scan(source: Folder, name: string): Promise<Scanned>;
  maxBytes?: number;
  maxFiles?: number;
  hash?: string;
  plugins?: string[];
  failed?: string;
};

export type View = { scan: Scan; map: MapDoc; explain: Explain[]; stack_labels: Record<string, string>; dependencies: DependencySummary };
export type Changes = {
  from: View;
  to: View;
  groups: DriftGroup[];
  map_groups: DriftGroup[];
  drift: { facts: DriftChange[]; map: DriftChange[] };
};
// A browser scan: the facts file as `cernly scan` writes it, its warnings and
// its view.
export type Scanned = { facts: string; warnings: string[]; view: View; git: boolean };

const raw = () => (globalThis as unknown as { cernly?: Raw }).cernly;

let loaded: Promise<Raw> | undefined;
export function ready(): Promise<Raw> {
  loaded ??= new Promise((resolve, reject) => {
    const settle = (r: Raw) => (r.failed ? reject(new Error(r.failed)) : resolve(r));
    const now = raw();
    if (now) return settle(now);
    globalThis.addEventListener("cernly-ready", () => settle(raw()!), { once: true });
  });
  return loaded;
}

// The largest file the module reads (localview.MaxBytes), checked before a
// file is read into memory.
export async function tooLarge(files: { name: string; size: number }[]): Promise<string | undefined> {
  const max = (await ready()).maxBytes;
  const big = max ? files.find((f) => f.size > max) : undefined;
  return big && `${big.name} is ${Math.ceil(big.size / 2 ** 20)} MB, more than the ${max! / 2 ** 20} MB this viewer reads.`;
}

export const view = async (facts: string) => (await ready()).view(facts);
export const compare = async (a: string, b: string) => (await ready()).compare(a, b);
export const scan = async (f: Folder) => (await ready()).scan(f, f.name);
