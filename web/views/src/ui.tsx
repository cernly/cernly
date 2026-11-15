import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Mark } from "@cernly/brand/mark";
import type { Explain } from "./types";
import { usePresence, useReducedMotion, useTween } from "./motion";

// A panel that slides over the page from the right (transform only, so the
// page behind never moves); Esc, the scrim or the close button slide it out
// the same way before the parent unmounts it. Focus moves into it on open and
// back to what opened it (the box) on close. Reduced motion: no slide.
export const DRAWER_MS = 220;

export function Drawer({ title, label, onClose, returnTo, children }: {
  title: string; label: string; onClose: () => void; returnTo?: string; children: ReactNode;
}) {
  const ref = useRef<HTMLElement>(null);
  const reduce = useReducedMotion();
  const [shown, setShown] = useState(false);
  const [closing, setClosing] = useState(false);
  const opener = useRef<HTMLElement | null>(null);
  const close = useCallback(() => setClosing(true), []);
  // Open: start off-screen, slide in on the next frame; focus moves in.
  useEffect(() => {
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    ref.current?.focus({ preventScroll: true });
    const raf = requestAnimationFrame(() => requestAnimationFrame(() => setShown(true)));
    return () => {
      cancelAnimationFrame(raf);
      // Back to the box that opened it, if it is still on the page.
      // Safari does not focus a button on click, so the opener can be named.
      const named = returnTo ? document.querySelector<HTMLElement>(returnTo) : null;
      const o = named ?? opener.current;
      if (o && o.isConnected && o !== document.body) o.focus({ preventScroll: true });
    };
  }, []);
  // Close: slide out, then let the parent unmount the drawer.
  useEffect(() => {
    if (!closing) return;
    const t = setTimeout(onClose, reduce ? 0 : DRAWER_MS);
    return () => clearTimeout(t);
  }, [closing, onClose, reduce]);
  useEffect(() => {
    // Esc in a dialog opened from the drawer (a confirmation) closes only that dialog.
    const key = (e: KeyboardEvent) => e.key === "Escape" && !document.querySelector("dialog[open]") && close();
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [close]);
  const state = shown && !closing ? " open" : "";
  return (
    <>
      <div className={`scrim${state}`} onClick={close} aria-hidden="true" />
      <aside ref={ref} tabIndex={-1} className={`drawer panel${state}`} role="dialog" aria-modal="true" aria-label={title}>
        <div className="drawer-head">
          <div>
            <p className="label">{label}</p>
            <h2>{title}</h2>
          </div>
          <button type="button" className="btn" onClick={close} aria-label="Close">Close</button>
        </div>
        <div className="drawer-body">{children}</div>
      </aside>
    </>
  );
}

// A count that tweens to its new value when it changes (between weeks);
// screen readers get the real value. Reduced motion: no tween.
export function Num({ value }: { value: number }) {
  const v = useTween(value);
  return <>{Math.round(v).toLocaleString("en-US")}</>;
}

// One finding rule with how often it occurs, its meaning and what to do; the
// subjects it is about open on request.
export type Attention = { rule: string; count: number; items: ReactNode[] };

export function AttentionList({ items, explain, empty }: { items: Attention[]; explain: Map<string, Explain>; empty: string }) {
  if (items.length === 0) return <p className="empty">{empty}</p>;
  const rank = { act: 0, confirm: 1, info: 2 } as const;
  const sorted = [...items].sort((a, b) => {
    const la = explain.get(a.rule)?.level ?? "info", lb = explain.get(b.rule)?.level ?? "info";
    return rank[la] - rank[lb] || b.count - a.count || a.rule.localeCompare(b.rule);
  });
  return <AttentionRows sorted={sorted} explain={explain} />;
}

function AttentionRows({ sorted, explain }: { sorted: Attention[]; explain: Map<string, Explain> }) {
  const rows = usePresence(sorted, (a) => a.rule);
  return (
    <ul className="attention">
      {rows.map(({ item: a, phase }) => {
        const e = explain.get(a.rule);
        const level = e?.level ?? "info";
        return (
          <li key={a.rule} className={`level-${level}${phase === "stay" ? "" : ` ${phase}`}`}>
            <details>
              <summary>
                <span className="count"><Num value={a.count} /></span>
                <span className="what">
                  <b>{e?.title ?? a.rule}</b>
                  <span className="muted">{e?.meaning}</span>
                  {e?.action && <span className="action">{level === "info" ? "" : level === "act" ? "To do: " : "To confirm: "}{e.action}</span>}
                </span>
                <span className={`badge ${level === "act" ? "warn" : level === "confirm" ? "dotted" : "dashed"}`}>{level === "act" ? "ACT" : level === "confirm" ? "CONFIRM" : "INFO"}</span>
              </summary>
              <ul className="subjects">{a.items.map((x, k) => <li key={k}>{x}</li>)}</ul>
            </details>
          </li>
        );
      })}
    </ul>
  );
}

// Info holds text an advanced reader may want and a decision maker does not
// need: a small "i" beside a heading opens it. The note is a popover in the
// browser's top layer, placed by the button with fixed coordinates, so no
// dialog, drawer or scroll box clips it or grows a scrollbar. Click outside,
// Esc, scrolling or resizing closes it (Esc closes only the note, not a
// drawer or dialog around it). Children are phrasing content (spans), so Info
// fits inside a heading.
export function Info({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLSpanElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      e.preventDefault();
      setOpen(false);
      btn.current?.focus();
    };
    // The note is fixed to where the button was; when the page moves, close it.
    const away = (e: Event) => { if (!pop.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", down);
    document.addEventListener("keydown", key, true);
    window.addEventListener("scroll", away, true);
    window.addEventListener("resize", away);
    return () => {
      document.removeEventListener("pointerdown", down);
      document.removeEventListener("keydown", key, true);
      window.removeEventListener("scroll", away, true);
      window.removeEventListener("resize", away);
    };
  }, [open]);
  // Place the note under the button, kept 16px inside the viewport; above it
  // when there is no room below.
  useLayoutEffect(() => {
    const el = pop.current, b = btn.current;
    if (!open || !el || !b) return;
    try { el.showPopover?.(); } catch { /* already shown, or no popover support: fixed position still applies */ }
    const r = b.getBoundingClientRect();
    const w = el.offsetWidth, h = el.offsetHeight;
    const left = Math.max(16, Math.min(r.left - 8, window.innerWidth - 16 - w));
    const below = r.bottom + 6;
    const top = below + h > window.innerHeight - 16 && r.top - 6 - h > 16 ? r.top - 6 - h : below;
    el.style.left = `${left}px`;
    el.style.top = `${top}px`;
  }, [open]);
  return (
    <span className="info" ref={ref}>
      <button ref={btn} type="button" className="info-btn" aria-expanded={open} aria-controls={id} aria-label={label} title={label} onClick={() => setOpen(!open)}>
        <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true" focusable="false">
          <circle cx="10" cy="10" r="8.25" fill="none" strokeWidth="1.5" />
          <circle cx="10" cy="6.2" r="1.25" className="dot" />
          <path d="M10 9.2v5.2" strokeWidth="1.8" strokeLinecap="round" />
        </svg>
      </button>
      {open && <span id={id} ref={pop} role="note" popover="manual" className="info-pop">{children}</span>}
    </span>
  );
}

// The verdict a CISO or CTO reads first: act, confirm or all clear, with the
// rules behind it. The mark's one point takes the verdict's colour; the words
// carry the meaning, so colour is never the only signal.
export type Level = "act" | "confirm" | "clear";

export function levelOf(items: Attention[], explain: Map<string, Explain>) {
  const sum = (l: string) => items.filter((a) => (explain.get(a.rule)?.level ?? "info") === l).reduce((t, a) => t + a.count, 0);
  const act = sum("act"), confirm = sum("confirm");
  const level: Level = act ? "act" : confirm ? "confirm" : "clear";
  return { level, act, confirm };
}

const verdictWord: Record<Level, string> = { act: "Action needed", confirm: "Confirm", clear: "All clear" };
const verdictPoint: Record<Level, string> = { act: "var(--danger)", confirm: "var(--warning)", clear: "var(--brand)" };

export function Verdict({ items, explain, clear, href = "#attention", of }: {
  items: Attention[]; explain: Map<string, Explain>; clear: string; href?: string; of: string;
}) {
  const { level, act, confirm } = levelOf(items, explain);
  const top = [...items]
    .filter((a) => (explain.get(a.rule)?.level ?? "info") !== "info")
    .sort((a, b) => Number(explain.get(a.rule)?.level !== "act") - Number(explain.get(b.rule)?.level !== "act") || b.count - a.count)
    .slice(0, 3);
  const line = level === "clear" ? clear : [act && `${act} to act on`, confirm && `${confirm} to confirm`].filter(Boolean).join(" · ");
  return (
    <section className={`panel verdict v-${level}`} aria-label={`Verdict: ${verdictWord[level]}`}>
      <Mark size={44} point={verdictPoint[level]} />
      <div className="verdict-text">
        <p className="label">{of}</p>
        <p className="verdict-word">{verdictWord[level]}</p>
        <p className="verdict-line">{line}</p>
      </div>
      {top.length > 0 && (
        <ul className="verdict-items">
          {top.map((a) => {
            const e = explain.get(a.rule);
            return (
              <li key={a.rule}>
                <a href={href} className={`badge ${e?.level === "act" ? "warn" : "dotted"}`}>{a.count} · {e?.title ?? a.rule}</a>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
