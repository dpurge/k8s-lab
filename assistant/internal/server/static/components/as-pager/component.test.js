import { test } from "node:test";
import assert from "node:assert/strict";
import { load, reset } from "../testenv.js";

await load("components/as-pager/component.js");
const AsPager = customElements.get("as-pager");

function mount(page, pageCount) {
  reset();
  const el = document.createElement("as-pager");
  el.setAttribute("page", String(page));
  el.setAttribute("page-count", String(pageCount));
  document.body.appendChild(el);
  return el;
}
const labels = (el) => [...el.querySelectorAll(".as-pager-item, .as-pager-gap")].map((n) => n.textContent);

test("renders nothing for a single page", () => {
  assert.equal(mount(1, 1).children.length, 0);
  assert.equal(mount(1, 0).children.length, 0);
});

test("two pages show only Previous and Next", () => {
  const el = mount(1, 2);
  assert.deepEqual(labels(el), ["Previous", "Next"]);
  assert.equal(el.querySelector(".as-pager-prev").disabled, true);
  assert.equal(el.querySelector(".as-pager-next").disabled, false);
});

test("three pages show numbers with the current one marked", () => {
  const el = mount(2, 3);
  assert.deepEqual(labels(el), ["Previous", "1", "2", "3", "Next"]);
  const active = el.querySelectorAll(".as-pager-item.active");
  assert.equal(active.length, 1);
  assert.equal(active[0].textContent, "2");
  assert.equal(active[0].getAttribute("aria-current"), "page");
});

test("Previous is disabled on the first page and Next on the last", () => {
  assert.equal(mount(1, 5).querySelector(".as-pager-prev").disabled, true);
  assert.equal(mount(5, 5).querySelector(".as-pager-next").disabled, true);
  assert.equal(mount(3, 5).querySelector(".as-pager-prev").disabled, false);
});

test("many pages show at most ten numbers with an ellipsis on the hidden sides", () => {
  assert.deepEqual(labels(mount(1, 30)), ["Previous", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "…", "Next"]);
  assert.deepEqual(labels(mount(15, 30)), ["Previous", "…", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "…", "Next"]);
  assert.deepEqual(labels(mount(30, 30)), ["Previous", "…", "21", "22", "23", "24", "25", "26", "27", "28", "29", "30", "Next"]);
});

test("pageItems stays within bounds for every page", () => {
  for (let count = 3; count <= 25; count++) {
    for (let page = 1; page <= count; page++) {
      const numbers = AsPager.pageItems(page, count).filter((n) => n !== null);
      assert.ok(numbers.length <= 10, `${page}/${count}`);
      assert.ok(numbers.includes(page), `${page}/${count} shows the current page`);
      assert.ok(numbers[0] >= 1 && numbers.at(-1) <= count, `${page}/${count}`);
    }
  }
});

test("clicking emits as-pager-select with the page, bubbling", () => {
  const el = mount(3, 5);
  const seen = [];
  document.body.addEventListener("as-pager-select", (e) => seen.push(e.detail.page));
  el.querySelector(".as-pager-next").click();
  el.querySelector(".as-pager-prev").click();
  [...el.querySelectorAll(".as-pager-item")].find((b) => b.textContent === "5").click();
  assert.deepEqual(seen, [4, 2, 5]);
});

test("a disabled button emits nothing", () => {
  const el = mount(1, 3);
  let n = 0;
  el.addEventListener("as-pager-select", () => n++);
  el.querySelector(".as-pager-prev").click();
  assert.equal(n, 0);
});

test("it never changes its own page", () => {
  const el = mount(1, 3);
  el.querySelector(".as-pager-next").click();
  assert.equal(el.getAttribute("page"), "1");
});

test("changing the attributes re-renders", () => {
  const el = mount(1, 5);
  el.setAttribute("page", "4");
  assert.equal(el.querySelector(".as-pager-item.active").textContent, "4");
  el.setAttribute("page-count", "1");
  assert.equal(el.children.length, 0);
});

test("labels can be translated", () => {
  const el = document.createElement("as-pager");
  el.setAttribute("page", "1");
  el.setAttribute("page-count", "2");
  el.setAttribute("prev-label", "Zurück");
  el.setAttribute("next-label", "Weiter");
  document.body.appendChild(el);
  assert.deepEqual(labels(el), ["Zurück", "Weiter"]);
});

test("missing or invalid attributes mean page 1 of 1", () => {
  reset();
  const el = document.createElement("as-pager");
  el.setAttribute("page", "x");
  document.body.appendChild(el);
  assert.equal(el.page, 1);
  assert.equal(el.pageCount, 1);
});
