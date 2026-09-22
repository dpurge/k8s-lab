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

test("clicking a card dispatches kb-card-select with its data-id", () => {
  const container = document.createElement("div");
  container.innerHTML = `
    <kb-card data-id="item-1"><b>Item One</b></kb-card>
    <kb-card data-id="item-2"><b>Item Two</b></kb-card>
  `;
  document.body.appendChild(container);

  const seen = [];
  container.addEventListener("kb-card-select", (event) => seen.push(event.detail.id));

  container.querySelectorAll("kb-card")[0].click();
  container.querySelectorAll("kb-card")[1].click();

  assert.deepEqual(seen, ["item-1", "item-2"]);
});

test("a single delegated listener on the container catches every card, including ones added later", () => {
  const container = document.createElement("div");
  document.body.appendChild(container);

  let selectedId = null;
  container.addEventListener("kb-card-select", (event) => {
    selectedId = event.detail.id;
  });

  container.innerHTML = `<kb-card data-id="later"></kb-card>`;
  container.querySelector("kb-card").click();

  assert.equal(selectedId, "later");
});
