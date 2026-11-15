// Hosts nobody claims, as Routes rows. Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { strongest, unclaimedRows } from "./unclaimed-model.ts";

const ref = (id, value) => ({ id, value, by: "Ada", at: "2026-11-01T09:00:00Z" });
const fact = (key, kind, attrs, path, line) => ({ key, kind, attrs, plugin: "x", evidence: [{ path, line }] });

const facts = [
  fact("outbound_host:api.pay.example", "outbound_host", { host: "api.pay.example", usage: "request" }, "worker/pay.go", 41),
  fact("outbound_host:docs.example.org", "outbound_host", { host: "docs.example.org", usage: "mention" }, "README.ts", 3),
  fact("env_var:MOCK_URL", "env_var", { name: "MOCK_URL" }, ".env.example", 2),
  fact("outbound_host:old.acme.test", "outbound_host", { host: "old.acme.test", usage: "default" }, "sync/client.go", 9),
  fact("outbound_host:api.acme.test", "outbound_host", { host: "api.acme.test", usage: "request" }, "web/api.ts", 5),
  fact("outbound_host:cdn.acme.test", "outbound_host", { host: "cdn.acme.test", usage: "mention" }, "web/cdn.ts", 1),
  fact("outbound_host:ok.vendor.example", "outbound_host", { host: "ok.vendor.example", usage: "request" }, "api/v.go", 7),
];

const map = {
  stacks: [], links: [], images: [], findings: [], ai_tools: [], parties: [], rules_version: "0.5.0", catalogue_version: "x",
  attached: [
    { service: "service:compose:prod:worker", fact: "outbound_host:api.pay.example", kind: "outbound_host", source: "." },
    { service: "service:compose:prod:api", fact: "outbound_host:api.pay.example", kind: "outbound_host", source: "." },
  ],
  unknown: [
    { host: "api.pay.example", usage: "request", from: ["outbound_host:api.pay.example"] },
    { host: "docs.example.org", usage: "mention", from: ["outbound_host:docs.example.org"] },
    { host: "mock.example.org", usage: "env_hint", from: ["env_var:MOCK_URL"] },
    { host: "ok.vendor.example", usage: "request", from: ["outbound_host:ok.vendor.example"], confirmed: ref(3, "approved") },
  ],
  dismissed: [{ subject: "host:old.acme.test", kind: "host", name: "old.acme.test", confirmed: ref(5, "not_supplier"), from: ["outbound_host:old.acme.test"] }],
  own: [
    { host: "api.acme.test", domain: "acme.test", confirmed: ref(4, "own"), from: ["outbound_host:api.acme.test"] },
    { host: "cdn.acme.test", domain: "acme.test", confirmed: ref(4, "own"), from: ["outbound_host:cdn.acme.test"] },
    { host: "app.acme.test", domain: "acme.test", served_by: ["route:x"], from: [] },
  ],
};

test("open rows leave out mentions and decided hosts; sources carry services and evidence", () => {
  const { open, mentioned } = unclaimedRows(map, facts);
  assert.deepEqual(open.map((r) => [r.host, r.usage]), [["api.pay.example", "request"], ["mock.example.org", "env_hint"]]);
  assert.deepEqual(open[0].sources, [{ fact: "outbound_host:api.pay.example", services: ["service:compose:prod:api", "service:compose:prod:worker"], evidence: [{ path: "worker/pay.go", line: 41 }] }]);
  assert.deepEqual(mentioned.map((r) => r.host), ["docs.example.org"]);
});

test("decided rows: approved, dismissed, and one row per own-domain claim", () => {
  const { decided } = unclaimedRows(map, facts);
  assert.deepEqual(decided.map((r) => [r.host, r.state, r.confirmed.id, r.usage]), [
    ["acme.test", "own", 4, "request"],
    ["ok.vendor.example", "approved", 3, "request"],
    ["old.acme.test", "not_supplier", 5, "default"],
  ]);
  assert.deepEqual(decided[0].hosts, ["api.acme.test", "cdn.acme.test"]);
  assert.equal(decided[0].subject, "host:acme.test");
});

test("strongest usage", () => {
  assert.equal(strongest(["mention", "request", "env_hint"]), "request");
  assert.equal(strongest([]), "");
});
