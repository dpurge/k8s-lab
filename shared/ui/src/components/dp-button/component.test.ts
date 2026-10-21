import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpButton } from "./component.ts";

const inner = (el: DpButton) => el.shadowRoot!.querySelector("button")!;

test("renders a real button in its shadow root with the slotted text", async () => {
  const el = await mount<DpButton>("<dp-button>Save</dp-button>");
  assert.ok(inner(el));
  assert.equal(inner(el).type, "button");
  assert.equal(el.textContent, "Save");
  assert.ok(el.shadowRoot!.querySelector("slot"));
});

test("disabled reaches the inner button and follows changes", async () => {
  const el = await mount<DpButton>("<dp-button disabled>Save</dp-button>");
  assert.equal(inner(el).disabled, true);
  el.removeAttribute("disabled");
  await el.updateComplete;
  assert.equal(inner(el).disabled, false);
});

test("aria-label on the host is forwarded to the inner button", async () => {
  const el = await mount<DpButton>('<dp-button aria-label="Menu">☰</dp-button>');
  assert.equal(inner(el).getAttribute("aria-label"), "Menu");
  el.removeAttribute("aria-label");
  await el.updateComplete;
  assert.equal(inner(el).hasAttribute("aria-label"), false);
});

test("variant is reflected so the :host([variant]) styles apply", async () => {
  const el = await mount<DpButton>('<dp-button variant="primary">Go</dp-button>');
  assert.equal(el.getAttribute("variant"), "primary");
  el.variant = "danger";
  await el.updateComplete;
  assert.equal(el.getAttribute("variant"), "danger");
});

test("block is reflected so the button can fill its container", async () => {
  const el = await mount<DpButton>("<dp-button block>Go</dp-button>");
  assert.equal(el.block, true);
  assert.ok(el.hasAttribute("block"));
});

test("a click on the inner button reaches a listener on the host", async () => {
  const el = await mount<DpButton>("<dp-button>Click</dp-button>");
  let clicks = 0;
  el.addEventListener("click", () => clicks++);
  inner(el).click();
  assert.equal(clicks, 1);
});

test("a disabled button does not click", async () => {
  const el = await mount<DpButton>("<dp-button disabled>Click</dp-button>");
  let clicks = 0;
  el.addEventListener("click", () => clicks++);
  inner(el).click();
  assert.equal(clicks, 0);
});

test("type=submit submits the closest form, type=button does not", async () => {
  document.body.innerHTML = '<form><dp-button type="submit">Send</dp-button><dp-button>Plain</dp-button></form>';
  const form = document.querySelector("form")!;
  const [submit, plain] = Array.from(document.querySelectorAll("dp-button")) as DpButton[];
  await Promise.all([submit.updateComplete, plain.updateComplete]);
  let submits = 0;
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    submits++;
  });
  inner(plain).click();
  assert.equal(submits, 0);
  inner(submit).click();
  assert.equal(submits, 1);
});

// ── A submit button must work for forms the way a native one does ─────────────

const proxyOf = (el: Element) => el.querySelector<HTMLButtonElement>(':scope > button[type="submit"]');
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

test("a submit button keeps one hidden real submit button in the form's DOM, never drawn", async () => {
  const el = await mount<DpButton>('<form><dp-button type="submit">Send</dp-button></form>').then(() => document.querySelector("dp-button") as DpButton);
  await el.updateComplete;
  const proxy = proxyOf(el)!;
  assert.ok(proxy, "a hidden submit button is a child of the host");
  assert.equal(el.querySelectorAll('button[type="submit"]').length, 1);
  assert.equal(proxy.hidden, true);
  assert.equal(proxy.tabIndex, -1);
  assert.equal(proxy.getAttribute("aria-hidden"), "true");
  assert.equal(proxy.form, document.querySelector("form"), "it belongs to the page's form");
  const slot = el.shadowRoot!.querySelector("slot:not([name])") as HTMLSlotElement;
  assert.equal(slot.assignedNodes().includes(proxy), false, "it is not drawn inside the visible button");
  assert.equal(el.textContent, "Send", "the label is unchanged");
});

