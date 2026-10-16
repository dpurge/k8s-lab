import { test } from "node:test";
import assert from "node:assert/strict";
import "./testenv.js";
import { load } from "./testenv.js";

await load("format.js");
const f = window.asFormat;

test("duration: no value is a dash", () => {
  for (const v of [null, undefined, NaN, "3", {}, []]) assert.equal(f.duration(v), "—", String(v));
});

test("duration: under ten seconds shows tenths", () => {
  assert.equal(f.duration(0), "0.0 s");
  assert.equal(f.duration(0.04), "0.0 s");
  assert.equal(f.duration(0.44), "0.4 s");
  assert.equal(f.duration(2), "2.0 s");
  assert.equal(f.duration(9.94), "9.9 s");
});

test("duration: rounding never shows 10.0 or 60 s", () => {
  assert.equal(f.duration(9.96), "10 s");
  assert.equal(f.duration(59.4), "59 s");
  assert.equal(f.duration(59.6), "1 min 00 s");
  assert.equal(f.duration(3599.6), "1 h 00 min");
});

test("duration: seconds, minutes and hours", () => {
  assert.equal(f.duration(12), "12 s");
  assert.equal(f.duration(60), "1 min 00 s");
  assert.equal(f.duration(65), "1 min 05 s");
  assert.equal(f.duration(125.4), "2 min 05 s");
  assert.equal(f.duration(3600), "1 h 00 min");
  assert.equal(f.duration(3661), "1 h 01 min");
  assert.equal(f.duration(86400), "24 h 00 min");
});

test("duration: a negative value is shown as zero", () => {
  assert.equal(f.duration(-5), "0.0 s");
});

test("time: missing or invalid timestamps are a dash", () => {
  for (const v of [null, undefined, "", "yesterday", "2026-13-45T99:00:00Z"]) assert.equal(f.time(v), "—", String(v));
});

test("time: formats in the given locale and zone", () => {
  const text = f.time("2026-10-16T12:00:05Z", { locale: "en-GB", timeZone: "UTC" });
  assert.match(text, /16 Oct 2026/);
  assert.match(text, /12:00:05/);
  assert.notEqual(f.time("2026-10-16T12:00:05Z", { locale: "en-GB", timeZone: "Asia/Tokyo" }), text);
});

test("clock: HH:MM:SS with a 24-hour clock, midnight as 00", () => {
  assert.equal(f.clock(new Date("2026-10-16T12:34:56Z"), { timeZone: "UTC" }), "12:34:56");
  assert.equal(f.clock(new Date("2026-10-16T00:00:05Z"), { timeZone: "UTC" }), "00:00:05");
  assert.equal(f.clock(new Date("2026-10-16T23:59:59Z"), { timeZone: "UTC" }), "23:59:59");
});

test("tokens: thousands separators, a dash for no value", () => {
  assert.equal(f.tokens(0), "0");
  assert.equal(f.tokens(999), "999");
  assert.equal(f.tokens(1200), "1,200");
  assert.equal(f.tokens(1234567), "1,234,567");
  for (const v of [null, undefined, NaN, "5"]) assert.equal(f.tokens(v), "—", String(v));
});

test("cost: cents from a dollar up, four decimals below, a dash for no value", () => {
  assert.equal(f.cost(0), "$0.00");
  assert.equal(f.cost(0.00001), "<$0.0001");
  assert.equal(f.cost(0.0001), "$0.0001");
  assert.equal(f.cost(0.0135), "$0.0135");
  assert.equal(f.cost(0.9999), "$0.9999");
  assert.equal(f.cost(1), "$1.00");
  assert.equal(f.cost(12.345), "$12.35");
  for (const v of [null, undefined, NaN, "1"]) assert.equal(f.cost(v), "—", String(v));
});

test("time: short leaves the seconds out", () => {
  const iso = "2026-10-16T12:34:56Z";
  const long = f.time(iso, { locale: "en-US", timeZone: "UTC" });
  const short = f.time(iso, { locale: "en-US", timeZone: "UTC", short: true });
  assert.match(long, /12:34:56/);
  assert.match(short, /12:34/);
  assert.doesNotMatch(short, /:56/);
  assert.match(short, /2026/);
});
