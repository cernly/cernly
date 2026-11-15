import { useRef, useState } from "react";
import { canPickFolder, droppedFolder, ListFolder, pickFolder, type Folder } from "./folder";

// The opening screen: scan a folder, or open one or two facts files. Nothing is
// uploaded; the page reads the files itself and has no network access.
export function Open({ onFiles, onFolder }: { onFiles: (files: File[]) => void; onFolder: (f: Folder) => void }) {
  const [over, setOver] = useState(false);
  const [error, setError] = useState("");
  const dirInput = useRef<HTMLInputElement>(null);
  const picker = canPickFolder();
  return (
    <section
      className={`panel drop${over ? " over" : ""}`}
      aria-label="Scan a repository or open a scan"
      onDragOver={(e) => { e.preventDefault(); setOver(true); }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        const files = [...e.dataTransfer.files];
        void droppedFolder(e.dataTransfer.items).then((f) => {
          if (f && f !== "files") onFolder(f);
          else if (files.length) onFiles(files);
        }).catch((err) => setError(String(err instanceof Error ? err.message : err)));
      }}
    >
      <h1>Scan a repository</h1>
      <p>Pick the repository's folder, the one with <span className="mono">.git</span> in it, or drop it here. The scan runs in this page: nothing is uploaded, and the page has no network access.</p>
      <div className="seg">
        {picker ? (
          <button type="button" className="btn primary" onClick={() => void pickFolder().then((f) => f && onFolder(f)).catch((err) => setError(String(err instanceof Error ? err.message : err)))}>Choose a folder</button>
        ) : (
          <label className="btn primary">
            Choose a folder
            <input
              ref={dirInput}
              type="file"
              {...{ webkitdirectory: "", directory: "" }}
              onChange={(e) => {
                const files = [...(e.target.files ?? [])];
                e.target.value = "";
                if (files.length) onFolder(new ListFolder(files));
              }}
            />
          </label>
        )}
        <label className="btn">
          Open scans
          <input
            type="file"
            accept=".json"
            multiple
            onChange={(e) => {
              const files = [...(e.target.files ?? [])];
              e.target.value = "";
              if (files.length) onFiles(files);
            }}
          />
        </label>
      </div>
      <p className="muted">Or open one facts.json from <span className="mono">cernly scan</span> to see it, or two to see what changed.</p>
      {error && <p className="error" role="alert">{error}</p>}
    </section>
  );
}
