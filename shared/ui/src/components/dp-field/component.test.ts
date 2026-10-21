import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpField } from "./component.ts";

const label = (el: Element) => el.querySelector<HTMLLabelElement>(':scope > label[slot="label"]');

// Lit re-runs #sync from slotchange, which jsdom delivers as a microtask.
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

test("makes a label from the attribute, tied to the control by for, and leaves the control alone", async () => {
  const el = await mount<DpField>('<dp-field label="Language" for="language"><select id="language" name="language" required></select></dp-field>');
  const l = label(el)!;
  assert.equal(l.textContent, "Language");
  assert.equal(l.htmlFor, "language");
  const select = document.getElementById("language") as HTMLSelectElement;
  assert.equal(select.parentElement, el, "the control is still a child of the field, in the page's DOM");
  assert.equal(select.name, "language");
  assert.equal(select.required, true);
  assert.equal(l.control, select, "the label is natively associated, so a click focuses the control");
});

test("the label comes first, ahead of the control", async () => {
  const el = await mount<DpField>('<dp-field label="Name"><input id="n"></dp-field>');
  assert.equal(el.children[0].tagName, "LABEL");
  assert.equal(el.children[1].id, "n");
});

test("shows the label slot before the control slot", async () => {
  const el = await mount<DpField>('<dp-field label="Name"><input></dp-field>');
  const slots = Array.from(el.shadowRoot!.querySelectorAll("slot"));
  assert.deepEqual(slots.map((s) => s.getAttribute("name")), ["label", null]);
});

test("without for, the control's own id is used", async () => {
  const el = await mount<DpField>('<dp-field label="Email"><input id="email"></dp-field>');
  assert.equal(label(el)!.htmlFor, "email");
});

test("a control with no id is given a unique one, so two fields never share it", async () => {
  document.body.innerHTML = '<dp-field label="A"><input></dp-field><dp-field label="B"><input></dp-field>';
  const [a, b] = Array.from(document.querySelectorAll("dp-field")) as DpField[];
  await Promise.all([a.updateComplete, b.updateComplete]);
  const ids = [a, b].map((f) => f.querySelector("input")!.id);
  assert.ok(ids[0] && ids[1] && ids[0] !== ids[1], ids.join(","));
  assert.equal(label(a)!.control, a.querySelector("input"));
  assert.equal(label(b)!.control, b.querySelector("input"));
});

test("an explicit for wins over the control's id", async () => {
  const el = await mount<DpField>('<dp-field label="X" for="elsewhere"><input id="own"></dp-field>');
  assert.equal(label(el)!.htmlFor, "elsewhere");
});

test("a control that arrives after the field, as in a parser, still gets its label tied to it", async () => {
  const el = await mount<DpField>('<dp-field label="Late"></dp-field>');
  assert.equal(label(el)!.htmlFor, "");
  const input = document.createElement("input");
  el.appendChild(input);
  await settle();
  assert.ok(input.id, "the control got an id");
  assert.equal(label(el)!.htmlFor, input.id);
});

test("changing label updates the text, and clearing it removes the label", async () => {
  const el = await mount<DpField>('<dp-field label="One"><input></dp-field>');
  el.label = "Two";
  await el.updateComplete;
  assert.equal(label(el)!.textContent, "Two");
  el.removeAttribute("label");
  await el.updateComplete;
  assert.equal(label(el), null);
  el.setAttribute("label", "Back");
  await el.updateComplete;
  assert.equal(label(el)!.textContent, "Back");
});

test("a label removed by the page is made again", async () => {
  const el = await mount<DpField>('<dp-field label="Keep"><input></dp-field>');
  label(el)!.remove();
  await settle();
  assert.equal(label(el)!.textContent, "Keep");
});

test("a label of your own is respected: none is generated, and its missing for is filled in", async () => {
  const el = await mount<DpField>('<dp-field label="Ignored"><label slot="label"><b>Rich</b> label</label><input id="r"></dp-field>');
  await settle();
  const labels = el.querySelectorAll("label");
  assert.equal(labels.length, 1);
  assert.equal(labels[0].htmlFor, "r");
  assert.equal(labels[0].querySelector("b")!.textContent, "Rich");
});

test("a field with no label attribute makes no label", async () => {
  const el = await mount<DpField>("<dp-field><input></dp-field>");
  assert.equal(el.querySelector("label"), null);
});

