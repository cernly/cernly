import { useEffect, useState } from "react";
import { save as download } from "./download";
import { maxRead, ListFolder, type Folder } from "./folder";

// Past this many files the webkitdirectory list itself is slow: the browser
// listed node_modules and build output before anything could skip them.
const manyFiles = 200_000;

export type Kind = "git" | "git-file" | "plain";

// What the page found in the picked folder, said before anything is scanned.
export function Confirm({ folder, kind, plugins, maxFiles, onScan, onCancel }: { folder: Folder; kind: Kind; plugins: string[]; maxFiles: number; onScan: () => void; onCancel: () => void }) {
  const count = folder instanceof ListFolder ? folder.count : undefined;
  const noHidden = folder instanceof ListFolder && !folder.hidden;
  return (
    <section className="panel confirm" aria-label="Before the scan">
      <h2>Scan <span className="mono">{folder.name}</span>?</h2>
      {kind === "git" && <p>A git repository. Only the files git tracks are read, as they are on disk now; the scan is labelled with the commit HEAD points to, and marked dirty when tracked files differ from it.</p>}
      {noHidden && <p className="error" role="alert">This browser left out the folder's hidden files (<span className="mono">.git</span>, <span className="mono">.env</span>, <span className="mono">.github</span>), so the scan would miss them and differ from the CLI's. Drop the folder on this page instead.</p>}
      {kind === "plain" && !noHidden && <p>This folder has no <span className="mono">.git</span>, so every file in it is read, untracked ones included, and the scan has no commit. Pick the repository's own folder to read only what git tracks.</p>}
      {kind === "git-file" && <p className="error" role="alert">Here <span className="mono">.git</span> is a file: a linked worktree or a submodule, whose repository is outside this folder. Pick the main checkout, or scan this one with the cernly CLI.</p>}
      {count !== undefined && count > manyFiles && <p className="error" role="alert">The browser listed {count.toLocaleString("en")} files in this folder, ignored ones included. A browser that can pick a folder directly (Chrome, Edge) or dropping the folder reads only what the scan needs.</p>}
      <ul className="muted notes">
        <li>Symlinks and submodules are skipped, as by the CLI. The page cannot see a symlink or a file's executable bit, so a change to either does not mark the scan dirty here.</li>
        <li>{plugins.length ? <>Built-in plugins plus {plugins.join(", ")}.</> : <>Built-in plugins only: this page carries no outside plugins.</>} Up to {(maxRead / 2 ** 30).toLocaleString("en")} GB and {maxFiles.toLocaleString("en")} files are read; a larger repository needs the CLI.</li>
      </ul>
      <div className="seg">
        {kind !== "git-file" && !noHidden && <button type="button" className="btn primary" onClick={onScan}>Scan</button>}
        <button type="button" className="btn" onClick={onCancel}>Cancel</button>
      </div>
    </section>
  );
}

// While the scan runs: how much it has read, so a long scan shows it moves.
export function Scanning({ folder }: { folder: Folder }) {
  const [read, setRead] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setRead(folder.bytesRead), 500);
    return () => clearInterval(t);
  }, [folder]);
  return (
    <p className="panel empty" role="status">
      Scanning <span className="mono">{folder.name}</span> in this page… {read > 0 && <span className="muted">{(read / 2 ** 20).toFixed(0)} MB read</span>}
    </p>
  );
}

// After a browser scan: its warnings, and the facts file to keep.
export function Scanned({ name, facts, warnings, dirty }: { name: string; facts: string; warnings: string[]; dirty: boolean }) {
  const save = () => download(`${name}.facts.json`, facts);
  return (
    <section className="panel scanned" aria-label="This scan">
      <div className="panel-head">
        <h2>Scanned in this page</h2>
        <button type="button" className="btn" onClick={save}>Save facts.json</button>
      </div>
      <div style={{ padding: "0 16px 12px" }}>
        <p className="muted">The same file <span className="mono">cernly scan</span> writes for this checkout. Open it here later, or with another scan to see what changed.{dirty ? " Tracked files differ from the commit, so the scan is marked dirty." : ""}</p>
        {warnings.length > 0 && <ul className="notes">{warnings.map((w) => <li key={w}>{w}</li>)}</ul>}
      </div>
    </section>
  );
}
