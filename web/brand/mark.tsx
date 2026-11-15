// The A3 mark "One answer" (docs/brand): grains above, the sieve, one point below. Drawn with tokens so it
// follows the theme: grains --border-strong (about 3:1), bar --ink, point --brand. The app passes point to colour
// the one dot by a status (the verdict: --brand all clear, --warning to confirm, --danger to act).
const grains: [number, number, number][] = [
  [10, 9, 1.6], [18, 13, 2.2], [26, 7, 1.8], [35, 11, 2], [14, 18, 2], [30, 17, 1.6], [23, 19, 1.5], [39, 18, 1.4],
];

export function Mark({ size, point = "var(--brand)" }: { size?: number; point?: string }) {
  return (
    <svg viewBox="0 0 48 48" width={size} height={size} aria-hidden="true" focusable="false">
      {grains.map(([cx, cy, r]) => (
        <circle key={`${cx}-${cy}`} cx={cx} cy={cy} r={r} style={{ fill: "var(--border-strong)" }} />
      ))}
      <path d="M6 26H42" strokeWidth="4" strokeLinecap="round" style={{ stroke: "var(--ink)" }} />
      <circle cx="24" cy="37.5" r="5.2" style={{ fill: point }} />
    </svg>
  );
}

// The mark plus "cernly". Pass href for a link; the accessible name is "Cernly".
export function Wordmark({ href }: { href?: string }) {
  const body = (
    <>
      <Mark />
      <span>ce<span className="r">r</span>nly</span>
    </>
  );
  return href ? (
    <a className="wordmark" href={href} aria-label="Cernly home">{body}</a>
  ) : (
    <span className="wordmark" aria-label="Cernly" role="img">{body}</span>
  );
}