test("a plain button has no such child, and changing type adds and removes it", async () => {
  const el = await mount<DpButton>("<dp-button>Plain</dp-button>");
  assert.equal(proxyOf(el), null);
  el.type = "submit";
  await el.updateComplete;
  assert.ok(proxyOf(el));
  el.type = "button";
  await el.updateComplete;
  assert.equal(proxyOf(el), null);
});

test("the hidden submit button follows disabled, so a disabled button never submits", async () => {
  const el = await mount<DpButton>('<form><dp-button type="submit" disabled>Send</dp-button></form>').then(() => document.querySelector("dp-button") as DpButton);
  await el.updateComplete;
  assert.equal(proxyOf(el)!.disabled, true);
  el.removeAttribute("disabled");
  await el.updateComplete;
  assert.equal(proxyOf(el)!.disabled, false);
});

test("Enter in a field, which the browser turns into a click on the form's submit button, submits once", async () => {
  document.body.innerHTML = '<form><input name="a"><input name="b"><dp-button type="submit">Go</dp-button></form>';
  const el = document.querySelector("dp-button") as DpButton;
  await el.updateComplete;
  let submits = 0;
  document.querySelector("form")!.addEventListener("submit", (e) => {
    e.preventDefault();
    submits++;
  });
  proxyOf(el)!.click();
  assert.equal(submits, 1);
});

test("a click on the visible button submits exactly once, not twice", async () => {
  document.body.innerHTML = "<form><dp-button type=\"submit\">Go</dp-button></form>";
  const el = document.querySelector("dp-button") as DpButton;
  await el.updateComplete;
  let submits = 0;
  document.querySelector("form")!.addEventListener("submit", (e) => {
    e.preventDefault();
    submits++;
  });
  el.shadowRoot!.querySelector("button")!.click();
  assert.equal(submits, 1);
});

test("the form is submitted with the hidden button as its submitter", async () => {
  document.body.innerHTML = '<form><dp-button type="submit">Go</dp-button></form>';
  const el = document.querySelector("dp-button") as DpButton;
  await el.updateComplete;
  let submitter: unknown = null;
  document.querySelector("form")!.addEventListener("submit", (e) => {
    e.preventDefault();
    submitter = (e as SubmitEvent).submitter;
  });
  el.shadowRoot!.querySelector("button")!.click();
  assert.equal(submitter, proxyOf(el));
});

test("a page that replaces the host's content gets the hidden button back", async () => {
  const el = await mount<DpButton>('<form><dp-button type="submit">Go</dp-button></form>').then(() => document.querySelector("dp-button") as DpButton);
  await el.updateComplete;
  el.textContent = "Sending…";
  await settle();
  assert.ok(proxyOf(el), "the hidden submit button is back");
  assert.equal(el.querySelectorAll('button[type="submit"]').length, 1);
  assert.equal(el.textContent, "Sending…");
});

test("removing the host from the page stops it watching, and a re-added one works again", async () => {
  const form = document.createElement("form");
  form.innerHTML = '<dp-button type="submit">Go</dp-button>';
  document.body.replaceChildren(form);
  const el = form.querySelector("dp-button") as DpButton;
  await el.updateComplete;
  el.remove();
  form.append(el);
  await el.updateComplete;
  assert.equal(el.querySelectorAll('button[type="submit"]').length, 1);
});

test("a label never wraps onto two lines", async () => {
  const css = (DpButton.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /button\s*\{[^}]*white-space:\s*nowrap/);
});

test("padding and font size can be set from the page, for a smaller button in a table", async () => {
  const css = (DpButton.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /font-size:\s*var\(--dp-button-font-size,\s*1rem\)/);
  assert.match(css, /padding:\s*var\(--dp-button-padding,\s*0\.65rem 1\.4rem\)/);
});
