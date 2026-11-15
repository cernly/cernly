// What a changed map item changed, as the server's drift.Change.Diff writes it. Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { driftDiff } from "./drift.ts";

test("a changed item names each differing field, old → new", () => {
  const c = { change: "changed", section: "findings", key: "party_unconfirmed party:aws", before: { detail: "old", grade: "" }, after: { detail: "new", grade: "" } };
  assert.equal(driftDiff(c), "detail: old → new");
  assert.equal(driftDiff({ ...c, before: {}, after: { domain: "example.be" } }), "domain: none → example.be");
  assert.equal(driftDiff({ ...c, change: "added" }), "");
});
