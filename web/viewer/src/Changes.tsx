import { useState } from "react";
import { driftDiff } from "@cernly/views/drift";
import { short } from "@cernly/views/format";
import type { Changes as Data } from "./cernly";

const sign = { added: "+", removed: "−", changed: "~" } as const;

// What changed between two scans, as the app's Changes view shows it.
export function Changes({ c }: { c: Data }) {
  const [section, setSection] = useState("");
  const groups = c.groups ?? [];
  const mapGroups = c.map_groups ?? [];
  const all = [...groups, ...mapGroups];
  const mapSection = mapGroups.some((g) => g.section === section);
  const changes = section ? (mapSection ? c.drift.map : c.drift.facts).filter((x) => x.section === section) : [];
  const maxTotal = Math.max(1, ...all.map((g) => g.added + g.removed + g.changed));
  const from = short(c.from.scan.commit.sha), to = short(c.to.scan.commit.sha);
  return (
    <section id="changes" aria-label="What changed" className="panel">
      <div className="panel-head">
        <h2>What changed</h2>
        <span className="label">{from} to {to}</span>
      </div>
      {all.length === 0 ? (
        <p className="empty">Nothing changed between {from} and {to}.</p>
      ) : (
        <>
          <p className="headline">
            From {from} to {to}:{" "}
            {(groups.length > 0 ? groups : mapGroups).map((g, k) => (
              <span key={g.section}>{k > 0 ? ", " : ""}<b>{g.added + g.removed + g.changed}</b> {g.added + g.removed + g.changed === 1 ? g.one : g.label}</span>
            ))}
            .
          </p>
          <div className="scroll-x">
            <table style={{ minWidth: 560 }}>
              <thead>
                <tr><th>What</th><th>Change</th><th className="num">Added</th><th className="num">Removed</th><th className="num">Changed</th></tr>
              </thead>
              <tbody>
                {all.map((g, k) => {
                  const total = g.added + g.removed + g.changed;
                  const w = (x: number) => `${(x / maxTotal) * 100}%`;
                  return (
                    <tr key={(k < groups.length ? "f:" : "m:") + g.section} className={`pick${section === g.section ? " on" : ""}`} onClick={() => setSection(section === g.section ? "" : g.section)}>
                      <td style={{ fontWeight: 600 }}>
                        {k === groups.length && mapGroups.length > 0 && <span className="muted" style={{ display: "block", fontWeight: 400 }}>Derived on the map</span>}
                        <button type="button" className="rowpick" aria-expanded={section === g.section}>{g.label}</button>
                      </td>
                      <td style={{ width: "40%" }}>
                        <div className="hbar" title={`${total}`}>
                          <span className="seg-added" style={{ width: w(g.added) }} />
                          <span className="seg-removed" style={{ width: w(g.removed) }} />
                          <span className="seg-changed" style={{ width: w(g.changed) }} />
                        </div>
                      </td>
                      <td className="num">{g.added || ""}</td>
                      <td className="num">{g.removed || ""}</td>
                      <td className="num">{g.changed || ""}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <p className="panel-foot">
            <span className="legend"><i className="seg-added" />ADDED</span>{" "}
            <span className="legend"><i className="seg-removed" />REMOVED</span>{" "}
            <span className="legend"><i className="seg-changed" />CHANGED</span>
            {" "}· Click a row for its changes; a changed map item shows old → new. A moved line is not a change.
          </p>
          {section && (
            <>
              <div className="panel-head"><h2>{all.find((g) => g.section === section)?.label} ({changes.length})</h2></div>
              <div className="scroll-x">
                <table>
                  <tbody>
                    {changes.slice(0, 500).map((x) => (
                      <tr key={x.section + x.key}>
                        <td className="code" style={{ width: 24 }}>{sign[x.change]}</td>
                        <td className="code">{x.key}</td>
                        {mapSection && <td className="code muted">{driftDiff(x)}</td>}
                      </tr>
                    ))}
                  </tbody>
                </table>
                {changes.length > 500 && <p className="empty">Showing 500 of {changes.length}.</p>}
              </div>
            </>
          )}
        </>
      )}
    </section>
  );
}
