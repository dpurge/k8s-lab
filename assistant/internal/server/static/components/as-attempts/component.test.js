import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, user, attemptRow, tasksApi, USER_ID } from "../testenv.js";

await load("format.js", "api.js", "components/as-attempts/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

// dp-pager is the shared Lit component (tested in shared/ui), which this jsdom
// does not load: the attributes the list sets on it are what is checked.
async function mount(attrs = {}, handler = tasksApi({ attempts: [attemptRow()] })) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-attempts");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

// The table itself is the shared dp-attempt-table (tested in shared/ui); what is
// checked here is the rows this element hands to it.
const table = (el) => el.querySelector("dp-attempt-table");
const rows = (el) => table(el).rows;
const attemptCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/llm-calls"));
const many = (n) => Array.from({ length: n }, (_, i) => attemptRow({ id: `cccccccc-0000-4000-8000-${String(i).padStart(12, "0")}`, input_tokens: i }));

test("one row per attempt with its user, provider, model, outcome, tokens, cost and duration", async () => {
  const { el } = await mount({}, tasksApi({ attempts: [attemptRow({ input_tokens: 1500, output_tokens: 30, cost_usd: 12.3456, duration_ms: 90000 }), attemptRow({ outcome: "timeout", error: "deadline", input_tokens: 0, output_tokens: 0, cost_usd: 0, duration_ms: 500 })] }));
  assert.deepEqual(rows(el), [
    { startedAt: "2026-10-16T12:00:00Z", user: "Ada", userId: USER_ID, provider: "gpu", model: "big", outcome: "ok", error: null, inputTokens: 1500, outputTokens: 30, costUsd: 12.3456, durationMs: 90000 },
    { startedAt: "2026-10-16T12:00:00Z", user: "Ada", userId: USER_ID, provider: "gpu", model: "big", outcome: "timeout", error: "deadline", inputTokens: 0, outputTokens: 0, costUsd: 0, durationMs: 500 },
  ]);
  assert.equal(table(el).getAttribute("empty-text"), "No model calls in this period.");
});

test("a page asks for its slice and sets the pager from the total", async () => {
  const { el, calls } = await mount({ page: 2, "page-size": 10 }, tasksApi({ attempts: many(35) }));
  assert.equal(rows(el).length, 10);
  assert.equal(rows(el)[0].inputTokens, 10, "page 2 starts at the eleventh attempt");
  const url = new URL(attemptCalls(calls)[0], "http://x");
  assert.equal(url.searchParams.get("limit"), "10");
  assert.equal(url.searchParams.get("offset"), "10");
  const pager = el.querySelector("dp-pager");
  assert.equal(pager.getAttribute("page"), "2");
  assert.equal(pager.getAttribute("page-count"), "4");
  assert.equal(el.querySelector(".as-attempts-summary").textContent, "35 attempts");
});

test("the default is 25 a page", async () => {
  const { calls } = await mount({}, tasksApi({ attempts: many(60) }));
  const url = new URL(attemptCalls(calls)[0], "http://x");
  assert.equal(url.searchParams.get("limit"), "25");
  assert.equal(url.searchParams.get("offset"), "0");
  assert.equal(url.searchParams.has("since"), false, "all time asks for no since");
});

test("a period asks for the time it starts", async () => {
  mock.timers.enable({ apis: ["Date"], now: new Date("2026-10-16T12:00:00Z") });
  const { calls } = await mount({ period: "7d" });
  assert.equal(new URL(attemptCalls(calls)[0], "http://x").searchParams.get("since"), "2026-10-09T12:00:00.000Z");
});

test("picking a period asks the host, from the first page, and changes nothing itself", async () => {
  const { el, calls } = await mount({ page: 3 }, tasksApi({ attempts: many(80) }));
  const seen = [];
  el.addEventListener("as-list-change", (e) => seen.push(e.detail));
  const select = el.querySelector("select");
  select.value = "24h";
  select.dispatchEvent(new window.Event("change"));
  assert.deepEqual(seen, [{ period: "24h", page: 1 }]);
  assert.equal(el.getAttribute("period"), null);
  assert.equal(attemptCalls(calls).length, 1);
});

test("the pager asks the host for a page, keeping the period", async () => {
  const { el } = await mount({ period: "30d" }, tasksApi({ attempts: many(80) }));
  const seen = [];
  el.addEventListener("as-list-change", (e) => seen.push(e.detail));
  el.querySelector("dp-pager").dispatchEvent(new window.CustomEvent("dp-pager-select", { detail: { page: 2 }, bubbles: true }));
  assert.deepEqual(seen, [{ period: "30d", page: 2 }]);
});

test("a page past the end asks the host for the last one", async () => {
  const { el } = await mount({ page: 9 }, tasksApi({ attempts: many(30) }));
  const seen = [];
  el.addEventListener("as-list-change", (e) => seen.push(e.detail));
  el.setAttribute("page", "8");
  await flush();
  assert.deepEqual(seen.at(-1), { period: "all", page: 2 });
});

test("no attempts hands the table no rows", async () => {
  const { el } = await mount({}, tasksApi({ attempts: [] }));
  assert.deepEqual(rows(el), []);
});

test("a failure is shown and reported, and the rows of a failed load are not shown", async () => {
  reset();
  mockFetch(tasksApi({ attemptsStatus: 500 }));
  const el = document.createElement("as-attempts");
  const loads = [];
  document.addEventListener("as-load", (e) => loads.push(e.detail));
  document.body.appendChild(el);
  await flush();
  assert.match(el.querySelector(".as-attempts-message").textContent, /^Could not load attempts: /);
  assert.equal(loads.at(-1).ok, false);
  assert.deepEqual(rows(el), []);
});

test("an unknown user shows the short id; names are handed over as plain strings", async () => {
  const { el } = await mount({}, tasksApi({ attempts: [attemptRow({ model_id: "<img src=x onerror=alert(1)>" }), attemptRow({ user_id: "bbbbbbbb-0000-4000-8000-000000000002" })], users: [user(USER_ID, "<b>Ada</b>")] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
  assert.equal(rows(el)[0].user, "<b>Ada</b>");
  assert.equal(rows(el)[0].model, "<img src=x onerror=alert(1)>");
  assert.equal(rows(el)[1].user, "bbbbbbbb");
  assert.equal(rows(el)[1].userId, "bbbbbbbb-0000-4000-8000-000000000002");
});

test("a response for an earlier page is ignored", async () => {
  reset();
  const pending = [];
  mockFetch((url) => {
    if (url.startsWith("/api/v1/users")) return jsonResponse(200, { items: [user(USER_ID, "Ada")] });
    return new Promise((resolve) => pending.push(resolve));
  });
  const el = document.createElement("as-attempts");
  document.body.appendChild(el);
  await flush();
  el.setAttribute("page", "2");
  await flush();
  const body = (id) => jsonResponse(200, { items: [attemptRow({ model_id: id })], total: 60, limit: 25, offset: 0 });
  pending[1](body("newer"));
  await flush();
  pending[0](body("older"));
  await flush();
  assert.deepEqual(rows(el).map((r) => r.model), ["newer"]);
});
