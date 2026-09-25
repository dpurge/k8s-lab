import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("./component.js");

test("renders every item with exactly one active, and dispatches pf-tabs-select on click", () => {
  const el = document.createElement("pf-tabs");
  el.items = [
    { id: "grants", label: "Grants" },
    { id: "ime", label: "IME" },
    { id: "llm", label: "LLM Prompts" },
  ];
  el.active = "grants";
  document.body.appendChild(el);

  const items = el.querySelectorAll(".pf-tabs-item");
  assert.equal(items.length, 3);
  assert.equal(items[0].textContent, "Grants");

  const activeItems = el.querySelectorAll(".pf-tabs-item.active");
  assert.equal(activeItems.length, 1);
  assert.equal(activeItems[0].dataset.id, "grants");

  let detail = null;
  el.addEventListener("pf-tabs-select", (event) => {
    detail = event.detail;
  });
  el.querySelector('[data-id="ime"]').click();

  assert.deepEqual(detail, { id: "ime" });
  // pf-tabs updates its own active state on click, independent of whether
  // anything is listening for the event.
  assert.equal(el.active, "ime");
  assert.equal(el.querySelector('[data-id="ime"]').classList.contains("active"), true);
  assert.equal(el.querySelector('[data-id="grants"]').classList.contains("active"), false);
});

test("does not know about any host-page switching function", () => {
  const el = document.createElement("pf-tabs");
  el.items = [{ id: "solo", label: "Solo" }];
  document.body.appendChild(el);

  // No listener attached at all — clicking must not throw, proving pf-tabs
  // never calls a host function directly, only dispatches an event.
  assert.doesNotThrow(() => el.querySelector(".pf-tabs-item").click());
});

test("re-rendering items preserves the active attribute across re-render", () => {
  const el = document.createElement("pf-tabs");
  document.body.appendChild(el);
  el.items = [
    { id: "a", label: "A" },
    { id: "b", label: "B" },
  ];
  el.active = "b";
  assert.equal(el.querySelector('[data-id="b"]').classList.contains("active"), true);

  // Setting items again (e.g. to update a pill count in a label) must not
  // lose which tab is active.
  el.items = [
    { id: "a", label: "A" },
    { id: "b", label: "B (2)" },
  ];
  assert.equal(el.querySelector('[data-id="b"]').classList.contains("active"), true);
  assert.equal(el.querySelector('[data-id="b"]').textContent, "B (2)");
});

test("orientation=vertical is a plain passthrough attribute — no JS behavior change", () => {
  const el = document.createElement("pf-tabs");
  el.setAttribute("orientation", "vertical");
  el.items = [{ id: "solo", label: "Solo" }];
  el.active = "solo";
  document.body.appendChild(el);

  assert.equal(el.getAttribute("orientation"), "vertical");
  assert.equal(el.querySelector(".pf-tabs-item.active").dataset.id, "solo");
});

test("injects its stylesheet into <head> exactly once, even for multiple instances", () => {
  const first = document.createElement("pf-tabs");
  document.body.appendChild(first);
  const second = document.createElement("pf-tabs");
  document.body.appendChild(second);

  const links = document.head.querySelectorAll("#pf-tabs-style");
  assert.equal(links.length, 1);
  assert.equal(links[0].getAttribute("href"), "/static/components/pf-tabs/component.css");
});
