import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpTabs, type TabItem } from "./component.ts";

const entries = (el: DpTabs) => Array.from(el.shadowRoot!.querySelectorAll<HTMLElement>(".item"));
const texts = (el: DpTabs) => entries(el).map((e) => e.textContent!.trim());
const ITEMS = [
  { id: "a", label: "Alpha" },
  { id: "b", label: "Beta" },
  { id: "c", label: "Gamma" },
];

async function tabs(active = "a", items: TabItem[] = ITEMS) {
  const el = await mount<DpTabs>("<dp-tabs></dp-tabs>");
  el.items = items;
  el.active = active;
  await el.updateComplete;
  return el;
}

test("renders every item as a button, with exactly one marked current", async () => {
  const el = await tabs("b");
  assert.deepEqual(texts(el), ["Alpha", "Beta", "Gamma"]);
  for (const e of entries(el)) {
    assert.equal(e.tagName, "BUTTON");
    assert.equal((e as HTMLButtonElement).type, "button");
  }
  const current = entries(el).filter((e) => e.classList.contains("active"));
  assert.equal(current.length, 1);
  assert.equal(current[0].textContent!.trim(), "Beta");
  assert.equal(current[0].getAttribute("aria-current"), "page");
  assert.equal(entries(el).filter((e) => e.hasAttribute("aria-current")).length, 1);
});

test("a click sends dp-tabs-select with the id, and makes it the active one", async () => {
  const el = await tabs("a");
  const seen: string[] = [];
  document.addEventListener("dp-tabs-select", (e) => seen.push((e as CustomEvent).detail.id));
  entries(el)[2].click();
  await el.updateComplete;
  assert.deepEqual(seen, ["c"]);
  assert.equal(el.active, "c");
  assert.equal(entries(el)[2].classList.contains("active"), true);
  assert.equal(entries(el)[0].classList.contains("active"), false);
});

test("a listener that calls preventDefault keeps the active tab where it was", async () => {
  const el = await tabs("a");
  el.addEventListener("dp-tabs-select", (e) => e.preventDefault());
  entries(el)[1].click();
  await el.updateComplete;
  assert.equal(el.active, "a");
  assert.equal(entries(el)[0].classList.contains("active"), true);
});

test("the event bubbles and is composed, so one delegated listener can serve a page", async () => {
  const el = await tabs();
  const event = await new Promise<Event>((resolve) => {
    el.addEventListener("dp-tabs-select", resolve, { once: true });
    entries(el)[1].click();
  });
  assert.equal(event.bubbles, true);
  assert.equal(event.composed, true);
  assert.equal(event.cancelable, true);
});

test("the component never calls into the host page", async () => {
  const el = await tabs();
  const before = Object.keys(globalThis).length;
  entries(el)[1].click();
  assert.equal(Object.keys(globalThis).length, before);
});

test("re-rendering the items keeps the active tab", async () => {
  const el = await tabs("b");
  el.items = [{ id: "a", label: "A2" }, { id: "b", label: "B2" }];
  await el.updateComplete;
  assert.deepEqual(texts(el), ["A2", "B2"]);
  assert.equal(entries(el)[1].classList.contains("active"), true);
});

test("an active id that matches no item marks nothing", async () => {
  const el = await tabs("zzz");
  assert.equal(entries(el).some((e) => e.classList.contains("active")), false);
  assert.equal(entries(el).some((e) => e.hasAttribute("aria-current")), false);
});

test("an item with an href is a real link, still reported and marked current", async () => {
  const el = await tabs("b", [
    { id: "a", label: "Chat", href: "#/chat" },
    { id: "b", label: "Tasks", href: "#/tasks" },
    { id: "c", label: "Plain" },
  ]);
  const [chat, tasks, plain] = entries(el);
  assert.equal(chat.tagName, "A");
  assert.equal(chat.getAttribute("href"), "#/chat");
  assert.equal(tasks.getAttribute("aria-current"), "page");
  assert.equal(plain.tagName, "BUTTON");
  const seen: string[] = [];
  el.addEventListener("dp-tabs-select", (e) => seen.push((e as CustomEvent).detail.id));
  chat.addEventListener("click", (e) => e.preventDefault());
  chat.click();
  assert.deepEqual(seen, ["a"]);
});

test("items can be given as a JSON attribute", async () => {
  const el = await mount<DpTabs>(`<dp-tabs active="tasks" items='[{"id":"chat","label":"Chat","href":"#/chat"},{"id":"tasks","label":"Tasks","href":"#/tasks"}]'></dp-tabs>`);
  assert.deepEqual(texts(el), ["Chat", "Tasks"]);
  assert.equal(entries(el)[1].classList.contains("active"), true);
});

test("a bad JSON attribute or bad items draw an empty bar instead of breaking the page", async () => {
  const bad = await mount<DpTabs>(`<dp-tabs items='{not json'></dp-tabs>`);
  assert.deepEqual(texts(bad), []);
  const el = await tabs("a");
  (el as unknown as { items: unknown }).items = [null, 7, { label: "no id" }, { id: "x" }, { id: "ok", label: "Fine" }, "text"];
  await el.updateComplete;
  assert.deepEqual(texts(el), ["Fine"]);
  (el as unknown as { items: unknown }).items = "nope";
  await el.updateComplete;
  assert.deepEqual(texts(el), []);
});

test("labels are text, never markup", async () => {
  const el = await tabs("a", [{ id: "a", label: "<img src=x onerror=alert(1)>" }]);
  assert.equal(el.shadowRoot!.querySelector("img"), null);
  assert.equal(texts(el)[0], "<img src=x onerror=alert(1)>");
});

test("orientation and variant are reflected as attributes for the styles, with defaults", async () => {
  const el = await tabs();
  assert.equal(el.getAttribute("orientation"), "horizontal");
  assert.equal(el.getAttribute("variant"), "pills");
  el.orientation = "vertical";
  el.variant = "underline";
  await el.updateComplete;
  assert.equal(el.getAttribute("orientation"), "vertical");
  assert.equal(el.getAttribute("variant"), "underline");
  const css = (DpTabs.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /:host\(\[orientation="vertical"\]\)/);
  assert.match(css, /:host\(\[variant="underline"\]\)/);
});

test("the navigation has an accessible name when given one", async () => {
  const el = await tabs();
  assert.equal(el.shadowRoot!.querySelector("nav")!.hasAttribute("aria-label"), false);
  el.label = "Sections";
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelector("nav")!.getAttribute("aria-label"), "Sections");
});

test("an empty list draws an empty navigation, and parts are exposed", async () => {
  const el = await mount<DpTabs>("<dp-tabs></dp-tabs>");
  assert.equal(entries(el).length, 0);
  el.items = ITEMS;
  el.active = "a";
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelector("nav")!.getAttribute("part"), "nav");
  assert.equal(entries(el)[0].getAttribute("part"), "item active");
  assert.equal(entries(el)[1].getAttribute("part"), "item");
});