test("hidden hides the field, even though the host sets display: block", async () => {
  const el = await mount<DpField>('<dp-field label="H" hidden><input></dp-field>');
  assert.equal(el.hasAttribute("hidden"), true);
  assert.match((DpField.styles as unknown as { cssText: string }).cssText, /:host\(\[hidden\]\)\s*\{\s*display:\s*none/);
});

test("the label text is set as text, never as markup", async () => {
  const el = await mount<DpField>('<dp-field><input></dp-field>');
  el.label = "<img src=x onerror=alert(1)>";
  await el.updateComplete;
  assert.equal(label(el)!.querySelector("img"), null);
  assert.equal(label(el)!.textContent, "<img src=x onerror=alert(1)>");
});

// ── The control stays a native element in the page, so an input method works ──
// Phraseforge's own IME (ime.js, wired by editor.js) assigns onkeydown,
// onkeypress and onkeyup to the control and reads this.value and
// this.selectionStart; the operating system's IME composes into the native
// control. Both need the control to be the page's own, untouched element.

test("the control is the page's own native element: found by id, not inside any shadow root", async () => {
  const el = await mount<DpField>('<dp-field label="Phrase"><textarea id="phrase"></textarea></dp-field>');
  const textarea = document.getElementById("phrase")!;
  assert.equal(textarea.tagName, "TEXTAREA");
  assert.equal(textarea.getRootNode(), document);
  assert.equal(el.shadowRoot!.querySelector("textarea, input, select"), null, "no control is drawn in the shadow root");
});

test("handlers the page assigns to the control keep firing, with the control as this", async () => {
  await mount<DpField>('<dp-field label="Phrase"><textarea id="t"></textarea></dp-field>');
  const t = document.getElementById("t") as HTMLTextAreaElement;
  const seen: string[] = [];
  t.onkeydown = function (this: GlobalEventHandlers, e: KeyboardEvent) {
    seen.push(`down:${e.key}:${this === t}`);
  };
  t.onkeypress = function (this: GlobalEventHandlers) {
    seen.push(`press:${this === t}`);
  };
  t.onkeyup = function (this: GlobalEventHandlers) {
    seen.push(`up:${this === t}`);
  };
  for (const type of ["keydown", "keypress", "keyup"]) t.dispatchEvent(new window.KeyboardEvent(type, { key: "a", bubbles: true }));
  assert.deepEqual(seen, ["down:a:true", "press:true", "up:true"]);
});

test("a page script can read and write the value and the caret, as ime.js insertText does", async () => {
  await mount<DpField>('<dp-field label="Phrase"><textarea id="t"></textarea></dp-field>');
  const t = document.getElementById("t") as HTMLTextAreaElement;
  t.value = "ab";
  t.selectionStart = t.selectionEnd = 1;
  const prefix = t.value.substring(0, t.selectionStart);
  const suffix = t.value.substring(t.selectionEnd);
  t.value = prefix + "שלום" + suffix;
  t.selectionStart = t.selectionEnd = t.value.length - suffix.length;
  assert.equal(t.value, "aשלוםb");
  assert.equal(t.selectionStart, 5);
});

test("composition events from the control reach listeners on the control and on the document", async () => {
  await mount<DpField>('<dp-field label="Phrase"><input id="i"></dp-field>');
  const input = document.getElementById("i") as HTMLInputElement;
  const seen: string[] = [];
  for (const type of ["compositionstart", "compositionupdate", "compositionend"]) {
    input.addEventListener(type, () => seen.push(`input:${type}`));
    document.addEventListener(type, () => seen.push(`document:${type}`));
  }
  for (const type of ["compositionstart", "compositionupdate", "compositionend"]) input.dispatchEvent(new window.CompositionEvent(type, { bubbles: true, data: "に" }));
  assert.deepEqual(seen, [
    "input:compositionstart", "document:compositionstart",
    "input:compositionupdate", "document:compositionupdate",
    "input:compositionend", "document:compositionend",
  ]);
});

test("the field changes nothing on the control except giving it an id when it has none", async () => {
  const html = '<dp-field label="Phrase" for="p"><textarea id="p" name="phrase" class="heb editor-enlarged" dir="rtl" lang="he" placeholder="x" required spellcheck="false" autocomplete="off">שלום</textarea></dp-field>';
  const before = new window.DOMParser().parseFromString(html, "text/html").querySelector("textarea")!;
  const el = await mount<DpField>(html);
  const after = document.getElementById("p") as HTMLTextAreaElement;
  const attrs = (n: Element) => Object.fromEntries(Array.from(n.attributes).map((a) => [a.name, a.value]));
  assert.deepEqual(attrs(after), attrs(before));
  assert.equal(after.value, "שלום");
  assert.equal(after.onkeydown, null);
  void el;

  const bare = await mount<DpField>('<dp-field label="No id"><input dir="rtl" lang="ar"></dp-field>');
  const input = bare.querySelector("input")!;
  assert.deepEqual(Object.keys(attrs(input)).sort(), ["dir", "id", "lang"]);
});
