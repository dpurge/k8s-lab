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

const rows = (el) => [...el.querySelectorAll("tbody tr")];
const cells = (tr) => [...tr.children].map((td) => td.textContent);
const modelCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/models"));

test("one row per model with its provider, tags, priority and availability", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo(), modelInfo({ id: "small", provider: "laptop", tags: ["chat"], priority: 10 })] }));
  assert.deepEqual(rows(el).map(cells), [
    ["big", "gpu", "chat, strong", "20", "available"],
    ["small", "laptop", "chat", "10", "available"],
  ]);
  assert.deepEqual([...el.querySelectorAll("thead th")].map((th) => th.textContent), ["Model", "Provider", "Tags", "Priority", "Available"]);
  assert.equal(rows(el)[0].dataset.modelId, "big");
});

test("models are listed in the order they are tried: priority, then file order", async () => {
  const { el } = await mount({}, tasksApi({
    models: [modelInfo({ id: "low", priority: 1 }), modelInfo({ id: "first-tie", priority: 5 }), modelInfo({ id: "high", priority: 9 }), modelInfo({ id: "second-tie", priority: 5 })],
  }));
  assert.deepEqual(rows(el).map((tr) => cells(tr)[0]), ["high", "first-tie", "second-tie", "low"]);
});

test("a model the breaker holds out says so and when it returns", async () => {
  const until = "2026-10-16T12:01:00Z";
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ available: false, unavailable_until: until })] }));
  const td = el.querySelector("td.as-model-unavailable");
  assert.match(td.textContent, /^held out until .*2026/);
  assert.equal(td.querySelector(".as-model-until").title, until);
  assert.equal(td.querySelector(".as-badge").className, "as-badge as-model-down");
  assert.equal(el.querySelector("td.as-model-available"), null);
});

test("an available model shows no return time", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-model-until"), null);
  assert.equal(el.querySelector(".as-badge").className, "as-badge as-model-up");
});

test("no models says so and shows no table", async () => {
  const { el } = await mount({}, tasksApi({ models: [] }));
  assert.equal(el.querySelector(".as-model-list-table-wrap").hidden, true);
  assert.equal(el.querySelector(".as-model-list-message").textContent, "No models are configured.");
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
  assert.equal(el.querySelector(".as-badge").className, "as-badge as-model-down");
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

test("model, provider and tag names reach the page as text, never as markup", async () => {
  const { el } = await mount({}, tasksApi({ models: [modelInfo({ id: "<img src=x onerror=alert(1)>", tags: ["<b>x</b>"] })] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
});
