// Dependencies by service. Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { changeText, filterServices, licenceText } from "./deps-model.ts";

const dep = (name, direct, change, extra = {}) => ({ key: `dependency:npm:api:${name}`, ecosystem: "npm", name, direct, path: "api/package.json", line: 3, change, ...extra });
const services = [
  { service: "service:compose:prod:api", name: "prod/api", direct: [dep("express", true), dep("left-pad", true, "new")], transitive: [dep("debug", false, "upgraded", { was_version: "4.3.0" })] },
  { service: "service:compose:prod:web", name: "prod/web", direct: [dep("react", true)], transitive: [] },
];

test("filters keep services with something left", () => {
  assert.deepEqual(filterServices(services, "changed", "").map((s) => [s.name, s.direct.length, s.transitive.length]), [["prod/api", 1, 1]]);
  assert.deepEqual(filterServices(services, "direct", "").map((s) => s.transitive.length), [0, 0]);
  assert.deepEqual(filterServices(services, "all", "REA").map((s) => s.name), ["prod/web"]);
});

test("words", () => {
  assert.equal(changeText(dep("x", true, "upgraded", { was_version: "1.0.0" })), "upgraded from 1.0.0");
  assert.equal(licenceText(dep("x", true)), "not declared");
});
