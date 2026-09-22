import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

// Stub fetch to serve the real component.html from disk, the same file the
// browser would be served — so the test exercises real template markup,
// not a copy re-typed into the test.
const here = path.dirname(fileURLToPath(import.meta.url));
const templateHTML = readFileSync(path.join(here, "component.html"), "utf8");
global.fetch = async (url) => {
  if (url === "/components/kb-nav/component.html") {
    return { text: async () => templateHTML };
  }
  throw new Error("unexpected fetch: " + url);
};

await import("./component.js");

function settle() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test("renders every item with exactly one active, and dispatches kb-nav-select on click", async () => {
  const el = document.createElement("kb-nav");
  el.items = [
    { id: "knowledge", label: "Knowledge" },
    { id: "chat", label: "Chat" },
    { id: "ingest", label: "Ingest" },
  ];
  el.active = "knowledge";
  document.body.appendChild(el);
  await settle();

  const items = el.querySelectorAll(".kb-nav-item");
  assert.equal(items.length, 3);
  assert.equal(items[0].textContent, "Knowledge");

  const activeItems = el.querySelectorAll(".kb-nav-item.active");
  assert.equal(activeItems.length, 1);
  assert.equal(activeItems[0].dataset.id, "knowledge");

  let detail = null;
  el.addEventListener("kb-nav-select", (event) => {
    detail = event.detail;
  });
  el.querySelector('[data-id="chat"]').click();

  assert.deepEqual(detail, { id: "chat" });
  // kb-nav updates its own active state on click, independent of whether
  // anything is listening for the event — see component.js's rationale.
  assert.equal(el.active, "chat");
  assert.equal(el.querySelector('[data-id="chat"]').classList.contains("active"), true);
  assert.equal(el.querySelector('[data-id="knowledge"]').classList.contains("active"), false);
});

test("does not know about showTab or any host-page function", async () => {
  const el = document.createElement("kb-nav");
  el.items = [{ id: "solo", label: "Solo" }];
  document.body.appendChild(el);
  await settle();

  // No listener attached at all — clicking must not throw, proving kb-nav
  // never calls a host function directly, only dispatches an event.
  assert.doesNotThrow(() => el.querySelector(".kb-nav-item").click());
});
