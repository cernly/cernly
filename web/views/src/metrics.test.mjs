// Surface metrics and the module graph. Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { delta, deltaWords, languages, layers, metricsOf, scopeOf, short, testLines, trimPrefix } from "./metrics.ts";

const surface = (o = {}) => ({ outside_parties: 0, data_stores: 0, columns_to_check: 0, unpinned_images: 0, production_stacks: 0, routes: 0, size: { files: 0, lines: 0, languages: [] }, ...o });

test("headline scope: production when there is a production stack", () => {
  const x = { total: surface({ production_stacks: 1, outside_parties: 5 }), production: surface({ production_stacks: 1, outside_parties: 3 }), services: [] };
  assert.equal(scopeOf(x).scope, "production");
  assert.equal(scopeOf(x).s.outside_parties, 3);
  assert.equal(scopeOf({ ...x, total: surface({ outside_parties: 5 }) }).scope, "total");
});

test("trend against an earlier map", () => {
  const now = { total: surface({ routes: 4 }), production: surface(), services: [] };
  const then = { total: surface({ routes: 6 }), production: surface(), services: [] };
  assert.equal(delta(now, then, "routes"), -2);
  assert.equal(delta(now, null, "routes"), null);
  assert.equal(deltaWords(-2), "−2");
  assert.equal(deltaWords(3), "+3");
  assert.equal(deltaWords(0), "no change");
  assert.equal(deltaWords(null), "");
  assert.equal(metricsOf({}), null, "a map from rules before 0.8.0");
});

test("size in words", () => {
  assert.equal(short(950), "950");
  assert.equal(short(12345), "12k");
  assert.equal(short(1234), "1.2k");
  const s = { files: 4, lines: 0, languages: [{ language: "go", files: 1, lines: 12345 }, { language: "sql", files: 1, lines: 10 }, { language: "shell", files: 1, lines: 5 }, { language: "python", files: 1, lines: 1 }] };
  assert.equal(languages(s), "go 12k, sql 10, shell 5 and 1 more");
  assert.equal(testLines({ ...s, test_lines: 1234 }), "of which 1.2k test lines");
  assert.equal(testLines(s), "", "older maps have no test lines");
});

test("graph layers, importers left", () => {
  const p = (n, depth) => ({ package: n, language: "go", dir: n, fan_in: 0, fan_out: 0, depth });
  const g = { packages: [p("x/store", 0), p("x/cmd", 2), p("x/billing", 1), p("x/tax", 0)], edges: [], cycles: [], services: [] };
  assert.deepEqual(layers(g).map((l) => l.map((x) => x.package)), [["x/cmd"], ["x/billing"], ["x/store", "x/tax"]]);
  assert.deepEqual(layers(g, ["x/store"]).map((l) => l.length), [1]);
  const t = trimPrefix(["example.com/a/cmd/api", "example.com/a/internal/x"]);
  assert.equal(t("example.com/a/internal/x"), "internal/x");
});
