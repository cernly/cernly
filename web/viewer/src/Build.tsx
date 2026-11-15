import { useEffect, useState } from "react";
import { build, type Build as Info } from "./cernly";

// The build this page runs, so a buyer can compare the module's hash with the published one.
export function Build() {
  const [b, setB] = useState<Info>();
  useEffect(() => { void build().then(setB, () => undefined); }, []);
  if (!b || (!b.version && !b.hash)) return null;
  return (
    <p className="muted mono build" data-testid="build" title={b.hash ? `module sha256 ${b.hash}` : undefined}>
      cernly {b.version}{b.hash && <>, module sha256 {b.hash.slice(0, 12)}…</>}
    </p>
  );
}
