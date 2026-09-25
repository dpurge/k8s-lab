import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;

await import("./component.js");

test("setJobStatus and setMessage set their own span's text, independently", () => {
  const el = document.createElement("pf-status-bar");
  document.body.appendChild(el);

  el.setJobStatus("Saving...");
  el.setMessage("Saved", false);

  assert.equal(el.querySelector(".pf-status-job").textContent, "Saving...");
  assert.equal(el.querySelector(".pf-status-message").textContent, "Saved");
  assert.equal(el.querySelector(".pf-status-message").classList.contains("error"), false);
});

test("setMessage toggles the error class based on isError", () => {
  const el = document.createElement("pf-status-bar");
  document.body.appendChild(el);

  el.setMessage("Something went wrong", true);
  assert.equal(el.querySelector(".pf-status-message").classList.contains("error"), true);

  el.setMessage("All good", false);
  assert.equal(el.querySelector(".pf-status-message").classList.contains("error"), false);
});

test("injects its stylesheet into <head> exactly once, even for multiple instances", () => {
  const first = document.createElement("pf-status-bar");
  document.body.appendChild(first);
  const second = document.createElement("pf-status-bar");
  document.body.appendChild(second);

  const links = document.head.querySelectorAll("#pf-status-bar-style");
  assert.equal(links.length, 1);
  assert.equal(links[0].getAttribute("href"), "/static/components/pf-status-bar/component.css");
});
