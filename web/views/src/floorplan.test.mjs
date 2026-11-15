// What the floor plan opens on: images built in CI.
// Run with `npm test` (Node's own runner; it strips the types of floorplan.ts).
import assert from "node:assert/strict";
import test from "node:test";
import { CI_VIEW, floorPlan, openView, stackID, stacksOf } from "./floorplan.ts";

const app = { test_only: false };
const e2e = { test_only: true };

test("a production stack wins over images built in CI", () => {
  assert.equal(openView([{ name: "prod", role: "production" }, { name: "dev", role: "none" }], [app]), "prod");
});

test("an unconfirmed or declared production stack wins too", () => {
  assert.equal(openView([{ name: "vps", role: "production (unconfirmed)" }], [app]), "vps");
});

test("no production stack and an app image built in CI: the Built in CI group", () => {
  assert.equal(openView([{ name: "dev", role: "none" }], [e2e, app]), CI_VIEW);
  assert.equal(openView([], [app]), CI_VIEW);
});

test("only test-only images: the largest stack, as before", () => {
  assert.equal(openView([{ name: "dev", role: "none" }, { name: "ci", role: "none" }], [e2e]), "dev");
});

test("nothing at all", () => {
  assert.equal(openView([], []), "");
});

// A helm and a kubernetes stack with one name are two picker entries, each
// with its own services (validation-1, microservices-demo).
test("stacks of two tools with one name stay apart", () => {
  const svc = (tool, stack, name) => ({ kind: "service", key: `service:${tool}:${stack}:${name}`, attrs: { name, stack } });
  const facts = [svc("helm", "chart", "web"), svc("kubernetes", "chart", "api"), svc("kubernetes", "chart", "db")];
  const map = { stacks: [{ stack: "stack:helm:chart", role: "none" }, { stack: "stack:kubernetes:chart", role: "none" }], links: [], images: [], findings: [], routes: [] };
  const ss = stacksOf(facts, map);
  assert.deepEqual(ss.map((s) => [s.name, s.stack, s.tool, s.shared, s.size]), [["kubernetes:chart", "chart", "kubernetes", true, 2], ["helm:chart", "chart", "helm", true, 1]]);
  assert.deepEqual(floorPlan(facts, map, "helm:chart").nodes.map((n) => n.key), ["service:helm:chart:web"]);
  assert.equal(stackID("datastore:compose:prod:db"), "compose:prod");
});
