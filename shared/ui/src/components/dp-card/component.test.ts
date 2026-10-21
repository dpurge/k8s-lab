import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpCard } from "./component.ts";

const body = (el: DpCard) => el.shadowRoot!.querySelector(".card")!;

function collect(el: Element) {
  const events: CustomEvent[] = [];
  el.addEventListener("dp-card-select", (e) => events.push(e as CustomEvent));
  return events;
}

function clearSelection() {
  document.getSelection()?.removeAllRanges();
}

test("renders the slotted content in a card part", async () => {
  const el = await mount<DpCard>("<dp-card><p>hello</p></dp-card>");
  assert.equal(body(el).getAttribute("part"), "card");
  assert.ok(body(el).querySelector("slot"));
  assert.equal(el.querySelector("p")!.textContent, "hello");
});

test("heading draws an h2 part, and removing it removes the h2", async () => {
  const el = await mount<DpCard>('<dp-card heading="Tasks">x</dp-card>');
  const h2 = () => el.shadowRoot!.querySelector("h2");
  assert.equal(h2()!.textContent, "Tasks");
  assert.equal(h2()!.getAttribute("part"), "heading");
  el.removeAttribute("heading");
  await el.updateComplete;
  assert.equal(h2(), null);
});

test("a card that is not interactive sends nothing and is not focusable", async () => {
  const el = await mount<DpCard>("<dp-card>text</dp-card>");
  const events = collect(el);
  el.click();
  assert.equal(events.length, 0);
  assert.equal(el.hasAttribute("tabindex"), false);
});

test("a click on an interactive card sends dp-card-select with its value", async () => {
  clearSelection();
  const el = await mount<DpCard>('<dp-card interactive value="42"><b>Item</b></dp-card>');
  const events = collect(el);
  el.querySelector("b")!.click();
  assert.equal(events.length, 1);
  assert.deepEqual(events[0].detail, { value: "42" });
  assert.equal(events[0].bubbles, true);
  assert.equal(events[0].composed, true);
});

test("the event reaches a listener on an ancestor, for one delegated handler per list", async () => {
  clearSelection();
  document.body.innerHTML = '<div id="list"><dp-card interactive value="a">A</dp-card><dp-card interactive value="b">B</dp-card></div>';
  const seen: string[] = [];
  document.getElementById("list")!.addEventListener("dp-card-select", (e) => seen.push((e as CustomEvent).detail.value));
  (document.querySelectorAll("dp-card")[1] as HTMLElement).click();
  assert.deepEqual(seen, ["b"]);
});

test("a click on a link, button or form control inside the card does not select it", async () => {
  clearSelection();
  const el = await mount<DpCard>(
    '<dp-card interactive><a href="#x" id="a">link</a><button id="b">b</button><input id="i"><label id="l">l</label></dp-card>',
  );
  const events = collect(el);
  for (const id of ["a", "b", "i", "l"]) el.querySelector<HTMLElement>(`#${id}`)!.click();
  assert.equal(events.length, 0);
});

test("a control that wraps the whole card does not stop the card from being selected", async () => {
  clearSelection();
  document.body.innerHTML = '<a href="#wrap"><dp-card interactive>text</dp-card></a>';
  const el = document.querySelector("dp-card") as DpCard;
  await el.updateComplete;
  const events = collect(el);
  el.click();
  assert.equal(events.length, 1);
});

test("a click while text is selected does not select the card", async () => {
  const el = await mount<DpCard>('<dp-card interactive><span id="t">some text</span></dp-card>');
  const events = collect(el);
  document.getSelection()!.selectAllChildren(el.querySelector("#t")!);
  assert.notEqual(document.getSelection()!.toString(), "", "the test needs a real selection");
  el.click();
  assert.equal(events.length, 0);
  clearSelection();
});

test("without preventDefault the card clicks its [data-open] link", async () => {
  clearSelection();
  const el = await mount<DpCard>('<dp-card interactive><h2><a href="#x" data-open="7">Open</a></h2><p id="p">meta</p></dp-card>');
  let opened = 0;
  el.querySelector("a")!.addEventListener("click", (e) => {
    e.preventDefault();
    opened++;
  });
  el.querySelector<HTMLElement>("#p")!.click();
  assert.equal(opened, 1);
});

test("a listener that calls preventDefault keeps the card from opening the link", async () => {
  clearSelection();
  const el = await mount<DpCard>('<dp-card interactive><a href="#x" data-open>Open</a><p id="p">meta</p></dp-card>');
  let opened = 0;
  el.querySelector("a")!.addEventListener("click", (e) => {
    e.preventDefault();
    opened++;
  });
  el.addEventListener("dp-card-select", (e) => e.preventDefault());
  el.querySelector<HTMLElement>("#p")!.click();
  assert.equal(opened, 0);
});

test("clicking the [data-open] link itself opens it once, with no second activation", async () => {
  clearSelection();
  const el = await mount<DpCard>('<dp-card interactive><a href="#x" data-open>Open</a></dp-card>');
  const events = collect(el);
  let opened = 0;
  el.querySelector("a")!.addEventListener("click", (e) => {
    e.preventDefault();
    opened++;
  });
  el.querySelector("a")!.click();
  assert.equal(opened, 1);
  assert.equal(events.length, 0);
});

test("an interactive card with no link is focusable and works from the keyboard", async () => {
  const el = await mount<DpCard>('<dp-card interactive value="k">text</dp-card>');
  assert.equal(el.getAttribute("tabindex"), "0");
  assert.equal(el.getAttribute("role"), "button");
  const events = collect(el);
  for (const key of ["Enter", " "]) el.dispatchEvent(new window.KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  el.dispatchEvent(new window.KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true }));
  assert.equal(events.length, 2);
});

test("a card with a [data-open] link is not made focusable, so Tab does not stop twice", async () => {
  const el = await mount<DpCard>('<dp-card interactive><a href="#x" data-open>Open</a></dp-card>');
  assert.equal(el.hasAttribute("tabindex"), false);
  assert.equal(el.hasAttribute("role"), false);
});

test("turning interactive off removes the focusability the card added, but not the author's", async () => {
  const el = await mount<DpCard>("<dp-card interactive>text</dp-card>");
  el.interactive = false;
  await el.updateComplete;
  assert.equal(el.hasAttribute("tabindex"), false);
  assert.equal(el.hasAttribute("role"), false);

  const own = await mount<DpCard>('<dp-card interactive tabindex="-1">text</dp-card>');
  own.interactive = false;
  await own.updateComplete;
  assert.equal(own.getAttribute("tabindex"), "-1");
});

test("a card added after the page loaded wires itself, and one removed stops listening", async () => {
  clearSelection();
  const el = await mount<DpCard>('<dp-card interactive value="late">text</dp-card>');
  const events = collect(el);
  el.click();
  assert.equal(events.length, 1);
  el.remove();
  el.click();
  assert.equal(events.length, 1);
});
