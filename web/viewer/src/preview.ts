// What the "Prepare an upload" panel shows and names; no React, so node tests it.

// The first `n` lines of a JSON file as pretty text, with a last line saying how many
// more there are. Only the preview is pretty: the saved file is the canonical text.
export function preview(text: string, n: number): string {
  const lines = JSON.stringify(JSON.parse(text), null, 2).split("\n");
  if (lines.length <= n) return lines.join("\n");
  return lines.slice(0, n).join("\n") + `\n… ${(lines.length - n).toLocaleString("en")} more lines`;
}

// A scan's name without the .json or .facts.json of the file it was opened from.
export const baseName = (name: string) => name.replace(/(\.facts)?\.json$/, "");

export const counts = (c: Record<string, number>) =>
  [["path", "paths"], ["name", "names"], ["host", "hosts"], ["repo", "repo id"]].map(([k, label]) => `${(c[k!] ?? 0).toLocaleString("en")} ${label}`).join(", ");
