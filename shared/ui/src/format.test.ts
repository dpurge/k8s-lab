import { test } from "node:test";
import assert from "node:assert/strict";
import { DASH, compactTime, cost, duration, fullTime, tokens } from "./format.ts";

test("tokens have thousands separators and no value is a dash", () => {
  assert.equal(tokens(1500000), "1,500,000");
  assert.equal(tokens(0), "0");
  assert.equal(tokens(12.6), "13");
  for (const bad of [undefined, null, "5", NaN]) assert.equal(tokens(bad), DASH);
});

test("cost is cents from a dollar up and four decimals below it", () => {
  assert.equal(cost(12.3456), "$12.35");
  assert.equal(cost(1), "$1.00");
  assert.equal(cost(0.5), "$0.5000");
  assert.equal(cost(0.0123), "$0.0123");
  assert.equal(cost(0), "$0.00");
  assert.equal(cost(0.00001), "<$0.0001");
  for (const bad of [undefined, null, "1", NaN]) assert.equal(cost(bad), DASH);
});

test("duration reads in seconds, minutes and hours", () => {
  assert.equal(duration(400), "0.4 s");
  assert.equal(duration(1500), "1.5 s");
  assert.equal(duration(12000), "12 s");
  assert.equal(duration(185000), "3 min 05 s");
  assert.equal(duration(3720000), "1 h 02 min");
  assert.equal(duration(-5), "0.0 s");
  for (const bad of [undefined, null, NaN]) assert.equal(duration(bad), DASH);
});

test("times: a bad or missing timestamp is a dash, a good one has no year when compact", () => {
  for (const bad of [undefined, null, "", "yesterday"]) {
    assert.equal(compactTime(bad), DASH);
    assert.equal(fullTime(bad), DASH);
  }
  assert.match(compactTime("2026-10-16T12:00:00Z"), /Oct 1[56]/);
  assert.doesNotMatch(compactTime("2026-10-16T12:00:00Z"), /2026/);
  assert.match(fullTime("2026-10-16T12:00:00Z"), /2026/);
});
