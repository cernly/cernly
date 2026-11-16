import "@cernly/brand/fonts.css";
import "@cernly/brand/tokens.css";
import "@cernly/brand/base.css";
import "@cernly/views/views.css";
import "./viewer.css";
import { Component, StrictMode, useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { Mark } from "@cernly/brand/mark";
import { compare, ready, scan, tooLarge, view, type Changes as ChangesData, type Scanned as ScannedData, type View } from "./cernly";
import { Changes } from "./Changes";
import { kind, type Folder } from "./folder";
import { Open } from "./Open";
import { Confirm, Scanned, Scanning, type Kind } from "./Scan";
import { Overview } from "./Overview";
import { AI, Data, Dependencies, Modules, Routes } from "./Views";

// The theme follows the system; the brand tokens read data-theme on <html>.
const dark = globalThis.matchMedia?.("(prefers-color-scheme: dark)");
const setTheme = () => { document.documentElement.dataset.theme = dark?.matches ? "dark" : "light"; };
setTheme();
dark?.addEventListener("change", setTheme);

// The views of one scan, in the app's order; with two scans What changed
// follows Overview.
const TABS = [
  ["overview", "Overview", Overview],
  ["ai", "AI tooling", AI],
  ["routes", "Routes", Routes],
  ["data", "Data", Data],
  ["dependencies", "Dependencies", Dependencies],
  ["modules", "Modules", Modules],
] as const;
type Tab = (typeof TABS)[number][0] | "changes";

function Tabs({ tab, two, onTab }: { tab: Tab; two: boolean; onTab: (t: Tab) => void }) {
  const all: [Tab, string][] = TABS.map(([id, label]) => [id, label]);
  if (two) all.splice(1, 0, ["changes", "What changed"]);
  return (
    <nav className="seg tabs" aria-label="View">
      {all.map(([id, label]) => <button key={id} type="button" className="btn" aria-pressed={tab === id} onClick={() => onTab(id)}>{label}</button>)}
    </nav>
  );
}

function One({ v, tab }: { v: View; tab: Tab }) {
  const T = TABS.find(([id]) => id === tab)?.[2] ?? Overview;
  return <T v={v} />;
}

type State =
  | { at: "open" }
  | { at: "busy" }
  | { at: "error"; message: string }
  | { at: "confirm"; folder: Folder; kind: Kind; plugins: string[]; maxFiles: number }
  | { at: "scanning"; folder: Folder }
  | { at: "one"; v: View; name: string; scanned?: ScannedData }
  | { at: "kept"; name: string; scanned: ScannedData }
  | { at: "two"; c: ChangesData; names: string[] };

async function load(files: File[]): Promise<State> {
  if (files.length > 2) return { at: "error", message: "Open one or two scans." };
  let current = files[0]!.name;
  try {
    const big = await tooLarge(files);
    if (big) return { at: "error", message: big };
    const texts = await Promise.all(files.map((f) => f.text()));
    if (texts.length === 1) return { at: "one", v: await view(texts[0]!), name: current };
    current = `${files[0]!.name} and ${files[1]!.name}`;
    return { at: "two", c: await compare(texts[0]!, texts[1]!), names: files.map((f) => f.name) };
  } catch (e) {
    return { at: "error", message: `${current}: ${e instanceof Error ? e.message : String(e)}` };
  }
}

async function confirm(folder: Folder): Promise<State> {
  try {
    const [k, r] = await Promise.all([kind(folder), ready()]);
    return { at: "confirm", folder, kind: k, plugins: r.plugins ?? [], maxFiles: r.maxFiles ?? 0 };
  } catch (e) {
    return { at: "error", message: `${folder.name}: ${e instanceof Error ? e.message : String(e)}` };
  }
}

async function scanFolder(folder: Folder): Promise<State> {
  try {
    const s = await scan(folder);
    // A scan the page cannot show keeps its facts file to save.
    if (!s.view) return { at: "kept", name: folder.name, scanned: s };
    return { at: "one", v: s.view, name: folder.name, scanned: s };
  } catch (e) {
    return { at: "error", message: `${folder.name}: ${e instanceof Error ? e.message : String(e)}` };
  }
}

// A scan the views cannot draw shows its error and the file control, not a
// blank page. A new key (each load, and back to the file control) clears it.
class Guard extends Component<{ children: ReactNode; onFiles: (files: File[]) => void; onFolder: (f: Folder) => void }, { error?: string }> {
  state: { error?: string } = {};
  static getDerivedStateFromError(e: unknown) {
    return { error: e instanceof Error ? e.message : String(e) };
  }
  render() {
    if (this.state.error === undefined) return this.props.children;
    return (
      <>
        <p className="error" role="alert">This scan could not be shown: {this.state.error}</p>
        <Open onFiles={this.props.onFiles} onFolder={this.props.onFolder} />
      </>
    );
  }
}

function App() {
  const [state, setState] = useState<State>({ at: "open" });
  const [tab, setTab] = useState<Tab>("overview");
  const [files, setFiles] = useState<File[] | null>(null);
  const [loads, setLoads] = useState(0);
  useEffect(() => {
    setLoads((n) => n + 1);
    if (!files) return;
    let live = true;
    setState({ at: "busy" });
    setTab("overview");
    void load(files).then((s) => live && setState(s));
    return () => { live = false; };
  }, [files]);

  const onFolder = (f: Folder) => {
    setFiles(null);
    setState({ at: "busy" });
    void confirm(f).then(setState);
  };
  const onScan = (f: Folder) => {
    setState({ at: "scanning", folder: f });
    setTab("overview");
    setLoads((n) => n + 1);
    void scanFolder(f).then(setState);
  };
  const names = state.at === "one" || state.at === "kept" ? state.name : state.at === "two" ? state.names.join(" and ") : "";
  return (
    <main className="page">
      <header className="bar">
        <Mark size={28} />
        <span className="wordmark" style={{ font: "600 18px/1 var(--font-display)" }}>cernly local viewer</span>
        {names && <span className="muted names mono">{names}</span>}
        <span className="spacer" />
        {(state.at === "one" || state.at === "two" || state.at === "kept") && <button type="button" className="btn" onClick={() => { setFiles(null); setState({ at: "open" }); }}>Start over</button>}
      </header>
      {state.at === "open" && <Open onFiles={setFiles} onFolder={onFolder} />}
      {state.at === "busy" && <p className="panel empty" role="status">Reading…</p>}
      {state.at === "confirm" && <Confirm folder={state.folder} kind={state.kind} plugins={state.plugins} maxFiles={state.maxFiles} onScan={() => onScan(state.folder)} onCancel={() => setState({ at: "open" })} />}
      {state.at === "scanning" && <Scanning folder={state.folder} />}
      {state.at === "error" && (
        <>
          <p className="error" role="alert">{state.message}</p>
          <Open onFiles={setFiles} onFolder={onFolder} />
        </>
      )}
      <Guard key={loads} onFiles={setFiles} onFolder={onFolder}>
        {state.at === "one" && state.scanned && <Scanned name={state.name} repo={state.scanned.repo} facts={state.scanned.facts} warnings={state.scanned.warnings} dirty={state.v.scan.commit.dirty === true} changed={state.scanned.changed} />}
        {state.at === "kept" && <Scanned name={state.name} repo={state.scanned.repo} facts={state.scanned.facts} warnings={state.scanned.warnings} dirty={state.scanned.dirty} changed={state.scanned.changed} viewError={state.scanned.view_error} />}
        {(state.at === "one" || state.at === "two") && <Tabs tab={tab} two={state.at === "two"} onTab={(t) => { setTab(t); globalThis.scrollTo?.(0, 0); }} />}
        {state.at === "one" && <One v={state.v} tab={tab} />}
        {state.at === "two" && (tab === "changes" ? <Changes c={state.c} /> : <One v={state.c.to} tab={tab} />)}
      </Guard>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
