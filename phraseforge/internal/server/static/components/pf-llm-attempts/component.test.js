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
  heading: "Recent attempts", filterModel: "Model", filterOutcome: "Outcome", filterAll: "all",
  colTime: "Time", colPurpose: "Purpose", colModel: "Model", colOutcome: "Outcome", colDuration: "Duration (s)",
  colTokens: "Tokens", tipTokens: "Tokens in / out", colHttp: "HTTP", request: "request", none: "No attempts",
  outcomeOk: "OK", outcomeError: "Error", outcomeTimeout: "Timed out", outcomeRateLimited: "Rate limited", outcomeCanceled: "Canceled",
};

function mount(items) {
  const el = document.createElement("pf-llm-attempts");
  el.labels = labels;
  el.locale = "en";
  el.modelIds = ["alpha", "beta"];
  el.kindLabel = (kind) => `kind:${kind}`;
  el.items = items;
  document.body.appendChild(el);
  return el;
}

const longError = "upstream said no: " + "x".repeat(400);
const failed = {
  id: "1", started_at: "2026-10-20T12:34:56Z", kind: "translation", source_language: "deu", target_language: "eng",
  job_id: "7b0a7a63-6b8f-4c43-9d0e-3f3b6a1d2c11", model_id: "beta", provider_id: "vertex", model_name: "n", tags: [],
  input_tokens: 1500, output_tokens: 20, cost_usd: 0, duration_ms: 2345, outcome: "rate_limited", error: longError,
  http_status: 429, request_id: "req-1",
};
const interactive = {
  id: "2", started_at: "2026-10-20T12:00:00Z", kind: "title", source_language: "deu", target_language: "title", job_id: null,
  model_id: "alpha", provider_id: "ollama", model_name: "n", tags: [], input_tokens: 5, output_tokens: 6, cost_usd: 0,
  duration_ms: 100, outcome: "ok", error: null, http_status: null, request_id: null,
};

const cells = (row) => [...row.children].map((c) => c.textContent);
const rowsOf = (el) => [...el.querySelectorAll("tbody tr")];

test("renders six columns on the first line", () => {
  const el = mount([failed]);
  const expectedTime = new Intl.DateTimeFormat("en", { dateStyle: "short", timeStyle: "short" }).format(new Date(failed.started_at));
  assert.deepEqual(cells(rowsOf(el)[0]), [expectedTime, "kind:translation (deu → eng)", "beta", "Rate limited", "2.3", "1,500 / 20"]);
  assert.deepEqual([...el.querySelectorAll("th")].map((h) => h.textContent), ["Time", "Purpose", "Model", "Outcome", "Duration (s)", "Tokens"]);
  assert.equal(el.querySelector("th:last-child").title, "Tokens in / out");
  assert.equal(el.querySelector(".pf-llm-outcome").dataset.outcome, "rate_limited");
});

test("a failed attempt has a muted second line with status, request id and error", () => {
  const el = mount([failed]);
  const rows = rowsOf(el);
  assert.equal(rows.length, 2);
  assert.equal(rows[1].className, "pf-llm-detail");
  assert.equal(rows[1].children.length, 1);
  assert.equal(rows[1].children[0].colSpan, 6);
  assert.equal(rows[1].querySelector(".pf-llm-facts").textContent, "HTTP 429 · request req-1");
  assert.equal(rows[1].querySelector(".pf-llm-error").textContent, longError);
});

test("the whole error text is in the title of the clamped error", () => {
  const el = mount([failed]);
  assert.equal(el.querySelector(".pf-llm-error").title, longError);
  const css = readFileSync(new URL("./component.css", import.meta.url), "utf8");
  assert.match(css, /-webkit-line-clamp: 2/);
  assert.match(css, /user-select: text/);
});

test("an attempt with no status, request id or error has no second line", () => {
  const el = mount([interactive]);
  assert.equal(rowsOf(el).length, 1);
  assert.equal(el.querySelector(".pf-llm-detail"), null);
  assert.equal(cells(rowsOf(el)[0])[3], "OK");
});

test("only the parts that exist are shown in the second line", () => {
  const onlyStatus = mount([{ ...interactive, http_status: 500 }]);
  assert.equal(onlyStatus.querySelector(".pf-llm-facts").textContent, "HTTP 500");
  assert.equal(onlyStatus.querySelector(".pf-llm-error"), null);
  const onlyError = mount([{ ...interactive, error: "boom" }]);
  assert.equal(onlyError.querySelector(".pf-llm-facts"), null);
  assert.equal(onlyError.querySelector(".pf-llm-error").textContent, "boom");
  const onlyRequest = mount([{ ...interactive, request_id: "r9" }]);
  assert.equal(onlyRequest.querySelector(".pf-llm-facts").textContent, "request r9");
});

test("the table sits in a scroll container that is only a safety net", () => {
  assert.ok(mount([failed]).querySelector(".pf-llm-scroll > table"));
  const css = readFileSync(new URL("./component.css", import.meta.url), "utf8");
  const widthRules = css.split("\n").filter((line) => /min-width|white-space: nowrap/.test(line));
  for (const rule of widthRules) {
    assert.ok(/select|td\.pf-llm-number/.test(rule.split("{")[0]), `only the select and numeric cells constrain width: ${rule}`);
  }
  assert.match(css, /\.pf-llm-scroll \{ overflow-x: auto; \}/);
});

test("no attempts shows the empty text across all columns", () => {
  const cell = mount([]).querySelector("tbody td");
  assert.equal(cell.textContent, "No attempts");
  assert.equal(cell.colSpan, 6);
});

test("the filters offer all models and the five outcomes", () => {
  const el = mount([]);
  const model = el.querySelector('select[data-filter="model"]');
  const outcome = el.querySelector('select[data-filter="outcome"]');
  assert.deepEqual([...model.options].map((o) => o.value), ["", "alpha", "beta"]);
  assert.deepEqual([...outcome.options].map((o) => o.value), ["", "ok", "error", "timeout", "rate_limited", "canceled"]);
  assert.equal(model.options[0].textContent, "Model: all");
});

test("choosing a filter dispatches pf-llm-attempts-filter with both filters", () => {
  const el = mount([interactive]);
  const seen = [];
  el.addEventListener("pf-llm-attempts-filter", (e) => seen.push(e.detail));
  const model = el.querySelector('select[data-filter="model"]');
  model.value = "beta";
  model.dispatchEvent(new dom.window.Event("change"));
  const outcome = el.querySelector('select[data-filter="outcome"]');
  outcome.value = "error";
  outcome.dispatchEvent(new dom.window.Event("change"));
  assert.deepEqual(seen, [
    { modelId: "beta", outcome: "" },
    { modelId: "beta", outcome: "error" },
  ]);
});

test("new items keep the chosen filters and the select on screen", () => {
  const el = mount([interactive]);
  const outcome = el.querySelector('select[data-filter="outcome"]');
  outcome.value = "timeout";
  outcome.dispatchEvent(new dom.window.Event("change"));
  el.items = [failed];
  assert.equal(el.querySelector('select[data-filter="outcome"]'), outcome);
  assert.equal(outcome.value, "timeout");
});

test("provider text is rendered as text, never as markup", () => {
  const el = mount([{ ...failed, error: "<img src=x onerror=alert(1)>", request_id: "<b>r</b>" }]);
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
  assert.ok(el.textContent.includes("<img"));
});

test("injects its stylesheet once", () => {
  mount([]);
  mount([]);
  assert.equal(document.querySelectorAll("#pf-llm-attempts-style").length, 1);
});
