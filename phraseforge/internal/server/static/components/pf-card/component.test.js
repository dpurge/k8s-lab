import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;

await import("./component.js");

test("registers the pf-card custom element", () => {
  assert.ok(customElements.get("pf-card"), "expected pf-card to be defined");
});

test("leaves its children completely unchanged (no wrapping, no attribute changes, no click interception)", async () => {
  const el = document.createElement("pf-card");
  el.innerHTML =
    '<h2><a href="/texts/1">Sample Title</a></h2><div class="meta"><span class="badge">fra / latn</span></div>';
  document.body.appendChild(el);

  const link = el.querySelector("h2 a");
  assert.equal(link.getAttribute("href"), "/texts/1", "the real navigation link must be untouched");
  assert.equal(link.textContent, "Sample Title");
  assert.equal(el.querySelector(".meta .badge").textContent, "fra / latn");

  let selectFired = false;
  el.addEventListener("pf-card-select", () => {
    selectFired = true;
  });
  el.click();
  assert.equal(selectFired, false, "pf-card must never dispatch a select event — it is not a kb-card port");
});

test("injects its stylesheet into <head> exactly once, even for multiple instances", async () => {
  const first = document.createElement("pf-card");
  document.body.appendChild(first);
  const second = document.createElement("pf-card");
  document.body.appendChild(second);

  const links = document.head.querySelectorAll("#pf-card-style");
  assert.equal(links.length, 1, "the stylesheet link must be injected only once regardless of instance count");
  assert.equal(links[0].getAttribute("href"), "/static/components/pf-card/component.css");
});
