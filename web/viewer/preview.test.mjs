// The upload panel's pure parts (src/preview.ts). Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { baseName, counts, preview } from "./src/preview.ts";

test("a short file is shown whole, a long one is cut with the number of lines left", () => {
  assert.equal(preview('{"a":1}', 40), '{\n  "a": 1\n}');
  const long = JSON.stringify({ xs: Array.from({ length: 100 }, (_, i) => i) });
  const lines = preview(long, 40).split("\n");
  assert.equal(lines.length, 41);
  assert.equal(lines.at(-1), "… 64 more lines");
});

test("the saved names drop .json and .facts.json", () => {
  assert.equal(baseName("shop.facts.json"), "shop");
  assert.equal(baseName("shop.json"), "shop");
  assert.equal(baseName("shop"), "shop");
});

test("counts name every category, missing ones as 0", () => {
  assert.equal(counts({ path: 1200, name: 3, host: 0 }), "1,200 paths, 3 names, 0 hosts, 0 repo id");
});
