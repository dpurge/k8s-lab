import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("./component.js");

const labels = {
  heading: "Models", refresh: "Refresh", colModel: "Model", colProvider: "Provider", colTags: "Tags",
  colPriority: "Priority", colAvailability: "Availability", available: "Available", unavailable: "Unavailable", none: "No models",
  reasons: { cooldown: "Cooling down", missing_credentials: "Credentials missing" },
};

function mount(models) {
  const el = document.createElement("pf-llm-models");
  el.labels = labels;
  el.locale = "en";
  el.models = models;
  document.body.appendChild(el);
  return el;
}

const models = [
  { id: "fast", provider_id: "ollama", tags: ["default", "big"], priority: 10, available: true },
  { id: "slow", provider_id: "vertex", tags: [], priority: 1200, available: false },
];

test("renders one row per model with provider, tags, priority and availability", () => {
  const el = mount(models);
  const rows = [...el.querySelectorAll("tbody tr")];
  assert.equal(rows.length, 2);
  const cells = (row) => [...row.children].map((c) => c.textContent);
  assert.deepEqual(cells(rows[0]), ["fast", "ollama", "defaultbig", "10", "Available"]);
  assert.deepEqual([...rows[0].querySelectorAll(".badge")].map((b) => b.textContent), ["default", "big", "Available"]);
  assert.deepEqual(cells(rows[1]), ["slow", "vertex", "", "1,200", "Unavailable"]);
  assert.equal(rows[0].querySelector(".pf-llm-availability").dataset.available, "true");
  assert.equal(rows[1].querySelector(".pf-llm-availability").dataset.available, "false");
});

test("an unavailable model shows a small reason badge with the reason as its title", () => {
  const el = mount([
    { id: "a", provider_id: "p", tags: [], priority: 1, available: false, unavailable_reason: "missing_credentials" },
    { id: "b", provider_id: "p", tags: [], priority: 1, available: false, unavailable_reason: "cooldown" },
    { id: "c", provider_id: "p", tags: [], priority: 1, available: false, unavailable_reason: "something_new" },
    { id: "d", provider_id: "p", tags: [], priority: 1, available: true },
  ]);
  const reasons = [...el.querySelectorAll("tbody tr")].map((row) => row.querySelector(".pf-llm-reason"));
  assert.deepEqual(reasons.slice(0, 3).map((r) => r.textContent), ["Credentials missing", "Cooling down", "something_new"]);
  assert.equal(reasons[0].title, "Credentials missing");
  assert.equal(reasons[0].dataset.reason, "missing_credentials");
  assert.equal(reasons[3], null);
});

test("the table sits in a horizontal scroll container", () => {
  const el = mount(models);
  assert.ok(el.querySelector(".pf-llm-scroll > table"));
});

test("no models shows the empty text across all columns", () => {
  const el = mount([]);
  const cell = el.querySelector("tbody td");
  assert.equal(cell.textContent, "No models");
  assert.equal(cell.colSpan, 5);
});

test("Refresh dispatches pf-llm-models-refresh", () => {
  const el = mount(models);
  let count = 0;
  el.addEventListener("pf-llm-models-refresh", () => count++);
  el.querySelector(".pf-llm-refresh").click();
  assert.equal(count, 1);
});

test("a model id or tag is rendered as text, never as markup", () => {
  const el = mount([{ id: "<img src=x onerror=alert(1)>", provider_id: "p", tags: ["<b>t</b>"], priority: 1, available: true }]);
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("b"), null);
  assert.ok(el.textContent.includes("<img"));
});

test("setting new models replaces the rows", () => {
  const el = mount(models);
  el.models = [models[1]];
  assert.deepEqual([...el.querySelectorAll("tbody tr")].map((r) => r.dataset.modelId), ["slow"]);
});

test("injects its stylesheet once", () => {
  mount(models);
  mount(models);
  assert.equal(document.querySelectorAll("#pf-llm-models-style").length, 1);
});
