import { useEffect, useMemo, useState } from "react";
import { redact, type Upload as Prepared } from "./cernly";
import { save } from "./download";
import { baseName, counts, preview } from "./preview";

const LINES = 40;

// Shows what an upload file would hold and saves it. The page cannot send anything:
// the file is saved, and the customer uploads it. With redaction on, paths, names,
// hosts and the repo id are replaced by tokens, and the list that turns them back
// is a second file that stays with the customer.
export function Upload({ name, facts }: { name: string; facts: string }) {
  const [open, setOpen] = useState(false);
  const [on, setOn] = useState(true);
  const [prepared, setPrepared] = useState<Prepared | { error: string }>();
  useEffect(() => {
    if (!open) return;
    let live = true;
    redact(facts).then(
      (p) => live && setPrepared(p),
      (e) => live && setPrepared({ error: e instanceof Error ? e.message : String(e) }),
    );
    return () => { live = false; };
  }, [open, facts]);

  const text = prepared && !("error" in prepared) ? (on ? prepared.facts : facts) : undefined;
  const shown = useMemo(() => {
    if (text === undefined) return undefined;
    try {
      return preview(text, LINES);
    } catch (e) {
      return `The preview could not be made: ${e instanceof Error ? e.message : String(e)}`;
    }
  }, [text]);
  const base = baseName(name);

  return (
    <section className="panel upload" aria-label="Prepare an upload">
      <div className="panel-head">
        <h2>Prepare an upload</h2>
        <button type="button" className="btn" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Close" : "Prepare an upload"}</button>
      </div>
      {open && (
        <div className="upload-body">
          <label className="check">
            <input type="checkbox" checked={on} onChange={(e) => setOn(e.target.checked)} />
            Redact paths, names, hosts and the repo id
          </label>
          <p className="muted">Fact kinds, the scanner's own vocabulary (languages, engines, frameworks), versions, numbers, line numbers and the commit id stay readable.</p>
          {prepared && "error" in prepared && <p className="error" role="alert">{prepared.error}</p>}
          {!prepared && <p className="muted" role="status">Preparing…</p>}
          {shown !== undefined && prepared && !("error" in prepared) && (
            <>
              <p className="muted" data-testid="upload-counts">{on ? `Redacted: ${counts(prepared.counts)}.` : "Nothing is redacted: the file is the scan as it is."}</p>
              {on && (
                <details className="readable" data-testid="upload-readable">
                  <summary>{prepared.readable.length === 1 ? "1 readable word stays" : `${prepared.readable.length} readable words stay`} in the whole file (besides the schema id and the commit)</summary>
                  <p className="muted">Check that none of these is yours to keep private before you save.</p>
                  <ul className="mono">{prepared.readable.map((w) => <li key={w}>{w}</li>)}</ul>
                </details>
              )}
              <pre className="preview mono" tabIndex={0} aria-label="The first lines of the upload file" data-testid="upload-preview">{shown}</pre>
              <div className="seg">
                <button type="button" className="btn primary" onClick={() => save(`${base}.upload.json`, text!)}>Save upload file</button>
                {on && <button type="button" className="btn" onClick={() => save(`${base}.tokens.json`, prepared.map)}>Save token list</button>}
              </div>
              {on && <p className="muted">Keep the token list: it turns the tokens back into your names. It is not part of the upload.</p>}
            </>
          )}
          <p className="muted">This page cannot send anything; you upload the file yourself.</p>
        </div>
      )}
    </section>
  );
}
