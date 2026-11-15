import type { DriftChange } from "./types";

// What a changed item changed, as drift.Change.Diff writes it: each
// top-level field that differs, field: old → new.
export function driftDiff(c: DriftChange): string {
  if (c.change !== "changed") return "";
  const b = (c.before ?? {}) as Record<string, unknown>;
  const a = (c.after ?? {}) as Record<string, unknown>;
  const show = (v: unknown) => (v === undefined || v === null || v === "" ? "none" : typeof v === "string" ? v : JSON.stringify(v));
  return [...new Set([...Object.keys(b), ...Object.keys(a)])]
    .sort()
    .filter((k) => JSON.stringify(b[k]) !== JSON.stringify(a[k]))
    .map((k) => `${k}: ${show(b[k])} → ${show(a[k])}`)
    .join("; ");
}
