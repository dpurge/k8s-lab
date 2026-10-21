import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpDialog } from "./component.ts";

const root = (d: DpDialog) => d.shadowRoot!;
const native = (d: DpDialog) => root(d).querySelector("dialog")!;
const ok = (d: DpDialog) => root(d).querySelector<HTMLElement>("dp-button.ok")!;
const cancel = (d: DpDialog) => root(d).querySelector<HTMLElement>("dp-button.cancel");
const click = async (d: DpDialog, el: Element) => {
  el.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  await d.updateComplete;
};

async function fresh() {
  return mount<DpDialog>("<dp-dialog></dp-dialog>");
}

test("confirm opens the native dialog as a modal with the message", async () => {
  const d = await fresh();
  const answer = d.confirm("Delete this item?");
  await d.updateComplete;
  assert.equal(native(d).hasAttribute("open"), true);
  assert.equal(d.hasAttribute("open"), true);
  assert.equal(root(d).querySelector(".message")!.textContent, "Delete this item?");
  void answer;
});

test("OK resolves true and Cancel resolves false", async () => {
  const d = await fresh();
  let answer = d.confirm("Sure?");
  await d.updateComplete;
  await click(d, ok(d));
  assert.equal(await answer, true);
  assert.equal(native(d).hasAttribute("open"), false);
  assert.equal(d.hasAttribute("open"), false);

  answer = d.confirm("Sure?");
  await d.updateComplete;
  await click(d, cancel(d)!);
  assert.equal(await answer, false);
});

test("the message is text, never markup, and keeps its line breaks", async () => {
  const d = await fresh();
  void d.confirm("<img src=x onerror=alert(1)>\nsecond line");
  await d.updateComplete;
  const message = root(d).querySelector(".message")!;
  assert.equal(message.querySelector("img"), null);
  assert.equal(message.textContent, "<img src=x onerror=alert(1)>\nsecond line");
  assert.match((DpDialog.styles as unknown as { cssText: string }).cssText, /\.message\s*\{[^}]*white-space:\s*pre-wrap/);
});

test("options: labels, danger, hideCancel", async () => {
  const d = await fresh();
  void d.confirm("Sure?", { okLabel: "Remove", cancelLabel: "Keep", danger: true });
  await d.updateComplete;
  assert.equal(ok(d).textContent!.trim(), "Remove");
  assert.equal(ok(d).getAttribute("variant"), "danger");
  assert.equal(cancel(d)!.textContent!.trim(), "Keep");

  void d.confirm("Plain");
  await d.updateComplete;
  assert.equal(ok(d).textContent!.trim(), "OK");
  assert.equal(ok(d).getAttribute("variant"), "primary");
  assert.equal(cancel(d)!.textContent!.trim(), "Cancel");

  void d.confirm("View", { hideCancel: true, okLabel: "Close" });
  await d.updateComplete;
  assert.equal(cancel(d), null);
  assert.equal(ok(d).textContent!.trim(), "Close");
});

test("wide adds the wide box, and the next dialog is not wide", async () => {
  const d = await fresh();
  void d.showContent(document.createElement("div"), { wide: true });
  await d.updateComplete;
  assert.equal(root(d).querySelector(".box")!.classList.contains("wide"), true);
  void d.confirm("Next");
  await d.updateComplete;
  assert.equal(root(d).querySelector(".box")!.classList.contains("wide"), false);
});

test("showContent puts the page's own node in the light DOM, where its form controls stay the page's", async () => {
  const d = await fresh();
  const form = document.createElement("form");
  form.innerHTML = '<textarea id="phrase" name="phrase"></textarea><input id="tags" name="tags">';
  const answer = d.showContent(form, { okLabel: "Export" });
  await d.updateComplete;
  assert.equal(form.parentElement, d, "the node is a light DOM child of the dialog");
  const textarea = document.getElementById("phrase") as HTMLTextAreaElement;
  assert.equal(textarea.getRootNode(), document, "the control is in the page's DOM, not in a shadow root");
  assert.equal(root(d).querySelector("textarea, input, select"), null);
  assert.ok(root(d).querySelector("slot"));
  textarea.onkeydown = function () {};
  assert.equal(typeof textarea.onkeydown, "function");
  await click(d, ok(d));
  assert.equal(await answer, true);
});

