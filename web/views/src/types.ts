import type { Metrics, ModuleGraph } from "./metrics";

// Types of the documents the viewer and the app read: the scan (scan.v1), the
// map (map.v1) and drift/v1, and the words for a finding. Only the fields the
// UI reads.

export type Evidence = { path: string; line: number; end_line?: number; unit?: string };
export type Fact = { kind: string; key: string; attrs: Record<string, any>; plugin: string; evidence: Evidence[] };
export type PluginRun = { name: string; version: string; status: string };
export type Scan = { repo: string; commit: { sha: string; committed_at: string; dirty?: boolean }; plugins?: PluginRun[]; facts: Fact[] };

export type StackRole = { stack: string; name?: string; role: string; derived_role?: string; declared?: { by: string; line?: number }; confirmed?: ConfirmRef; manual: boolean; reasons: { workflow: string; job: string; role: string; detail: string; test_job?: { by: string; match: string; line: number } }[] };
export type Link = { service: string; recipe: string; sources: string[]; contexts: string[] };
export type Image = { service: string; image_repo: string; class: string };
export type Party = { id: string; name: string; category: string; kind: string; grade: string; services: string[]; signals: { kind: string; fact: string }[]; confirmed?: ConfirmRef };
export type Finding = { rule: string; subject: string; grade?: string; detail: string; from: string[] };
export type RouteRewrite = { kind: string; prefix?: string; to?: string };
export type RouteEdge = { route: string; stack?: string; via?: string; sites?: string[]; match: string; action: string; upstream?: string; to?: string; rewrite?: RouteRewrite; match_type?: string };
// Endpoints of the app code (absent in maps from rules before 0.17.0 and without endpoint facts).
export type EndpointAuth = { state: "required" | "anonymous" | "unknown" | string; markers?: string[]; policy?: string };
export type EndpointRow = {
  endpoint: string; service?: string; method: string; path: string; framework: string;
  auth: EndpointAuth; routes: string[]; public: boolean; link?: "broad" | string; from: string[];
};
export type EndpointCounts = { total: number; required: number; anonymous: number; unknown: number };
export type OwnHost = { host: string; domain: string; served_by?: string[]; declared?: { by: string; line?: number }; confirmed?: ConfirmRef; from: string[] };
export type UnknownHost = { host: string; usage: string; from: string[]; confirmed?: ConfirmRef };

export type ConfirmKind = "stack_role" | "party" | "host" | "gate_exception" | "driver";
export type ConfirmRef = { id: number; value: string; by: string; at: string; overridden?: boolean };

export type Dismissed = { subject: string; kind: "party" | "host"; name: string; confirmed: ConfirmRef; from: string[] };
export type ConfirmationUse = { id: number; kind: ConfirmKind; subject: string; value: string; reason: string; by: string; at: string; status: "applied" | "overridden" | "no_subject" | "ignored" };

export type TestJob = { by: string; match: string; line: number };
// An image a CI job builds that no Compose service runs. Never production.
export type CIImage = {
  image: string;
  recipe: string;
  dockerfile: string;
  target: string;
  image_repos: string[];
  builds: { build: string; workflow: string; job: string; job_name?: string; job_line?: number; line?: number; events: string[]; branches: string[]; test_job?: TestJob }[];
  pushes: { target: string; by: "push" | "docker_push"; build: string; workflow?: string; job?: string; line?: number }[];
  contexts: string[];
  sources: string[];
  test_only: boolean;
  deployment: string;
  detail: string;
};
// A table merged across schema sources, with columns whose name looks
// sensitive (absent in maps from rules before 0.5.0).
export type SensitiveColumn = { column: string; word: string; from: string[] };
export type MapTable = { table: string; schema?: string; from: string[]; check?: SensitiveColumn[] };
// Test suites per service and the commands that run them (absent in maps from rules before 0.8.0 and without test files).
export type TestPresence = {
  service?: string; suite: string; language: string; framework: string; files: number;
  runs: string[]; jobs?: { workflow: string; job: string }[]; from: string[];
};
export type Attached = { service: string; fact: string; kind: string; scope?: string; source: string };

export type MapDoc = {
  rules_version: string;
  catalogue_version: string;
  stacks: StackRole[];
  links: Link[];
  images: Image[];
  ci_images?: CIImage[]; // absent in maps from rules before 0.3.0
  attached?: Attached[];
  parties: Party[];
  findings: Finding[];
  routes?: RouteEdge[];
  endpoints?: EndpointRow[]; // absent in maps from rules before 0.17.0
  own: OwnHost[];
  unknown: UnknownHost[];
  ai_tools: { tool: string; name: string; files: number }[];
  confirmations?: ConfirmationUse[]; // absent without confirmations
  dismissed?: Dismissed[];
  tables?: MapTable[];
  tests?: TestPresence[];
  metrics?: Metrics; // absent in maps from rules before 0.8.0
  modules?: ModuleGraph; // absent without import facts
};

export type DriftChange = { change: "added" | "removed" | "changed"; section: string; key: string; before?: unknown; after?: unknown };
export type DriftGroup = { section: string; label: string; one: string; added: number; removed: number; changed: number; examples: string[] };
export type Drift = { groups: DriftGroup[]; map_groups?: DriftGroup[]; drift: { facts: DriftChange[]; map: DriftChange[] } };

// What a finding means and what to do, the same words as the evidence report.
export type Explain = { id: string; title: string; meaning: string; action: string; level: "act" | "confirm" | "info" };
