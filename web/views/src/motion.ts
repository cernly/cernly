import { useEffect, useRef, useState } from "react";

// Motion helpers shared by the views. Every animation here has a still
// fallback: with prefers-reduced-motion the value is shown as it is.

export function useReducedMotion(): boolean {
  const query = "(prefers-reduced-motion: reduce)";
  const [reduce, setReduce] = useState(() => typeof window !== "undefined" && !!window.matchMedia?.(query).matches);
  useEffect(() => {
    const m = window.matchMedia?.(query);
    if (!m) return;
    const on = () => setReduce(m.matches);
    on();
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return reduce;
}

// The next shown value of a progress bar between server updates (pure; see
// motion.test.mjs).
//   shown: what the bar shows now; real: the server's latest value;
//   rate: observed units per millisecond (0 when unknown); dt: ms since the last frame;
//   step: how far past the real value the bar may run while it waits (the
//   expected step: what one server update adds, at least one unit).
// It never moves backwards, catches up with a real value ahead of it within
// about half a second, and between updates creeps on at the observed rate,
// never more than `step` past the real value and never past the total.
export function progressStep(shown: number, real: number, total: number, rate: number, dt: number, step = 1): number {
  const cap = Math.min(total, real + step);
  let next = shown;
  if (real > shown) {
    // Catch up: close most of the gap quickly, at least at the observed rate.
    next = shown + Math.max((real - shown) * Math.min(1, dt / 160), rate * dt);
    next = Math.min(next, real);
  } else if (rate > 0) {
    // Ahead of the server: creep on, slowing as it nears the cap.
    const room = cap - shown;
    next = shown + Math.min(rate * dt, Math.max(0, room) * Math.min(1, dt / 1200));
  }
  return Math.max(shown, Math.min(next, cap));
}

// useSmoothProgress turns a value that arrives in jumps (a server poll every
// few seconds) into one that moves continuously, per progressStep.
export function useSmoothProgress(real: number, total: number): number {
  const reduce = useReducedMotion();
  const [shown, setShown] = useState(real);
  const st = useRef({ shown: real, real, total, first: { t: 0, v: real }, rate: 0, step: 1 });
  useEffect(() => {
    const s = st.current;
    const now = performance.now();
    if (s.first.t === 0) s.first = { t: now, v: real };
    else if (real > s.real && now > s.first.t) {
      s.rate = (real - s.first.v) / (now - s.first.t);
      // The expected step: what one update has added on average, at least one unit.
      s.step = Math.max(1, real - s.real);
    }
    s.real = real;
    s.total = total;
  }, [real, total]);
  useEffect(() => {
    if (reduce) return;
    let raf = 0;
    let last = performance.now();
    const tick = (t: number) => {
      const s = st.current;
      const next = progressStep(s.shown, s.real, s.total, s.rate, Math.min(100, t - last), s.step);
      last = t;
      if (next !== s.shown) {
        s.shown = next;
        setShown(next);
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [reduce]);
  return reduce ? Math.max(real, 0) : Math.max(shown, 0);
}

// useTween eases a number towards its target over `ms` (counts that change
// between weeks). Reduced motion: the target at once.
export function useTween(target: number, ms = 600): number {
  const reduce = useReducedMotion();
  const [v, setV] = useState(target);
  const from = useRef(target);
  const cur = useRef(target);
  useEffect(() => {
    if (reduce || !Number.isFinite(target)) {
      cur.current = target;
      setV(target);
      return;
    }
    from.current = cur.current;
    const start = performance.now();
    let raf = 0;
    const tick = (t: number) => {
      const k = Math.min(1, (t - start) / ms);
      const e = 1 - Math.pow(1 - k, 3);
      cur.current = from.current + (target - from.current) * e;
      setV(cur.current);
      if (k < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [target, ms, reduce]);
  return v;
}

// usePresence keeps items that left a list for a moment so they can fade
// out, and marks items that joined it so they can fade and scale in. The
// first render marks nothing (no animation on page load). Leaving items keep
// their place in the list. Reduced motion: everything is "stay".
export type Phase = "enter" | "stay" | "leave";
export function usePresence<T>(items: T[], keyOf: (t: T) => string, ms = 300): { item: T; key: string; phase: Phase }[] {
  const reduce = useReducedMotion();
  const st = useRef<{ first: boolean; known: Set<string>; prev: { item: T; key: string }[]; leaving: Map<string, { item: T; index: number; until: number }> }>({
    first: true, known: new Set(), prev: [], leaving: new Map(),
  });
  const [, bump] = useState(0);
  const now = Date.now();
  const s = st.current;
  const cur = items.map((item) => ({ item, key: keyOf(item) }));
  const keys = new Set(cur.map((c) => c.key));
  const enter = new Set<string>();
  if (!s.first && !reduce) {
    for (const c of cur) if (!s.known.has(c.key)) enter.add(c.key);
    s.prev.forEach((p, index) => {
      if (!keys.has(p.key) && !s.leaving.has(p.key)) s.leaving.set(p.key, { item: p.item, index, until: now + ms });
    });
  }
  for (const k of keys) s.leaving.delete(k);
  for (const [k, l] of s.leaving) if (l.until <= now || reduce) s.leaving.delete(k);
  s.first = false;
  s.known = keys;
  s.prev = cur;
  const out: { item: T; key: string; phase: Phase }[] = cur.map((c) => ({ ...c, phase: enter.has(c.key) ? "enter" : "stay" }));
  for (const [key, l] of [...s.leaving].sort((a, b) => a[1].index - b[1].index)) out.splice(Math.min(l.index, out.length), 0, { item: l.item, key, phase: "leave" });
  const pending = s.leaving.size > 0;
  useEffect(() => {
    if (!pending) return;
    const t = setTimeout(() => bump((x) => x + 1), ms + 20);
    return () => clearTimeout(t);
  });
  return out;
}

// useGlide moves points (boxes on the floor plan) from where they are shown
// to their new place over `ms`, so positions that change between weeks glide
// instead of jumping. Points it has seen keep their last place (a leaving
// box fades out where it was). Reduced motion: the new places at once.
export function useGlide(targets: { key: string; x: number; y: number }[], ms = 450): Map<string, { x: number; y: number }> {
  const reduce = useReducedMotion();
  const shown = useRef(new Map<string, { x: number; y: number }>());
  const [, bump] = useState(0);
  const sig = targets.map((t) => `${t.key}@${t.x},${t.y}`).join("|");
  useEffect(() => {
    const from = new Map(shown.current);
    const to = new Map(targets.map((t) => [t.key, { x: t.x, y: t.y }]));
    const moving = [...to].filter(([k, p]) => {
      const f = from.get(k);
      return f && (f.x !== p.x || f.y !== p.y);
    });
    for (const [k, p] of to) if (!from.has(k) || reduce) shown.current.set(k, p);
    if (reduce || moving.length === 0) {
      bump((x) => x + 1);
      return;
    }
    const start = performance.now();
    let raf = 0;
    const tick = (t: number) => {
      const k = Math.min(1, (t - start) / ms);
      const e = k < 0.5 ? 4 * k * k * k : 1 - Math.pow(-2 * k + 2, 3) / 2;
      for (const [key, p] of moving) {
        const f = from.get(key)!;
        shown.current.set(key, { x: f.x + (p.x - f.x) * e, y: f.y + (p.y - f.y) * e });
      }
      bump((x) => x + 1);
      if (k < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
    // targets is read through sig
  }, [sig, ms, reduce]);
  // Before the effect has run once, show the targets.
  const out = new Map(shown.current);
  for (const t of targets) if (!out.has(t.key)) out.set(t.key, { x: t.x, y: t.y });
  return out;
}