test("showContent replaces earlier content, and confirm clears it", async () => {
  const d = await fresh();
  const first = document.createElement("p");
  const second = document.createElement("p");
  void d.showContent(first);
  void d.showContent(second);
  await d.updateComplete;
  assert.deepEqual(Array.from(d.children), [second]);
  void d.confirm("just text");
  await d.updateComplete;
  assert.equal(d.children.length, 0);
});

test("opening a dialog that is open answers the earlier call with false", async () => {
  const d = await fresh();
  const first = d.confirm("one");
  await d.updateComplete;
  const second = d.confirm("two");
  await d.updateComplete;
  assert.equal(await first, false);
  assert.equal(root(d).querySelector(".message")!.textContent, "two");
  await click(d, ok(d));
  assert.equal(await second, true);
});

test("Escape, which the browser handles by closing the dialog itself, resolves false", async () => {
  const d = await fresh();
  const answer = d.confirm("Sure?");
  await d.updateComplete;
  native(d).close();
  assert.equal(await answer, false);
  assert.equal(d.hasAttribute("open"), false);
});

test("a click on the backdrop resolves false, but only when the press began there too", async () => {
  const d = await fresh();
  let answer = d.confirm("Sure?");
  await d.updateComplete;
  native(d).dispatchEvent(new window.MouseEvent("mousedown", { bubbles: true }));
  native(d).dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  assert.equal(await answer, false);

  // A selection dragged from inside the box and released on the backdrop.
  answer = d.confirm("Sure?");
  await d.updateComplete;
  root(d).querySelector(".box")!.dispatchEvent(new window.MouseEvent("mousedown", { bubbles: true }));
  native(d).dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  assert.equal(d.hasAttribute("open"), true, "still open");
  d.close(true);
  assert.equal(await answer, true);
});

test("a click inside the box does not close it", async () => {
  const d = await fresh();
  void d.confirm("Sure?");
  await d.updateComplete;
  const box = root(d).querySelector(".box")!;
  box.dispatchEvent(new window.MouseEvent("mousedown", { bubbles: true }));
  await click(d, box);
  assert.equal(d.hasAttribute("open"), true);
});

test("dp-dialog-close tells the page the answer", async () => {
  const d = await fresh();
  const seen: boolean[] = [];
  document.addEventListener("dp-dialog-close", (e) => seen.push((e as CustomEvent).detail.confirmed));
  void d.confirm("one");
  await d.updateComplete;
  await click(d, ok(d));
  void d.confirm("two");
  await d.updateComplete;
  await click(d, cancel(d)!);
  assert.deepEqual(seen, [true, false]);
});

test("close from code answers false by default, and true when asked", async () => {
  const d = await fresh();
  let answer = d.confirm("x");
  await d.updateComplete;
  d.close();
  await d.updateComplete;
  assert.equal(await answer, false);
  answer = d.confirm("x");
  await d.updateComplete;
  d.close(true);
  await d.updateComplete;
  assert.equal(await answer, true);
  d.close();
});

test("the open attribute opens and closes it declaratively", async () => {
  const d = await mount<DpDialog>('<dp-dialog heading="About">text</dp-dialog>');
  d.setAttribute("open", "");
  await d.updateComplete;
  assert.equal(native(d).hasAttribute("open"), true);
  assert.equal(root(d).querySelector("h2")!.textContent, "About");
  assert.equal(native(d).getAttribute("aria-labelledby"), "heading");
  d.removeAttribute("open");
  await d.updateComplete;
  assert.equal(native(d).hasAttribute("open"), false);
});

test("the dialog is named by its message when it has no heading", async () => {
  const d = await fresh();
  void d.confirm("Delete?");
  await d.updateComplete;
  assert.equal(native(d).getAttribute("aria-labelledby"), "message");
});

test("the content area is hidden until there is content", async () => {
  const d = await fresh();
  void d.confirm("just text");
  await d.updateComplete;
  assert.equal(root(d).querySelector(".content")!.classList.contains("has-content"), false);
});

test("the content area is shown once the page's content arrives", async () => {
  const d = await fresh();
  void d.showContent(document.createElement("div"));
  await d.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 0));
  await d.updateComplete;
  assert.equal(root(d).querySelector(".content")!.classList.contains("has-content"), true);
});
