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
  const el = document.createElement("pf-field");
  el.setAttribute("label", "Language");
  el.setAttribute("for", "language");
  const select = document.createElement("select");
  select.id = "language";
  select.name = "language";
  select.required = true;
  el.appendChild(select);
  document.body.appendChild(el);

  assert.equal(el.children.length, 2);
  assert.equal(el.children[0].tagName, "LABEL");
  assert.equal(el.children[0].textContent, "Language");
  assert.equal(el.children[0].getAttribute("for"), "language");
  assert.equal(el.children[1], select, "the original select is untouched, not moved");
});

test("never touches the input child's own attributes (id, name, required, value)", () => {
  const el = document.createElement("pf-field");
  el.setAttribute("label", "Phrase");
  el.setAttribute("for", "field-source");
  const input = document.createElement("input");
  input.id = "field-source";
  input.name = "phrase";
  input.required = true;
  input.value = "hello";
  el.appendChild(input);
  document.body.appendChild(el);

  assert.equal(input.id, "field-source");
  assert.equal(input.name, "phrase");
  assert.equal(input.required, true);
  assert.equal(input.value, "hello");
});

// Regression coverage for the exact class of bug found and fixed in
// pf-button/kb-button: connectedCallback fires on the OPENING tag during
// real HTML parsing, before the browser has appended what's between the
// tags. Unlike pf-button, pf-field never depends on that content already
// being there — it only prepends a new node — so this must still pass
// without a MutationObserver-deferred wrap or any deferral.
test("real HTML parse order: label still ends up first even though the input is parsed after connectedCallback fires", async () => {
  const source = readFileSync(new URL("./component.js", import.meta.url), "utf8");
  const html = `<!doctype html><html><head><script>${source}</script></head><body><pf-field label="Title" for="title"><input id="title" /></pf-field></body></html>`;
  const parsedDom = new JSDOM(html, { url: "http://localhost/", runScripts: "dangerously" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  const el = parsedDom.window.document.querySelector("pf-field");
  assert.equal(el.children.length, 2);
  assert.equal(el.children[0].tagName, "LABEL");
  assert.equal(el.children[0].getAttribute("for"), "title");
  assert.equal(el.children[1].tagName, "INPUT");
  assert.equal(el.children[1].id, "title");
});

test("can carry its own id (needed for #transcription-group-style visibility toggling)", () => {
  const el = document.createElement("pf-field");
  el.id = "transcription-group";
  el.setAttribute("label", "Transcription");
  el.setAttribute("for", "field-transcription");
  const input = document.createElement("input");
  input.id = "field-transcription";
  el.appendChild(input);
  document.body.appendChild(el);

  const found = document.getElementById("transcription-group");
  assert.equal(found, el, "getElementById must find pf-field itself, not a wrapper");
  found.style.display = "none";
  assert.equal(el.style.display, "none");
});
