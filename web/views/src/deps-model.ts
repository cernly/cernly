// The dependencies of one scan by service, as localview and the server send
// them, and the pure helpers the Dependencies view uses. Run the tests with
// `npm test` (deps.test.mjs).

export type Dep = {
  key: string;
  ecosystem: string;
  name: string;
  version?: string;
  scope?: string;
  licence?: string;
  direct: boolean;
  via?: string[];
  path: string;
  line: number;
  change?: "new" | "upgraded" | "downgraded" | "changed";
  was_version?: string;
};

export type ServiceDeps<D extends Dep = Dep> = { service: string; name: string; direct: D[]; transitive: D[] };

export type DependencySummary = { services: ServiceDeps[]; counts: Record<string, number> };

export type Filter = "all" | "changed" | "direct";

// filterServices keeps, per service, the dependencies the filter asks for;
// a service left with none is dropped. Transitive ones are kept only for
// "all" and "changed".
export function filterServices<D extends Dep>(services: ServiceDeps<D>[], filter: Filter, query: string): ServiceDeps<D>[] {
  const q = query.trim().toLowerCase();
  const keep = (d: D) => (filter !== "changed" || !!d.change) && (!q || d.name.toLowerCase().includes(q));
  return services
    .map((s) => ({ ...s, direct: s.direct.filter(keep), transitive: filter === "direct" ? [] : s.transitive.filter(keep) }))
    .filter((s) => s.direct.length + s.transitive.length > 0);
}

// changeText is the version change in words.
export function changeText(d: Dep): string {
  switch (d.change) {
    case "new": return "new";
    case "upgraded": return `upgraded from ${d.was_version}`;
    case "downgraded": return `downgraded from ${d.was_version}`;
    case "changed": return `was ${d.was_version || "unversioned"}`;
    default: return "";
  }
}

// licenceText: the declared licence, or "not declared". Nothing is looked up.
export const licenceText = (d: Dep) => d.licence || "not declared";
