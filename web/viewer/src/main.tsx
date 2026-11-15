import "@cernly/brand/fonts.css";
import "@cernly/brand/tokens.css";
import "@cernly/brand/base.css";
import "@cernly/views/views.css";
import "./viewer.css";
import { Component, StrictMode, useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { Mark } from "@cernly/brand/mark";
import { compare, tooLarge, view, type Changes as ChangesData, type View } from "./cernly";
import { Changes } from "./Changes";
import { Open } from "./Open";
import { Overview } from "./Overview";

// The theme follows the system; the brand tokens read data-theme on <html>.
const dark = globalThis.matchMedia?.("(prefers-color-scheme: dark)");
const setTheme = () => { document.documentElement.dataset.theme = dark?.matches ? "dark" : "light"; };
setTheme();
dark?.addEventListener("change", setTheme);

type State =
  | { at: "open" }
  | { at: "busy" }
  | { at: "error"; message: string }
  | { at: "one"; v: View; name: string }
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

// A scan the views cannot draw shows its error and the file control, not a
// blank page. A new key (each load, and back to the file control) clears it.
class Guard extends Component<{ children: ReactNode; onFiles: (files: File[]) => void }, { error?: string }> {
  state: { error?: string } = {};
  static getDerivedStateFromError(e: unknown) {
    return { error: e instanceof Error ? e.message : String(e) };
  }
  render() {
    if (this.state.error === undefined) return this.props.children;
    return (
      <>
        <p className="error" role="alert">This scan could not be shown: {this.state.error}</p>
        <Open onFiles={this.props.onFiles} />
      </>
    );
  }
}

function App() {
  const [state, setState] = useState<State>({ at: "open" });
  const [tab, setTab] = useState<"overview" | "changes">("overview");
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

  const names = state.at === "one" ? state.name : state.at === "two" ? state.names.join(" and ") : "";
  return (
    <main className="page">
      <header className="bar">
        <Mark size={28} />
        <span className="wordmark" style={{ font: "600 18px/1 var(--font-display)" }}>cernly local viewer</span>
        {names && <span className="muted names mono">{names}</span>}
        <span className="spacer" />
        {(state.at === "one" || state.at === "two") && <button type="button" className="btn" onClick={() => { setFiles(null); setState({ at: "open" }); }}>Open other scans</button>}
      </header>
      {state.at === "open" && <Open onFiles={setFiles} />}
      {state.at === "busy" && <p className="panel empty" role="status">Reading the scan…</p>}
      {state.at === "error" && (
        <>
          <p className="error" role="alert">{state.message}</p>
          <Open onFiles={setFiles} />
        </>
      )}
      <Guard key={loads} onFiles={setFiles}>
        {state.at === "one" && <Overview v={state.v} />}
        {state.at === "two" && (
          <>
            <div className="seg" role="group" aria-label="View">
              <button type="button" className="btn" aria-pressed={tab === "overview"} onClick={() => setTab("overview")}>Overview</button>
              <button type="button" className="btn" aria-pressed={tab === "changes"} onClick={() => setTab("changes")}>Changes</button>
            </div>
            {tab === "overview" ? <Overview v={state.c.to} /> : <Changes c={state.c} />}
          </>
        )}
      </Guard>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
