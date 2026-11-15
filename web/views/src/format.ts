// Small pure helpers for reading keys, counts and links.

export const short = (sha: string) => sha.slice(0, 7);
export const n = (x: number) => x.toLocaleString("en-US");
// A count and its noun: plural(1, "file") is "1 file", plural(2, "file") "2 files";
// give the plural when it is not the noun plus s (plural(2, "party", "parties")).
export const plural = (x: number, one: string, many = one + "s") => `${n(x)} ${x === 1 ? one : many}`;
export const tail = (key: string) => key.slice(key.lastIndexOf(":") + 1);
export const stackOf = (key: string) => key.split(":")[2] ?? "";
// An image built in CI as a reader names it: "docker/Dockerfile", or "docker/Dockerfile, stage api".
export const ciLabel = (key: string) => {
  const k = key.replace(/^ci_image:/, "");
  const i = k.lastIndexOf(":");
  if (i < 0) return k;
  return k.slice(i + 1) === "_final" ? k.slice(0, i) : `${k.slice(0, i)}, stage ${k.slice(i + 1)}`;
};
// A stack as a reader sees it: its label (its compose folder, its workflow or
// its declared name), else its name. Keys and URLs keep the name.
export const stackLabel = (labels: Record<string, string> | undefined, name: string) => labels?.[name] || name;

// A link to a file at a line, at the scanned commit, when the repo is on GitHub.
export function sourceURL(repo: string, sha: string, path: string, line?: number): string | undefined {
  if (!repo.startsWith("github.com/")) return undefined;
  return `https://${repo}/blob/${sha}/${path.split("/").map(encodeURIComponent).join("/")}${line ? `#L${line}` : ""}`;
}
