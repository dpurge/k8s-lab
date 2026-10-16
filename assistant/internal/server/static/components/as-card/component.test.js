import { test } from "node:test";
import assert from "node:assert/strict";
import { load, reset } from "../testenv.js";

await load("components/as-card/component.js");

function mount(heading, html = "<p>content</p>") {
  reset();
  const el = document.createElement("as-card");
  if (heading !== null) el.setAttribute("heading", heading);
  el.innerHTML = html;
  document.body.appendChild(el);
  return el;
}

test("shows the heading above the content", () => {
  const el = mount("Tasks");
  assert.equal(el.firstElementChild.className, "as-card-heading");
  assert.equal(el.firstElementChild.textContent, "Tasks");
  assert.equal(el.querySelector("p").textContent, "content");
});

test("a card without a heading adds nothing", () => {
  const el = mount(null);
  assert.equal(el.querySelector(".as-card-heading"), null);
  assert.equal(el.children.length, 1);
});

test("changing, adding and removing the heading", () => {
  const el = mount("One");
  el.setAttribute("heading", "Two");
  assert.equal(el.querySelectorAll(".as-card-heading").length, 1);
  assert.equal(el.querySelector(".as-card-heading").textContent, "Two");
  el.removeAttribute("heading");
  assert.equal(el.querySelector(".as-card-heading"), null);
  el.setAttribute("heading", "Three");
  assert.equal(el.firstElementChild.textContent, "Three");
});

test("the content is left alone", () => {
  const el = mount("Tasks", "<span id=a>x</span><span id=b>y</span>");
  const a = el.querySelector("#a");
  el.setAttribute("heading", "Other");
  assert.equal(el.querySelector("#a"), a, "the same node, not a copy");
  assert.deepEqual([...el.children].map((c) => c.id), ["", "a", "b"]);
});

test("the heading is text, never markup", () => {
  const el = mount("<img src=x onerror=alert(1)>");
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector(".as-card-heading").textContent, "<img src=x onerror=alert(1)>");
});

test("injects its stylesheet once", () => {
  reset();
  mount("A");
  mount("B");
  assert.equal(document.querySelectorAll("#as-card-style").length, 1);
});
