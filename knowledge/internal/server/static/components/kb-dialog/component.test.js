import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;
global.KeyboardEvent = dom.window.KeyboardEvent;
global.Event = dom.window.Event;

// kb-dialog builds its OK/Cancel buttons out of <kb-button>, so that
// component must be registered first.
await import("../kb-button/component.js");
await import("./component.js");

function settle() {
  return new Promise((resolve) => queueMicrotask(resolve));
}

test("confirm() resolves true when OK is clicked", async () => {
  const dialog = document.createElement("kb-dialog");
  document.body.appendChild(dialog);
  await settle();

  const promise = dialog.confirm("Delete this item?");
  assert.equal(dialog.classList.contains("open"), true);
  assert.equal(dialog.querySelector(".kb-dialog-message").textContent, "Delete this item?");

  const okButton = dialog.querySelectorAll("kb-button")[1].querySelector("button");
  okButton.click();

  assert.equal(await promise, true);
  assert.equal(dialog.classList.contains("open"), false);
});

test("confirm() resolves false when Cancel is clicked", async () => {
  const dialog = document.createElement("kb-dialog");
  document.body.appendChild(dialog);
  await settle();

  const promise = dialog.confirm("Discard this draft?");
  const cancelButton = dialog.querySelectorAll("kb-button")[0].querySelector("button");
  cancelButton.click();

  assert.equal(await promise, false);
});

test("confirm() resolves false on Escape and on clicking the overlay", async () => {
  const dialog = document.createElement("kb-dialog");
  document.body.appendChild(dialog);
  await settle();

  let promise = dialog.confirm("Approve this draft?");
  dialog.dispatchEvent(new dialog.ownerDocument.defaultView.KeyboardEvent("keydown", { key: "Escape" }));
  assert.equal(await promise, false);

  promise = dialog.confirm("Approve this draft?");
  dialog.dispatchEvent(new dialog.ownerDocument.defaultView.Event("click", { bubbles: true }));
  assert.equal(await promise, false);
});
