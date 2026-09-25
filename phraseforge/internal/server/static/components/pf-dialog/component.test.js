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

// pf-dialog builds its OK/Cancel buttons out of <pf-button>, so that
// component must be registered first.
await import("../pf-button/component.js");
await import("./component.js");

function settle() {
  return new Promise((resolve) => queueMicrotask(resolve));
}

test("confirm() resolves true when OK is clicked", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const promise = dialog.confirm("Delete this item?");
  assert.equal(dialog.classList.contains("open"), true);
  assert.equal(dialog.querySelector(".pf-dialog-message").textContent, "Delete this item?");

  const okButton = dialog.querySelectorAll("pf-button")[1].querySelector("button");
  okButton.click();

  assert.equal(await promise, true);
  assert.equal(dialog.classList.contains("open"), false);
});

test("confirm() resolves false when Cancel is clicked", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const promise = dialog.confirm("Discard this draft?");
  const cancelButton = dialog.querySelectorAll("pf-button")[0].querySelector("button");
  cancelButton.click();

  assert.equal(await promise, false);
});

test("confirm() resolves false on Escape and on clicking the overlay", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  let promise = dialog.confirm("Approve this draft?");
  dialog.dispatchEvent(new dialog.ownerDocument.defaultView.KeyboardEvent("keydown", { key: "Escape" }));
  assert.equal(await promise, false);

  promise = dialog.confirm("Approve this draft?");
  dialog.dispatchEvent(new dialog.ownerDocument.defaultView.Event("click", { bubbles: true }));
  assert.equal(await promise, false);
});

test("showContent() renders the given node, hides the message, and widens the box", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const node = document.createElement("div");
  node.textContent = "structured content";
  const promise = dialog.showContent(node, { okLabel: "Close", wide: true });

  assert.equal(dialog.classList.contains("open"), true);
  assert.equal(dialog.querySelector(".pf-dialog-message").style.display, "none");
  assert.equal(dialog.querySelector(".pf-dialog-content").textContent, "structured content");
  assert.equal(dialog.querySelector(".pf-dialog-box").classList.contains("wide"), true);
  assert.equal(dialog.querySelectorAll("pf-button")[1].querySelector("button").textContent, "Close");

  dialog.querySelectorAll("pf-button")[1].querySelector("button").click();
  assert.equal(await promise, true);
});

test("confirm() after showContent() clears the stale content, message, and wide class", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const node = document.createElement("div");
  node.textContent = "structured content";
  let promise = dialog.showContent(node, { wide: true });
  dialog.querySelectorAll("pf-button")[0].querySelector("button").click();
  await promise;

  promise = dialog.confirm("Plain message");
  assert.equal(dialog.querySelector(".pf-dialog-content").childNodes.length, 0);
  assert.equal(dialog.querySelector(".pf-dialog-message").style.display, "");
  assert.equal(dialog.querySelector(".pf-dialog-box").classList.contains("wide"), false);
  dialog.querySelectorAll("pf-button")[0].querySelector("button").click();
  await promise;
});

test("showContent() with hideCancel hides Cancel; confirm() always shows it again", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const cancelButton = dialog.querySelectorAll("pf-button")[0];
  const node = document.createElement("div");
  let promise = dialog.showContent(node, { hideCancel: true });
  assert.equal(cancelButton.style.display, "none");
  dialog.querySelectorAll("pf-button")[1].querySelector("button").click();
  await promise;

  promise = dialog.confirm("Plain message");
  assert.equal(cancelButton.style.display, "");
  dialog.querySelectorAll("pf-button")[0].querySelector("button").click();
  await promise;
});

test("a manual resize (inline width/height) does not leak into the next open()", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const box = dialog.querySelector(".pf-dialog-box");
  let promise = dialog.showContent(document.createElement("div"), { wide: true });
  box.style.width = "900px";
  box.style.height = "700px";
  dialog.querySelectorAll("pf-button")[1].querySelector("button").click();
  await promise;

  promise = dialog.showContent(document.createElement("div"), { wide: true });
  assert.equal(box.style.width, "");
  assert.equal(box.style.height, "");
  dialog.querySelectorAll("pf-button")[1].querySelector("button").click();
  await promise;
});

test("confirm() sets the OK button's variant to danger only when requested", async () => {
  const dialog = document.createElement("pf-dialog");
  document.body.appendChild(dialog);
  await settle();

  const okButton = dialog.querySelectorAll("pf-button")[1];

  let promise = dialog.confirm("Delete this item?", { danger: true });
  assert.equal(okButton.getAttribute("variant"), "danger");
  okButton.querySelector("button").click();
  await promise;

  promise = dialog.confirm("Import configuration?");
  assert.equal(okButton.getAttribute("variant"), "primary");
  okButton.querySelector("button").click();
  await promise;
});
