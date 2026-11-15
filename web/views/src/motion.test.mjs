// The progress bar between server updates (feedback C1.2). Run with `npm test`.
import assert from "node:assert/strict";
import test from "node:test";
import { progressStep } from "./motion.ts";

// Run the bar for ms milliseconds in 16 ms frames.
const run = (shown, real, total, rate, ms) => {
  for (let t = 0; t < ms; t += 16) shown = progressStep(shown, real, total, rate, 16);
  return shown;
};

test("catches up with a new real value within about half a second", () => {
  const v = run(3, 4, 26, 0, 600);
  assert.ok(v > 3.95 && v <= 4, String(v));
});

test("never moves backwards", () => {
  assert.equal(progressStep(5, 4, 26, 0.001, 16), 5);
  assert.equal(progressStep(5, 0, 26, 0, 16), 5);
});

test("creeps on between updates, never more than one step past the real value", () => {
  const v = run(4, 4, 26, 1 / 3000, 60_000);
  assert.ok(v > 4 && v <= 5, String(v));
});

test("never passes the total", () => {
  assert.ok(run(25.5, 26, 26, 1, 5_000) <= 26);
  assert.ok(run(25.9, 25.9, 26, 1, 5_000) <= 26);
});

test("without an observed rate it waits at the real value", () => {
  assert.equal(run(4, 4, 26, 0, 5_000), 4);
});
