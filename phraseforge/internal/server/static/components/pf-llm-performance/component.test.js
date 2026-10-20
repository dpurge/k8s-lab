import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("./component.js");

const labels = {
  heading: "Performance", period: "Period", groupBy: "Group by", hint: "Successful attempts only.",
  window1h: "Last hour", window24h: "Last 24 hours", window7d: "Last 7 days", window30d: "Last 30 days", windowAll: "All time",
  groupModel: "Per model", groupKindModel: "Per purpose and model",
  colModel: "Model", colCalls: "Calls", colSuccess: "OK %", colTime: "Time s", colTokens: "Tokens",
  colTokensPerSecond: "Tok/s", colCost: "Cost $", none: "Nothing yet",
  tipCalls: "Attempts", tipSuccess: "Success", tipTime: "Mean / median / p95 (s)", tipTokens: "Tokens in / out",
  tipTokensPerSecond: "Output tokens/s", tipCost: "Cost (USD)",
  outcomeOk: "OK", outcomeError: "Error", outcomeTimeout: "Timed out", outcomeRateLimited: "Rate limited", outcomeCanceled: "Canceled",
};

function mount(items, groupBy = "model") {
  const el = document.createElement("pf-llm-performance");
  el.labels = labels;
  el.locale = "en";
  el.groupBy = groupBy;
  el.items = items;
  document.body.appendChild(el);
  return el;
}

const full = {
  model_id: "alpha", provider_id: "ollama", kind: "title", calls: 1234, ok_calls: 1000, error_calls: 200, timeout_calls: 30,
  rate_limited_calls: 3, canceled_calls: 1, success_rate: 0.81037, input_tokens: 5000, output_tokens: 12345,
  mean_duration_ms: 2345, p50_duration_ms: 2000, p95_duration_ms: 9876, output_tokens_per_second: 41.26, cost_usd: 0.5,
};
const noOK = {
  model_id: "beta", provider_id: "vertex", kind: "title", calls: 2, ok_calls: 0, error_calls: 2, timeout_calls: 0,
  rate_limited_calls: 0, canceled_calls: 0, success_rate: 0, input_tokens: 0, output_tokens: 0,
  mean_duration_ms: null, p50_duration_ms: null, p95_duration_ms: null, output_tokens_per_second: null, cost_usd: 0,
};

const cells = (row) => [...row.children].map((c) => c.textContent);

test("renders seven compact columns with Intl formatting and merged cells", () => {
  const el = mount([full]);
  assert.deepEqual(cells(el.querySelector("tbody tr")), [
    "alphaollama", "1,234", "81%", "2.3 / 2.0 / 9.9", "5,000 / 12,345", "41.3", "0.5000",
  ]);
  assert.deepEqual([...el.querySelectorAll("th")].map((h) => h.textContent), [
    "Model", "Calls", "OK %", "Time s", "Tokens", "Tok/s", "Cost $",
  ]);
});

test("every header but Model has the full wording as its title", () => {
  const el = mount([full]);
  assert.deepEqual([...el.querySelectorAll("th")].map((h) => h.title), [
    "", "Attempts", "Success", "Mean / median / p95 (s)", "Tokens in / out", "Output tokens/s", "Cost (USD)",
  ]);
});

test("the provider is a muted second line under the model id", () => {
  const model = mount([full]).querySelector("tbody td");
  assert.equal(model.firstChild.textContent, "alpha");
  assert.equal(model.querySelector(".pf-llm-sub").textContent, "ollama");
});

test("null durations and speed are dashes, a zero rate and cost are numbers", () => {
  const el = mount([noOK]);
  assert.deepEqual(cells(el.querySelector("tbody tr")), ["betavertex", "2", "0%", "— / — / —", "0 / 0", "—", "0.0000"]);
});

test("a missing part of the time cell is a dash, the others stay", () => {
  const el = mount([{ ...full, mean_duration_ms: null }]);
  assert.equal(el.querySelector("tbody tr").children[3].textContent, "— / 2.0 / 9.9");
});

test("the calls cell names the count per outcome in its title", () => {
  const el = mount([full]);
  assert.equal(
    el.querySelector("tbody tr").children[1].title,
    "OK: 1,000, Error: 200, Timed out: 30, Rate limited: 3, Canceled: 1",
  );
});

