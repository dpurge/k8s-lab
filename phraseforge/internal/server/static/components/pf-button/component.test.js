import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;

await import("./component.js");

function settle() {
  return new Promise((resolve) => queueMicrotask(resolve));
}

test("wraps its content in a real <button> and forwards disabled/type", async () => {
  const el = document.createElement("pf-button");
  el.textContent = "Save";
  el.setAttribute("disabled", "");
  el.setAttribute("type", "submit");
  document.body.appendChild(el);
  await settle();

  const button = el.querySelector("button");
  assert.ok(button, "expected an inner <button>");
  assert.equal(button.textContent, "Save");
  assert.equal(button.disabled, true);
  assert.equal(button.type, "submit");
});

test("a click on the inner button bubbles to the host element", async () => {
  const el = document.createElement("pf-button");
  el.textContent = "Click me";
  document.body.appendChild(el);
  await settle();

  let clicked = false;
  el.addEventListener("click", () => {
    clicked = true;
  });
  el.querySelector("button").click();

  assert.equal(clicked, true);
});

test("forwards aria-label to the inner button, initially and on change", async () => {
  const el = document.createElement("pf-button");
  el.textContent = "☰";
  el.setAttribute("aria-label", "Toggle sidebar");
  document.body.appendChild(el);
  await settle();

  const button = el.querySelector("button");
  assert.equal(button.getAttribute("aria-label"), "Toggle sidebar");

  el.setAttribute("aria-label", "Collapse sidebar");
  assert.equal(button.getAttribute("aria-label"), "Collapse sidebar");

  el.removeAttribute("aria-label");
  assert.equal(button.hasAttribute("aria-label"), false);
});

test("removing the disabled attribute re-enables the inner button", async () => {
  const el = document.createElement("pf-button");
  el.textContent = "Delete";
  el.setAttribute("disabled", "");
  document.body.appendChild(el);
  await settle();
  assert.equal(el.querySelector("button").disabled, true);

  el.removeAttribute("disabled");
  assert.equal(el.querySelector("button").disabled, false);
});

// Regression test for the real bug this component must not repeat:
// connectedCallback fires the instant the browser inserts <pf-button>'s
// OPENING tag during HTML parsing — before it has parsed/appended the text
// between the tags. phraseforge's sidebar-toggle button has exactly this
// shape (static "☰" written directly in layout.html), so this must be
// simulated by executing the real component source from an inline <head>
// <script>, not by defining it up front the way the tests above do.
test("content written directly in HTML (not set programmatically) still ends up inside the button", async () => {
  const source = readFileSync(new URL("./component.js", import.meta.url), "utf8");
  const html = `<!doctype html><html><head><script>${source}</script></head><body><pf-button variant="icon">☰</pf-button></body></html>`;
  const parsedDom = new JSDOM(html, { url: "http://localhost/", runScripts: "dangerously" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  const el = parsedDom.window.document.querySelector("pf-button");
  const button = el.querySelector("button");
  assert.ok(button, "expected an inner <button>");
  assert.equal(button.textContent, "☰");
  assert.equal(el.childNodes.length, 1, "the toggle glyph must not be left as a stray sibling of the button");
});
