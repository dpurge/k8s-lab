import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpPager, pageItems } from "./component.ts";

const buttons = (el: DpPager) => Array.from(el.shadowRoot!.querySelectorAll<HTMLButtonElement>("button"));
const labels = (el: DpPager) =>
  Array.from(el.shadowRoot!.querySelectorAll("nav > *")).map((n) => n.textContent!.trim());

async function pager(page: number, pageCount: number, extra = "") {
  return mount<DpPager>(`<dp-pager page="${page}" page-count="${pageCount}" ${extra}></dp-pager>`);
}

test("renders nothing for a single page", async () => {
  const el = await pager(1, 1);
  assert.equal(el.shadowRoot!.querySelector("nav"), null);
});

test("renders nothing when page-count is missing, zero or not a number", async () => {
  for (const html of ["<dp-pager></dp-pager>", '<dp-pager page-count="0"></dp-pager>', '<dp-pager page-count="x"></dp-pager>']) {
    const el = await mount<DpPager>(html);
    assert.equal(el.shadowRoot!.querySelector("nav"), null, html);
  }
});

test("two pages show only Previous and Next", async () => {
  const el = await pager(1, 2);
  assert.deepEqual(labels(el), ["Previous", "Next"]);
  assert.equal(el.shadowRoot!.querySelector<HTMLButtonElement>(".prev")!.disabled, true);
  assert.equal(el.shadowRoot!.querySelector<HTMLButtonElement>(".next")!.disabled, false);
});

test("three pages show numbers with the current one marked", async () => {
  const el = await pager(2, 3);
  assert.deepEqual(labels(el), ["Previous", "1", "2", "3", "Next"]);
  const current = el.shadowRoot!.querySelectorAll('[aria-current="page"]');
  assert.equal(current.length, 1);
  assert.equal(current[0].textContent!.trim(), "2");
  assert.ok(current[0].classList.contains("current"));
});

test("Previous is disabled on the first page and Next on the last", async () => {
  const first = await pager(1, 5);
  assert.equal(first.shadowRoot!.querySelector<HTMLButtonElement>(".prev")!.disabled, true);
  assert.equal(first.shadowRoot!.querySelector<HTMLButtonElement>(".next")!.disabled, false);
  const last = await pager(5, 5);
  assert.equal(last.shadowRoot!.querySelector<HTMLButtonElement>(".prev")!.disabled, false);
  assert.equal(last.shadowRoot!.querySelector<HTMLButtonElement>(".next")!.disabled, true);
});

test("up to ten pages show every number with no ellipsis", () => {
  assert.deepEqual(pageItems(5, 10), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
  assert.deepEqual(DpPager.pageItems(1, 3), [1, 2, 3]);
});

test("more than ten pages show a ten-number window with ellipses where pages are hidden", () => {
  assert.deepEqual(pageItems(1, 25), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, null]);
  assert.deepEqual(pageItems(5, 25), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, null]);
  assert.deepEqual(pageItems(6, 25), [null, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, null]);
  assert.deepEqual(pageItems(13, 25), [null, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, null]);
  assert.deepEqual(pageItems(25, 25), [null, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25]);
  assert.deepEqual(pageItems(22, 25), [null, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25]);
});

test("the window never exceeds the limit and always contains the current page", () => {
  for (const limit of [3, 5, 10]) {
    for (const count of [3, 4, 9, 10, 11, 40]) {
      for (let page = 1; page <= count; page++) {
        const numbers = pageItems(page, count, limit).filter((n): n is number => n !== null);
        assert.ok(numbers.length <= limit, `${limit}/${count}/${page}`);
        assert.ok(numbers.includes(page), `${limit}/${count}/${page}`);
        assert.deepEqual(numbers, numbers.slice().sort((a, b) => a - b));
      }
    }
  }
});

test("max-numbers changes the window", async () => {
  assert.deepEqual(pageItems(10, 20, 5), [null, 8, 9, 10, 11, 12, null]);
  const el = await pager(10, 20, 'max-numbers="5"');
  assert.deepEqual(labels(el), ["Previous", "…", "8", "9", "10", "11", "12", "…", "Next"]);
});

test("renders ellipsis gaps for a long range", async () => {
  const el = await pager(13, 25);
  assert.deepEqual(labels(el), ["Previous", "…", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "…", "Next"]);
  assert.equal(el.shadowRoot!.querySelectorAll(".gap").length, 2);
});

test("clicking emits dp-pager-select with the target page, and it bubbles out of the shadow root", async () => {
  const el = await pager(4, 9);
  const pages: number[] = [];
  document.addEventListener("dp-pager-select", (e) => pages.push((e as CustomEvent).detail.page));
  const byText = (t: string) => buttons(el).find((b) => b.textContent!.trim() === t)!;
  byText("Next").click();
  byText("Previous").click();
  byText("6").click();
  assert.deepEqual(pages, [5, 3, 6]);
});

test("the current page's button reports its own page, so the host can reload", async () => {
  const el = await pager(3, 6);
  const pages: number[] = [];
  el.addEventListener("dp-pager-select", (e) => pages.push((e as CustomEvent).detail.page));
  el.shadowRoot!.querySelector<HTMLButtonElement>(".current")!.click();
  assert.deepEqual(pages, [3]);
});

test("a disabled button sends nothing", async () => {
  const el = await pager(1, 4);
  const pages: number[] = [];
  el.addEventListener("dp-pager-select", (e) => pages.push((e as CustomEvent).detail.page));
  el.shadowRoot!.querySelector<HTMLButtonElement>(".prev")!.click();
  assert.deepEqual(pages, []);
});

test("custom labels are used, and the navigation has an accessible name", async () => {
  const el = await pager(2, 2, 'prev-label="Zurück" next-label="Weiter" label="Seiten"');
  assert.deepEqual(labels(el), ["Zurück", "Weiter"]);
  assert.equal(el.shadowRoot!.querySelector("nav")!.getAttribute("aria-label"), "Seiten");
  const plain = await pager(2, 2);
  assert.equal(plain.shadowRoot!.querySelector("nav")!.getAttribute("aria-label"), "Pages");
});

test("a page outside the range is kept within it", async () => {
  const high = await pager(99, 5);
  assert.equal(high.shadowRoot!.querySelector(".current")!.textContent!.trim(), "5");
  const low = await pager(0, 5);
  assert.equal(low.shadowRoot!.querySelector(".current")!.textContent!.trim(), "1");
  const negative = await pager(-3, 5);
  assert.equal(negative.shadowRoot!.querySelector(".current")!.textContent!.trim(), "1");
});

test("changing page, page-count and labels re-renders", async () => {
  const el = await pager(1, 3);
  el.page = 3;
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelector(".current")!.textContent!.trim(), "3");
  el.setAttribute("page-count", "1");
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelector("nav"), null);
  el.setAttribute("page-count", "2");
  el.setAttribute("prev-label", "Back");
  await el.updateComplete;
  assert.deepEqual(labels(el), ["Back", "Next"]);
});

test("the buttons are real buttons that never submit a form", async () => {
  const el = await pager(2, 4);
  for (const b of buttons(el)) assert.equal(b.type, "button");
});
