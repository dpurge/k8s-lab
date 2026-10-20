import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("./component.js");

function mount(options, value) {
  const el = document.createElement("pf-tag-picker");
  el.options = options;
  el.value = value;
  document.body.appendChild(el);
  return el;
}

function boxes(el) {
  return [...el.querySelectorAll('input[type="checkbox"]')];
}

test("renders one checkbox per option, ticking the value", () => {
  const el = mount(["big", "default", "vision"], ["default"]);
  assert.deepEqual(boxes(el).map((box) => box.value), ["big", "default", "vision"]);
  assert.deepEqual(boxes(el).map((box) => box.checked), [false, true, false]);
  assert.equal(el.getAttribute("role"), "group");
  assert.deepEqual(el.value, ["default"]);
});

test("no value means nothing is ticked", () => {
  const el = mount(["a", "b"], []);
  assert.deepEqual(el.value, []);
  assert.ok(boxes(el).every((box) => !box.checked));
});

test("ticking and unticking updates value and dispatches pf-tag-picker-change", () => {
  const el = mount(["a", "b", "c"], []);
  const seen = [];
  el.addEventListener("pf-tag-picker-change", (event) => seen.push(event.detail.value));
  boxes(el)[2].click();
  boxes(el)[0].click();
  assert.deepEqual(el.value, ["a", "c"], "value follows the display order, not the click order");
  boxes(el)[2].click();
  assert.deepEqual(el.value, ["a"]);
  assert.deepEqual(seen, [["c"], ["a", "c"], ["a"]]);
});

test("a ticked tag that is not among the options stays shown and ticked", () => {
  const el = mount(["a"], ["gone"]);
  assert.deepEqual(boxes(el).map((box) => [box.value, box.checked]), [["a", false], ["gone", true]]);
  assert.deepEqual(el.value, ["gone"]);
});

test("setting new options keeps the ticks of the tags still offered", () => {
  const el = mount(["a", "b"], ["b"]);
  el.options = ["b", "c"];
  assert.deepEqual(el.value, ["b"]);
  assert.deepEqual(boxes(el).map((box) => box.value), ["b", "c"]);
});

test("a tag is rendered as text, never as markup", () => {
  const el = mount(['<img src=x onerror=alert(1)>'], []);
  assert.equal(el.querySelector("img"), null);
  assert.ok(el.textContent.includes("<img"));
});

test("injects its stylesheet once", () => {
  mount(["a"], []);
  mount(["b"], []);
  assert.equal(document.querySelectorAll("#pf-tag-picker-style").length, 1);
});