test("grouping by purpose and model puts the kind label before the provider in the second line", () => {
  const el = mount([full], "kind_model");
  el.kindLabel = (kind) => `kind:${kind}`;
  el.items = [full];
  assert.equal(el.querySelectorAll("th").length, 7, "no extra column");
  assert.equal(el.querySelector(".pf-llm-sub").textContent, "kind:title · ollama");
});

test("no items shows the empty text across all columns", () => {
  const el = mount([]);
  const cell = el.querySelector("tbody td");
  assert.equal(cell.textContent, "Nothing yet");
  assert.equal(cell.colSpan, 7);
});

test("the table sits in a scroll container that is only a safety net", () => {
  const el = mount([full]);
  assert.ok(el.querySelector(".pf-llm-scroll > table"));
  const css = readFileSync(new URL("./component.css", import.meta.url), "utf8");
  const widthRules = css.split("\n").filter((line) => /min-width|white-space: nowrap/.test(line));
  for (const rule of widthRules) {
    assert.ok(/select|td\.pf-llm-number/.test(rule.split("{")[0]), `only the select and numeric cells constrain width: ${rule}`);
  }
  assert.match(css, /\.pf-llm-scroll \{ overflow-x: auto; \}/);
});

test("choosing a period dispatches pf-llm-performance-change with its since", () => {
  const el = mount([full]);
  const seen = [];
  el.addEventListener("pf-llm-performance-change", (e) => seen.push(e.detail));
  const select = el.querySelector("select");
  assert.deepEqual([...select.options].map((o) => o.value), ["1h", "24h", "7d", "30d", "all"]);
  assert.equal(select.value, "24h");
  const before = Date.now();
  select.value = "7d";
  select.dispatchEvent(new dom.window.Event("change"));
  assert.equal(seen.length, 1);
  assert.equal(seen[0].window, "7d");
  assert.equal(seen[0].groupBy, "model");
  const sinceMs = Date.parse(seen[0].since);
  assert.ok(Math.abs(before - sinceMs - 7 * 24 * 3600 * 1000) < 5000, seen[0].since);
  assert.equal(el.windowValue, "7d");
  select.value = "all";
  select.dispatchEvent(new dom.window.Event("change"));
  assert.equal(seen[1].since, "1970-01-01T00:00:00Z");
});

test("sinceFor counts back from now", () => {
  const Performance = customElements.get("pf-llm-performance");
  const now = new Date("2026-10-20T12:00:00Z");
  assert.equal(Performance.sinceFor("1h", now), "2026-10-20T11:00:00.000Z");
  assert.equal(Performance.sinceFor("30d", now), "2026-09-20T12:00:00.000Z");
  assert.equal(Performance.sinceFor("all", now), "1970-01-01T00:00:00Z");
});

test("the grouping toggle marks the active grouping and dispatches once per change", () => {
  const el = mount([full]);
  const seen = [];
  el.addEventListener("pf-llm-performance-change", (e) => seen.push(e.detail));
  const buttons = () => [...el.querySelectorAll(".pf-llm-toggle button")];
  assert.deepEqual(buttons().map((b) => b.getAttribute("aria-pressed")), ["true", "false"]);
  buttons()[0].click();
  assert.equal(seen.length, 0, "choosing the active grouping again is not a change");
  buttons()[1].click();
  assert.deepEqual(seen.map((d) => d.groupBy), ["kind_model"]);
  assert.deepEqual(buttons().map((b) => b.getAttribute("aria-pressed")), ["false", "true"]);
  assert.equal(el.groupBy, "kind_model");
  assert.equal(el.querySelectorAll("th").length, 7, "the purpose is a second line, not a column");
});

test("new items keep the selected period", () => {
  const el = mount([full]);
  const select = el.querySelector("select");
  select.value = "1h";
  select.dispatchEvent(new dom.window.Event("change"));
  el.items = [noOK];
  assert.equal(el.querySelector("select"), select, "the select is not rebuilt, so it keeps its focus");
  assert.equal(el.querySelector("select").value, "1h");
});

test("a model id is rendered as text, never as markup", () => {
  const el = mount([{ ...full, model_id: "<img src=x onerror=alert(1)>" }]);
  assert.equal(el.querySelector("img"), null);
});

test("injects its stylesheet once", () => {
  mount([]);
  mount([]);
  assert.equal(document.querySelectorAll("#pf-llm-performance-style").length, 1);
});
