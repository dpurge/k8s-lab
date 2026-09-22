import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.MutationObserver = dom.window.MutationObserver;

await import("./component.js");

test("prepends a label with the given text and for attribute, ahead of existing children", () => {
  const el = document.createElement("kb-field");
  el.setAttribute("label", "Title");
  el.setAttribute("for", "title");
  const input = document.createElement("input");
  input.id = "title";
  el.appendChild(input);
  document.body.appendChild(el);

  assert.equal(el.children.length, 2);
  assert.equal(el.children[0].tagName, "LABEL");
  assert.equal(el.children[0].textContent, "Title");
  assert.equal(el.children[0].getAttribute("for"), "title");
  assert.equal(el.children[1], input, "the original input is untouched, not moved");
});

// Regression coverage for the exact class of bug found and fixed in
// kb-button: connectedCallback fires on the OPENING tag during real HTML
// parsing, before the browser has appended what's between the tags. Unlike
// kb-button, kb-field never depends on that content already being there —
// it only prepends a new node — so this must still pass without a
// MutationObserver or any deferral.
test("real HTML parse order: label still ends up first even though the input is parsed after connectedCallback fires", async () => {
  const source = readFileSync(new URL("./component.js", import.meta.url), "utf8");
  const html = `<!doctype html><html><head><script>${source}</script></head><body><kb-field label="Title" for="title"><input id="title" /></kb-field></body></html>`;
  const parsedDom = new JSDOM(html, { url: "http://localhost/", runScripts: "dangerously" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  const el = parsedDom.window.document.querySelector("kb-field");
  assert.equal(el.children.length, 2);
  assert.equal(el.children[0].tagName, "LABEL");
  assert.equal(el.children[0].getAttribute("for"), "title");
  assert.equal(el.children[1].tagName, "INPUT");
  assert.equal(el.children[1].id, "title");
});
