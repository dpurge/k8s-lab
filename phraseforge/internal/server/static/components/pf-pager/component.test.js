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
const PfPager = customElements.get("pf-pager");

function mount(page, pageCount) {
  const el = document.createElement("pf-pager");
  el.setAttribute("page", String(page));
  el.setAttribute("page-count", String(pageCount));
  document.body.appendChild(el);
  return el;
}
const labels = (el) => [...el.querySelectorAll(".pf-pager-item, .pf-pager-gap")].map((n) => n.textContent);

test("renders nothing for a single page", () => {
  assert.equal(mount(1, 1).children.length, 0);
});

test("two pages show only Previous and Next", () => {
  const el = mount(1, 2);
  assert.deepEqual(labels(el), ["Previous", "Next"]);
  assert.equal(el.querySelector(".pf-pager-prev").disabled, true);
  assert.equal(el.querySelector(".pf-pager-next").disabled, false);
});

test("three pages show numbers with the current one marked", () => {
  const el = mount(2, 3);
  assert.deepEqual(labels(el), ["Previous", "1", "2", "3", "Next"]);
  const active = el.querySelectorAll(".pf-pager-item.active");
  assert.equal(active.length, 1);
  assert.equal(active[0].textContent, "2");
});

test("up to ten pages show every number with no ellipsis", () => {
  assert.deepEqual(PfPager.pageItems(5, 10), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
});

test("more than ten pages show a ten-number window with ellipses where pages are hidden", () => {
  assert.deepEqual(PfPager.pageItems(1, 25), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, null]);
  assert.deepEqual(PfPager.pageItems(5, 25), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, null]);
  assert.deepEqual(PfPager.pageItems(6, 25), [null, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, null]);
  assert.deepEqual(PfPager.pageItems(13, 25), [null, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, null]);
  assert.deepEqual(PfPager.pageItems(25, 25), [null, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25]);
  assert.deepEqual(PfPager.pageItems(22, 25), [null, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25]);
});

test("the window never exceeds ten numbers and always contains the current page", () => {
  for (let count = 3; count <= 40; count++) {
    for (let page = 1; page <= count; page++) {
      const numbers = PfPager.pageItems(page, count).filter((n) => n !== null);
      assert.ok(numbers.length <= 10, `${page}/${count}`);
      assert.ok(numbers.includes(page), `${page}/${count}`);
    }
  }
});

test("renders ellipsis gaps in the DOM for a long range", () => {
  assert.deepEqual(labels(mount(13, 25)), [
    "Previous", "…", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "…", "Next",
  ]);
});

test("clicking emits pf-pager-select with the target page", () => {
  const el = mount(3, 6);
  const pages = [];
  el.addEventListener("pf-pager-select", (e) => pages.push(e.detail.page));
  el.querySelector(".pf-pager-prev").click();
  el.querySelector(".pf-pager-next").click();
  [...el.querySelectorAll(".pf-pager-item")].find((b) => b.textContent === "6").click();
  assert.deepEqual(pages, [2, 4, 6]);
});

test("custom labels are used", () => {
  const el = document.createElement("pf-pager");
  el.setAttribute("page", "1");
  el.setAttribute("page-count", "2");
  el.setAttribute("prev-label", "Zurück");
  el.setAttribute("next-label", "Weiter");
  document.body.appendChild(el);
  assert.deepEqual(labels(el), ["Zurück", "Weiter"]);
});
