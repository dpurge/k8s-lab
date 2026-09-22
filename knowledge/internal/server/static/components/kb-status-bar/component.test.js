import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;

await import("./component.js");

test("setJobStatus and setMessage update their own text independently", () => {
  const el = document.createElement("kb-status-bar");
  document.body.appendChild(el);

  el.setJobStatus("ingest: running");
  el.setMessage("Saved.", false);

  assert.equal(el.querySelector(".kb-status-job").textContent, "ingest: running");
  assert.equal(el.querySelector(".kb-status-message").textContent, "Saved.");
  assert.equal(el.querySelector(".kb-status-message").classList.contains("error"), false);

  el.setJobStatus("");
  assert.equal(el.querySelector(".kb-status-job").textContent, "");
  assert.equal(el.querySelector(".kb-status-message").textContent, "Saved.", "message unaffected by clearing job status");
});

test("setMessage with isError=true adds the error class", () => {
  const el = document.createElement("kb-status-bar");
  document.body.appendChild(el);

  el.setMessage("Something failed", true);
  assert.equal(el.querySelector(".kb-status-message").classList.contains("error"), true);

  el.setMessage("Recovered", false);
  assert.equal(el.querySelector(".kb-status-message").classList.contains("error"), false);
});
