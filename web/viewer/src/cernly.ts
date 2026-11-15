import type { DependencySummary } from "@cernly/views/deps-model";
import type { DriftChange, DriftGroup, Explain, MapDoc, Scan } from "@cernly/views/types";

// The WebAssembly module (core/cmd/cernly-viewer) puts `cernly` on globalThis
// and fires "cernly-ready". Both calls take and return JSON text. When the
// module did not start, the page's loader sets `failed` instead.
type Raw = { view(facts: string): string; compare(a: string, b: string): string; maxBytes?: number; failed?: string };

export type View = { scan: Scan; map: MapDoc; explain: Explain[]; stack_labels: Record<string, string>; dependencies: DependencySummary };
export type Changes = {
  from: View;
  to: View;
  groups: DriftGroup[];
  map_groups: DriftGroup[];
  drift: { facts: DriftChange[]; map: DriftChange[] };
};

const raw = () => (globalThis as unknown as { cernly?: Raw }).cernly;

let loaded: Promise<Raw> | undefined;
function ready(): Promise<Raw> {
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

function parse<T>(text: string): T {
  const out = JSON.parse(text) as T | { error: string };
  if (out && typeof out === "object" && "error" in out && typeof out.error === "string") throw new Error(out.error);
  return out as T;
}

export const view = async (facts: string) => parse<View>((await ready()).view(facts));
export const compare = async (a: string, b: string) => parse<Changes>((await ready()).compare(a, b));
