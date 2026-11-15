// The AI tooling set up in one scan: the configuration files of AI tools in
// the repository. Which change a tool wrote is not in a scan.
import { plural } from "./format";

// empty is the sentence for a scan with none.
export function AISetup({ tools, of, empty }: { tools: { name: string; files: number }[]; of: string; empty: string }) {
  return (
    <section aria-label="AI tooling set up" className="panel side-panel">
      <div className="panel-head"><h2>Set up in the repo · {of}</h2></div>
      <div className="panel-body">
        {tools.length === 0 ? (
          <p className="muted">{empty}</p>
        ) : (
          <div className="kv">
            {tools.map((t) => (
              <span key={t.name} style={{ display: "contents" }}><span>{t.name}</span><span className="mono muted">{plural(t.files, "file")}</span></span>
            ))}
          </div>
        )}
      </div>
      <p className="panel-foot">Repo-level proof: these tools are set up here. It does not say which change a tool wrote.</p>
    </section>
  );
}
