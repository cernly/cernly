import { useState } from "react";

// The opening screen: one drop zone and a file button. Nothing is uploaded; the
// files are read here by the page.
export function Open({ onFiles }: { onFiles: (files: File[]) => void }) {
  const [over, setOver] = useState(false);
  return (
    <section
      className={`panel drop${over ? " over" : ""}`}
      aria-label="Open a Cernly scan"
      onDragOver={(e) => { e.preventDefault(); setOver(true); }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        if (e.dataTransfer.files.length) onFiles([...e.dataTransfer.files]);
      }}
    >
      <h1>Open a Cernly scan</h1>
      <p>Drop one facts.json to see it, or two to see what changed. Nothing leaves this page: it has no network access.</p>
      <label className="btn primary">
        Choose files
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
    </section>
  );
}
