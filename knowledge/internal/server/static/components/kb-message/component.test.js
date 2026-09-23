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

await import("./component.js");

test("setting message renders the contentNode's real DOM, not a re-parsed string", () => {
  const el = document.createElement("kb-message");
  const fragment = document.createDocumentFragment();
  const codeBlock = document.createElement("pre");
  codeBlock.appendChild(document.createElement("code")).textContent = "const x = 1;";
  fragment.appendChild(codeBlock);
  // A literal "<b>not html</b>" text node — if the component ever fell back
  // to innerHTML on a stringified version of this content, this would turn
  // into a real <b> element. It must stay a text node.
  fragment.appendChild(document.createTextNode("<b>not html</b>"));

  el.message = { role: "assistant", contentNode: fragment };
  document.body.appendChild(el);

  const renderedCode = el.querySelector(".kb-message-content pre code");
  assert.ok(renderedCode, "expected the actual <pre><code> element from the fragment to be present");
  assert.equal(renderedCode.textContent, "const x = 1;");

  assert.equal(el.querySelector(".kb-message-content b"), null, "the literal text must not have been parsed into an element");
  assert.ok(el.querySelector(".kb-message-content").textContent.includes("<b>not html</b>"));
});

test("data-role is set from message.role, not the ARIA role attribute", () => {
  const el = document.createElement("kb-message");
  el.message = { role: "user", contentNode: document.createTextNode("hi") };
  document.body.appendChild(el);

  assert.equal(el.dataset.role, "user");
  assert.equal(el.getAttribute("role"), null);
});

test("renders a source list when sources are present", () => {
  const el = document.createElement("kb-message");
  el.message = {
    role: "assistant",
    contentNode: document.createTextNode("answer"),
    sources: [{ id: "item-1", title: "Item One", score: 0.9123, summary: "a summary" }],
  };
  document.body.appendChild(el);

  const links = el.querySelectorAll(".kb-message-sources a.kb-message-source");
  assert.equal(links.length, 1);
  assert.equal(links[0].textContent, "Item One");
});

test("renders no sources block when sources are absent or empty", () => {
  const noSources = document.createElement("kb-message");
  noSources.message = { role: "assistant", contentNode: document.createTextNode("answer") };
  document.body.appendChild(noSources);
  assert.equal(noSources.querySelector(".kb-message-sources"), null);

  const emptySources = document.createElement("kb-message");
  emptySources.message = { role: "assistant", contentNode: document.createTextNode("answer"), sources: [] };
  document.body.appendChild(emptySources);
  assert.equal(emptySources.querySelector(".kb-message-sources"), null);
});

test("clicking a source dispatches a bubbling kb-source-select with the source id", () => {
  const container = document.createElement("div");
  document.body.appendChild(container);

  const el = document.createElement("kb-message");
  container.appendChild(el);
  el.message = {
    role: "assistant",
    contentNode: document.createTextNode("answer"),
    sources: [{ id: "item-42", title: "Item Forty-Two", score: 0.5, summary: "s" }],
  };

  let detail = null;
  container.addEventListener("kb-source-select", (event) => {
    detail = event.detail;
  });

  el.querySelector(".kb-message-source").click();

  assert.deepEqual(detail, { id: "item-42" });
});

test("the component source never assigns a string to .innerHTML on the message content path", () => {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(path.join(here, "component.js"), "utf8");
  assert.equal(/\.innerHTML\s*=/.test(source), false, "kb-message must only appendChild/textContent, never innerHTML, to stay structurally safe from injection");
});
