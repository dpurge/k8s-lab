import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, modelInfo, tasksApi } from "../testenv.js";

await load("format.js", "api.js", "components/as-model-list/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

async function mount(attrs = {}, handler = tasksApi({ models: [modelInfo()] })) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-model-list");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

// The table itself is the shared dp-model-table (tested in shared/ui); what is
// checked here is the rows this element hands to it.
const table = (el) => el.querySelector("dp-model-table");
const rows = (el) => table(el).rows;
const modelCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/models"));

test("one row per model with its provider, tags, priority and availability", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo(), modelInfo({ id: "small", provider: "laptop", tags: ["chat"], priority: 10 })] }));
  assert.deepEqual(rows(el), [
    { provider: "gpu", model: "big", tags: ["chat", "strong"], priority: 20, state: "available", until: undefined },
    { provider: "laptop", model: "small", tags: ["chat"], priority: 10, state: "available", until: undefined },
  ]);
  assert.equal(table(el).getAttribute("empty-text"), "No models are configured.");
});

test("models are listed in the order they are tried: priority, then file order", async () => {
  const { el } = await mount({}, tasksApi({
    models: [modelInfo({ id: "low", priority: 1 }), modelInfo({ id: "first-tie", priority: 5 }), modelInfo({ id: "high", priority: 9 }), modelInfo({ id: "second-tie", priority: 5 })],
  }));
  assert.deepEqual(rows(el).map((r) => r.model), ["high", "first-tie", "second-tie", "low"]);
});

test("a model the breaker holds out is held out, with the time it returns", async () => {
  const until = "2026-10-16T12:01:00Z";
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ available: false, unavailable_until: until })] }));
  assert.equal(rows(el)[0].state, "held_out");
  assert.equal(rows(el)[0].until, until);
});

test("a model whose provider lacks a credential has that state and no return time", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ available: false, unavailable_until: null, unavailable_reason: "missing_credentials" })] }));
  assert.equal(rows(el)[0].state, "no_credentials");
  assert.equal(rows(el)[0].until, undefined);
});

test("an available model has no return time, even if the server sent one", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ available: true, unavailable_until: "2026-10-16T12:01:00Z" })] }));
  assert.equal(rows(el)[0].state, "available");
  assert.equal(rows(el)[0].until, undefined);
});

test("no models hands the table no rows", async () => {
  const { el } = await mount({}, tasksApi({ models: [] }));
  assert.deepEqual(rows(el), []);
});

test("it refreshes itself, since availability changes without anyone acting", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let up = true;
  const { el, calls } = await mount({}, (url) => jsonResponse(200, { items: [modelInfo({ available: up })] }));
  assert.equal(modelCalls(calls).length, 1);
  up = false;
  mock.timers.tick(10000);
  await flush();
  assert.equal(modelCalls(calls).length, 2);
  assert.equal(rows(el)[0].state, "held_out");
});

test("refresh-ms 0 turns refreshing off, and removing the element stops it", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const off = await mount({ "refresh-ms": 0 });
  mock.timers.tick(60000);
  await flush();
  assert.equal(modelCalls(off.calls).length, 1);

  const gone = await mount({ "refresh-ms": 1000 });
  gone.el.remove();
  mock.timers.tick(5000);
  await flush();
  assert.equal(modelCalls(gone.calls).length, 1);
});

test("an error shows the server's message and a later success clears it", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let fail = true;
  const events = [];
  reset();
  mockFetch((url) => (fail ? jsonResponse(500, { error: { message: "boom" } }) : jsonResponse(200, { items: [modelInfo()] })));
  const el = document.createElement("as-model-list");
  el.addEventListener("as-load", (e) => events.push(e.detail));
  document.body.appendChild(el);
  await flush();
  assert.equal(el.querySelector(".as-model-list-message").textContent, "Could not load models: boom");
  assert.equal(events[0].ok, false);
  assert.equal(events[0].live, true);
  fail = false;
  mock.timers.tick(10000);
  await flush();
  assert.equal(el.querySelector(".as-model-list-message").classList.contains("error"), false);
  assert.equal(rows(el).length, 1);
  assert.equal(events[1].ok, true);
});

test("model, provider and tag names are handed over as plain strings; the table draws them as text", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ id: "<img src=x onerror=alert(1)>", tags: ["<b>x</b>"] })] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
  assert.deepEqual([rows(el)[0].model, rows(el)[0].tags], ["<img src=x onerror=alert(1)>", ["<b>x</b>"]]);
});
