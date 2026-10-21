import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, user, usageRow, tasksApi, USER_ID } from "../testenv.js";

await load("format.js", "api.js", "components/as-usage/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const BOB = "bbbbbbbb-0000-4000-8000-000000000002";

async function mount(attrs = {}, handler = tasksApi({ usage: [usageRow()] })) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-usage");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

// The table itself is the shared dp-usage-table (tested in shared/ui); what is
// checked here is the rows this element hands to it.
const table = (el) => el.querySelector("dp-usage-table");
const rows = (el) => table(el).rows;
const shown = (el) => rows(el).map((r) => [r.user, r.provider, r.model, r.calls, r.inputTokens, r.outputTokens, r.costUsd]);
const usageCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/usage"));

test("one row per user, provider and model, with the user's name", async () => {
  const { el } = await mount({}, tasksApi({
    usage: [usageRow({ calls: 1200, input_tokens: 1500000, output_tokens: 30000, cost_usd: 12.3456 }), usageRow({ user_id: BOB, provider_id: "laptop", model_id: "small", calls: 1, input_tokens: 10, output_tokens: 2, cost_usd: 0 })],
    users: [user(USER_ID, "Ada"), user(BOB, "Bob")],
  }));
  assert.deepEqual(shown(el), [
    ["Ada", "gpu", "big", 1200, 1500000, 30000, 12.3456],
    ["Bob", "laptop", "small", 1, 10, 2, 0],
  ]);
  assert.equal(rows(el)[0].userId, USER_ID);
  assert.equal(table(el).getAttribute("empty-text"), "No model calls in this period.");
});

test("the summary adds up calls and cost", async () => {
  const { el } = await mount({}, tasksApi({ usage: [usageRow({ calls: 2, cost_usd: 3 }), usageRow({ user_id: BOB, model_id: "small", calls: 1, cost_usd: 0.5 })] }));
  assert.equal(el.querySelector(".as-usage-summary").textContent, "3 calls · $3.50");
});

test("an unknown user shows the short id, and the full id as a tooltip", async () => {
  const { el } = await mount({}, tasksApi({ usage: [usageRow({ user_id: BOB })], users: [user(USER_ID, "Ada")] }));
  assert.equal(rows(el)[0].user, "bbbbbbbb");
  assert.equal(rows(el)[0].userId, BOB, "the full id is what the table shows as a tooltip");
});

test("a failure to load users still shows the rows", async () => {
  const { el } = await mount({}, tasksApi({ usage: [usageRow()], usersStatus: 500 }));
  assert.equal(rows(el).length, 1);
  assert.equal(rows(el)[0].user, "aaaaaaaa");
});

test("no usage hands the table no rows and clears the summary", async () => {
  const { el } = await mount({}, tasksApi({ usage: [] }));
  assert.deepEqual(rows(el), []);
  assert.equal(el.querySelector(".as-usage-summary").textContent, "");
});

test("all time asks for no since; a period asks for the time it starts", async () => {
  const { calls } = await mount();
  assert.deepEqual(usageCalls(calls), ["/api/v1/usage"]);

  mock.timers.enable({ apis: ["Date"], now: new Date("2026-10-16T12:00:00Z") });
  const week = await mount({ period: "7d" });
  assert.deepEqual(usageCalls(week.calls), [`/api/v1/usage?since=${encodeURIComponent("2026-10-09T12:00:00.000Z")}`]);
  const day = await mount({ period: "24h" });
  assert.deepEqual(usageCalls(day.calls), [`/api/v1/usage?since=${encodeURIComponent("2026-10-15T12:00:00.000Z")}`]);
  const month = await mount({ period: "30d" });
  assert.deepEqual(usageCalls(month.calls), [`/api/v1/usage?since=${encodeURIComponent("2026-09-16T12:00:00.000Z")}`]);
});

test("a nonsense period means all time", async () => {
  const { el, calls } = await mount({ period: "forever; drop table" });
  assert.deepEqual(usageCalls(calls), ["/api/v1/usage"]);
  assert.equal(el.querySelector("select").value, "all");
});

test("picking a period asks the host to change it, and changes nothing itself", async () => {
  const { el, calls } = await mount();
  const seen = [];
  el.addEventListener("as-list-change", (e) => seen.push(e.detail));
  const select = el.querySelector("select");
  select.value = "7d";
  select.dispatchEvent(new window.Event("change"));
  assert.deepEqual(seen, [{ period: "7d", group: "" }]);
  assert.equal(el.getAttribute("period"), null);
  assert.equal(usageCalls(calls).length, 1);
});

test("the host changing the period loads it and moves the select", async () => {
  const { el, calls } = await mount();
  el.setAttribute("period", "30d");
  await flush();
  assert.equal(el.querySelector("select").value, "30d");
  assert.equal(usageCalls(calls).length, 2);
  assert.match(usageCalls(calls)[1], /since=/);
});

test("an error shows the server's message and as-load reports it", async () => {
  const events = [];
  reset();
  mockFetch(tasksApi({ usageStatus: 500 }));
  const el = document.createElement("as-usage");
  el.addEventListener("as-load", (e) => events.push(e.detail));
  document.body.appendChild(el);
  await flush();
  assert.equal(el.querySelector(".as-usage-message").textContent, "Could not load usage: internal error");
  assert.ok(el.querySelector(".as-usage-message").classList.contains("error"));
  assert.equal(events.length, 1);
  assert.equal(events[0].ok, false);
  assert.equal(events[0].live, false);
});

test("a success reports ok, and a later success clears an earlier error", async () => {
  reset();
  let fail = true;
  mockFetch((url) => (fail && url.startsWith("/api/v1/usage") ? jsonResponse(500, { error: { message: "boom" } }) : tasksApi({ usage: [usageRow()] })(url)));
  const el = document.createElement("as-usage");
  document.body.appendChild(el);
  await flush();
  assert.ok(el.querySelector(".as-usage-message").classList.contains("error"));
  fail = false;
  el.setAttribute("period", "7d");
  await flush();
  assert.equal(el.querySelector(".as-usage-message").classList.contains("error"), false);
  assert.equal(el.querySelector(".as-usage-message").textContent, "");
  assert.equal(rows(el).length, 1);
});

test("a slow response for an earlier period is ignored", async () => {
  reset();
  const pending = [];
  mockFetch((url) => {
    if (url.startsWith("/api/v1/users")) return jsonResponse(200, { items: [user(USER_ID, "Ada")] });
    return new Promise((resolve) => pending.push(resolve));
  });
  const el = document.createElement("as-usage");
  document.body.appendChild(el);
  await flush();
  el.setAttribute("period", "7d");
  await flush();
  pending[1](jsonResponse(200, { items: [usageRow({ model_id: "newer" })] }));
  await flush();
  pending[0](jsonResponse(200, { items: [usageRow({ model_id: "older" })] }));
  await flush();
  assert.deepEqual(rows(el).map((r) => r.model), ["newer"]);
});

test("model ids and users reach the page as text, never as markup", async () => {
  const { el } = await mount({}, tasksApi({ usage: [usageRow({ model_id: "<img src=x onerror=alert(1)>" })], users: [user(USER_ID, "<b>Ada</b>")] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
  assert.deepEqual([rows(el)[0].user, rows(el)[0].model], ["<b>Ada</b>", "<img src=x onerror=alert(1)>"], "handed over as plain strings; the table draws them as text");
});

test("loading marks the element busy until it is done", async () => {
  reset();
  let release;
  mockFetch((url) => (url.startsWith("/api/v1/usage") ? new Promise((r) => (release = () => r(jsonResponse(200, { items: [] })))) : jsonResponse(200, { items: [] })));
  const el = document.createElement("as-usage");
  document.body.appendChild(el);
  assert.equal(el.getAttribute("aria-busy"), "true");
  release();
  await flush();
  assert.equal(el.hasAttribute("aria-busy"), false);
});

test("a note says the usage covers only the retained call history", async () => {
  const { el } = await mount();
  assert.match(el.querySelector(".as-usage-note").textContent, /retained call history/);
});

// ── grouping ─────────────────────────────────────────────────────────────────

// The summing is dp-usage-table's (tested in shared/ui); what is checked here is
// that the table gets the finest-grain rows and the grouping.

const FINE = tasksApi({
  usage: [
    usageRow({ calls: 2, input_tokens: 100, output_tokens: 10, cost_usd: 3 }),
    usageRow({ provider_id: "cloud", model_id: "big", calls: 1, input_tokens: 50, output_tokens: 5, cost_usd: 2 }),
    usageRow({ user_id: BOB, model_id: "small", calls: 4, input_tokens: 40, output_tokens: 4, cost_usd: 0.5 }),
  ],
  users: [user(USER_ID, "Ada"), user(BOB, "Bob")],
});

test("the table gets every row and the grouping from the attribute", async () => {
  for (const group of ["user", "provider", "model"]) {
    const { el } = await mount({ group }, FINE);
    assert.equal(table(el).groupBy, group);
    assert.equal(rows(el).length, 3, "the rows are not summed here");
  }
});

test("no group, or a nonsense one, is no grouping", async () => {
  assert.equal(table((await mount({}, FINE)).el).groupBy, "");
  assert.equal(table((await mount({ group: "x" }, FINE)).el).groupBy, "");
});

test("picking a grouping asks the host and keeps the period; the host changing it regroups without a new request", async () => {
  const { el, calls } = await mount({ period: "7d" }, FINE);
  const seen = [];
  el.addEventListener("as-list-change", (e) => seen.push(e.detail));
  const select = el.querySelector(".as-usage-group");
  select.value = "model";
  select.dispatchEvent(new window.Event("change"));
  assert.deepEqual(seen, [{ period: "7d", group: "model" }]);
  assert.equal(el.getAttribute("group"), null);
  const before = usageCalls(calls).length;
  el.setAttribute("group", "model");
  await flush();
  assert.equal(usageCalls(calls).length, before, "no new request");
  assert.equal(table(el).groupBy, "model");
  assert.equal(select.value, "model");
});

test("the summary does not change with the grouping", async () => {
  const { el } = await mount({ group: "provider" }, FINE);
  assert.equal(el.querySelector(".as-usage-summary").textContent, "7 calls · $5.50");
});
