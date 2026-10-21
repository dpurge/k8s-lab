// Shared setup for the component tests: a jsdom window installed as the
// globals Lit and the components expect. Each test file runs in its own
// process, so this state is never shared between files.
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", {
  url: "http://localhost/",
  pretendToBeVisual: true,
});

for (const name of [
  "window",
  "document",
  "customElements",
  "HTMLElement",
  "Document",
  "ShadowRoot",
  "CSSStyleSheet",
  "Node",
  "Event",
  "CustomEvent",
  "KeyboardEvent",
  "Element",
  "HTMLLabelElement",
  "CompositionEvent",
  "InputEvent",
  "location",
  "localStorage",
  "DOMParser",
  "MutationObserver",
] as const) {
  Object.defineProperty(globalThis, name, { value: dom.window[name], configurable: true, writable: true });
}

// jsdom has <dialog> but neither showModal() nor close(). These stand in for
// them, so the tests check the component's own logic; the browser's behaviour
// (Escape, the focus trap, the top layer) is checked in a real browser.
const dialogPrototype = dom.window.HTMLDialogElement.prototype as unknown as {
  showModal?: (this: HTMLDialogElement) => void;
  close?: (this: HTMLDialogElement) => void;
};
dialogPrototype.showModal = function () {
  this.setAttribute("open", "");
};
dialogPrototype.close = function () {
  if (!this.hasAttribute("open")) return;
  this.removeAttribute("open");
  this.dispatchEvent(new dom.window.Event("close"));
};

// mount adds an element to a clean body and waits for Lit's first render.
export async function mount<T extends HTMLElement & { updateComplete: Promise<unknown> }>(html: string): Promise<T> {
  document.body.innerHTML = html;
  const el = document.body.firstElementChild as T;
  await el.updateComplete;
  return el;
}
