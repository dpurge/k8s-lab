import { test } from "node:test";
import assert from "node:assert/strict";
import { components, create, data, draw, settle } from "./testkit.ts";

const one = (c: Record<string, unknown>, model: Record<string, unknown> = {}) => [
  create("s"),
  data("s", "/", model),
  components("s", [{ id: "root", ...c }]),
];

const input = (el: HTMLElement) => el.querySelector("input, textarea") as HTMLInputElement;
const type = (control: HTMLInputElement, value: string, init: InputEventInit = {}) => {
  control.value = value;
  control.dispatchEvent(new InputEvent("input", { bubbles: true, ...init }));
};
const model = (processor: { getSurface(id: string): unknown }, path: string) =>
  (processor.getSurface("s") as { dataModel: { get(p: string): unknown } }).dataModel.get(path);

// ── TextField ────────────────────────────────────────────────────────────────

test("a TextField is a native input in the page DOM, labelled, and two-way bound", async () => {
  const { el, processor } = await draw(one({ component: "TextField", label: "Name", value: { "@path": "/name" } }, { name: "Ada" }), "s");
  await settle(el);
  const control = input(el);
  assert.equal(control.tagName, "INPUT");
  assert.equal(control.value, "Ada");
  assert.equal(control.getRootNode(), document, "the control is in the page's own DOM, not a shadow root");
  assert.equal(el.querySelector("dp-field")!.querySelector("label")!.textContent, "Name");
  type(control, "Grace");
  assert.equal(model(processor, "/name"), "Grace");
  processor.processMessages([data("s", "/name", "Hopper")] as never);
  await settle(el);
  assert.equal(input(el).value, "Hopper");
  assert.equal(input(el), control, "the control is kept, so focus and the caret stay");
});

test("a TextField variant picks the control: longText is a textarea, number and obscured set the type", async () => {
  const long = await draw(one({ component: "TextField", label: "L", variant: "longText", value: { "@path": "/t" } }, { t: "x" }), "s");
  await settle(long.el);
  assert.equal(long.el.querySelector("textarea")!.value, "x");
  for (const [variant, type] of [["number", "number"], ["obscured", "password"], ["shortText", "text"]]) {
    const { el } = await draw(one({ component: "TextField", label: "L", variant, value: { "@path": "/t" } }), "s");
    await settle(el);
    assert.equal(input(el).type, type, variant);
  }
});

test("input methods: nothing is written to the model while composing, the composed text is on compositionend", async () => {
  for (const composed of ["你好", "日本語", "שלום"]) {
    const { el, processor } = await draw(one({ component: "TextField", label: "L", value: { "@path": "/t" } }, { t: "" }), "s");
    await settle(el);
    const control = input(el);
    type(control, composed.slice(0, 1), { isComposing: true });
    type(control, composed, { isComposing: true });
    assert.equal(model(processor, "/t"), "", `${composed}: still composing`);
    control.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: composed }));
    assert.equal(model(processor, "/t"), composed);
    await settle(el);
    assert.equal(input(el).value, composed, "the control keeps what was composed");
  }
});

test("a re-render for another reason does not touch a field that is being composed", async () => {
  const { el, processor } = await draw(
    [
      create("s"),
      data("s", "/", { t: "", other: "a" }),
      components("s", [
        { id: "root", component: "Column", children: ["f", "o"] },
        { id: "f", component: "TextField", label: "L", value: { "@path": "/t" } },
        { id: "o", component: "Text", text: { "@path": "/other" } },
      ]),
    ],
    "s",
  );
  await settle(el);
  const control = input(el);
  type(control, "に", { isComposing: true });
  processor.processMessages([data("s", "/other", "b")] as never);
  await settle(el);
  assert.equal(control.value, "に", "the half-composed text survived the update");
});

// ── CheckBox, Slider, DateTimeInput ──────────────────────────────────────────

test("a CheckBox is bound both ways to a boolean", async () => {
  const { el, processor } = await draw(one({ component: "CheckBox", label: "Agree", value: { "@path": "/ok" } }, { ok: false }), "s");
  await settle(el);
  const box = input(el);
  assert.equal(box.type, "checkbox");
  assert.equal(box.checked, false);
  assert.equal(el.querySelector("label")!.textContent!.trim(), "Agree");
  box.checked = true;
  box.dispatchEvent(new Event("change", { bubbles: true }));
  assert.equal(model(processor, "/ok"), true);
  await settle(el);
  processor.processMessages([data("s", "/ok", false)] as never);
  await settle(el);
  assert.equal(input(el).checked, false);
});

test("a Slider is a range input with the bounds, steps as equal divisions, and a number in the model", async () => {
  const { el, processor } = await draw(one({ component: "Slider", label: "Volume", min: 0, max: 50, steps: 5, value: { "@path": "/v" } }, { v: 10 }), "s");
  await settle(el);
  const range = input(el);
  assert.equal(range.type, "range");
  assert.equal(range.min, "0");
  assert.equal(range.max, "50");
  assert.equal(range.step, "10");
  assert.equal(range.value, "10");
  assert.equal(el.querySelector("output")!.textContent, "10");
  type(range, "30");
  assert.equal(model(processor, "/v"), 30);
});

test("a DateTimeInput maps the model's ISO text to the control and back", async () => {
  const dt = await draw(one({ component: "DateTimeInput", label: "When", enableDate: true, enableTime: true, value: { "@path": "/d" } }, { d: "2026-09-04T14:30:00" }), "s");
  await settle(dt.el);
  assert.equal(input(dt.el).type, "datetime-local");
  assert.equal(input(dt.el).value, "2026-09-04T14:30");
  input(dt.el).value = "2026-10-01T08:15";
  input(dt.el).dispatchEvent(new Event("change", { bubbles: true }));
  assert.equal(model(dt.processor, "/d"), "2026-10-01T08:15:00");

  const date = await draw(one({ component: "DateTimeInput", enableDate: true, value: { "@path": "/d" } }, { d: "2026-09-04" }), "s");
  await settle(date.el);
  assert.equal(input(date.el).type, "date");
  assert.equal(input(date.el).value, "2026-09-04");

  const time = await draw(one({ component: "DateTimeInput", enableTime: true, value: { "@path": "/d" } }, { d: "14:30" }), "s");
  await settle(time.el);
  assert.equal(input(time.el).type, "time");
  assert.equal(input(time.el).value, "14:30");
});

// ── ChoicePicker ─────────────────────────────────────────────────────────────

const OPTIONS = [
  { label: "One", value: "1" },
  { label: "Two", value: "2" },
  { label: "Three", value: "3" },
];

test("a ChoicePicker (mutually exclusive) is a radio group that holds one value", async () => {
  const { el, processor } = await draw(one({ component: "ChoicePicker", label: "Pick", options: OPTIONS, value: { "@path": "/c" } }, { c: ["2"] }), "s");
  await settle(el);
  const radios = [...el.querySelectorAll<HTMLInputElement>("input[type=radio]")];
  assert.deepEqual(radios.map((r) => r.checked), [false, true, false]);
  assert.equal(el.querySelector("[role=radiogroup]") !== null, true);
  radios[2].checked = true;
  radios[2].dispatchEvent(new Event("change", { bubbles: true }));
  assert.deepEqual(model(processor, "/c"), ["3"]);
});

test("a ChoicePicker (multiple selection) adds and removes values", async () => {
  const { el, processor } = await draw(
    one({ component: "ChoicePicker", variant: "multipleSelection", options: OPTIONS, value: { "@path": "/c" } }, { c: ["1"] }),
    "s",
  );
  await settle(el);
  const boxes = [...el.querySelectorAll<HTMLInputElement>("input[type=checkbox]")];
  boxes[1].checked = true;
  boxes[1].dispatchEvent(new Event("change", { bubbles: true }));
  assert.deepEqual(model(processor, "/c"), ["1", "2"]);
  await settle(el);
  boxes[0].checked = false;
  boxes[0].dispatchEvent(new Event("change", { bubbles: true }));
  assert.deepEqual(model(processor, "/c"), ["2"]);
});

test("a filterable ChoicePicker shows only the options that match, and chips are a display style", async () => {
  const { el } = await draw(
    one({ component: "ChoicePicker", displayStyle: "chips", filterable: true, options: OPTIONS, value: { "@path": "/c" } }, { c: [] }),
    "s",
  );
  await settle(el);
  assert.ok(el.querySelector(".picker.chips"));
  assert.equal(el.querySelectorAll(".option").length, 3);
  type(el.querySelector<HTMLInputElement>("input.filter")!, "tw");
  await settle(el);
  assert.deepEqual([...el.querySelectorAll(".option")].map((o) => o.textContent!.trim()), ["Two"]);
});

// ── Button, actions and checks ───────────────────────────────────────────────

const form = (extra: Record<string, unknown> = {}) => [
  create("s"),
  data("s", "/", { email: "" }),
  components("s", [
    { id: "root", component: "Column", children: ["email", "send"] },
    {
      id: "email",
      component: "TextField",
      label: "Email",
      value: { "@path": "/email" },
      checks: [{ condition: { "@call": "required", args: { value: { "@path": "/email" } } }, message: "Email is required" }],
    },
    { id: "send", component: "Button", child: "label", variant: "primary", action: { event: { name: "send", context: { email: { "@path": "/email" } } } }, ...extra },
    { id: "label", component: "Text", text: "Send" },
  ]),
];

test("a Button's event action sends the action with its context resolved against the data model", async () => {
  const { el, processor, sent } = await draw(form(), "s");
  await settle(el);
  type(input(el), "ada@example.com");
  await settle(el);
  el.querySelector<HTMLElement>("dp-a2ui-button dp-button")!.click();
  await settle(el);
  assert.equal(sent.length, 1);
  const message = sent[0] as { name: string; surfaceId: string; sourceComponentId: string; context: unknown };
  assert.equal(message.name, "send");
  assert.equal(message.surfaceId, "s");
  assert.equal(message.sourceComponentId, "send");
  assert.deepEqual(message.context, { email: "ada@example.com" });
  assert.equal(model(processor, "/email"), "ada@example.com");
});

test("a failed check shows its message under the field and disables a Button that has the same rule", async () => {
  const { el } = await draw(
    form({ checks: [{ condition: { "@call": "required", args: { value: { "@path": "/email" } } }, message: "Fill in the email first" }] }),
    "s",
  );
  await settle(el);
  // In v1.0 the function's own ValidationResult message wins; the rule's
  // `message` is the fallback for a function that returns a plain boolean.
  assert.match(el.querySelector(".errors")!.textContent!, /This field is required/);
  assert.equal(input(el).getAttribute("aria-invalid"), "true");
  const button = el.querySelector<HTMLElement>("dp-a2ui-button dp-button")!;
  assert.equal(button.hasAttribute("disabled"), true);
  assert.match(button.getAttribute("title")!, /This field is required/);
});

test("a disabled Button sends nothing, and typing a value enables it and clears the message", async () => {
  const { el, sent } = await draw(
    form({ checks: [{ condition: { "@call": "required", args: { value: { "@path": "/email" } } }, message: "needed" }] }),
    "s",
  );
  await settle(el);
  el.querySelector<HTMLElement>("dp-a2ui-button dp-button")!.click();
  assert.deepEqual(sent, []);
  type(input(el), "a@b.co");
  await settle(el);
  assert.equal(el.querySelector(".errors"), null);
  assert.equal(el.querySelector("dp-a2ui-button dp-button")!.hasAttribute("disabled"), false);
  el.querySelector<HTMLElement>("dp-a2ui-button dp-button")!.click();
  assert.equal(sent.length, 1);
});

test("a function-call action runs in the renderer and sends nothing to the agent", async () => {
  const { el, sent } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Button", child: "t", action: { functionCall: { call: "formatNumber", args: { value: 1 } } } },
        { id: "t", component: "Text", text: "Go" },
      ]),
    ],
    "s",
  );
  await settle(el);
  el.querySelector<HTMLElement>("dp-a2ui-button dp-button")!.click();
  await settle(el);
  assert.deepEqual(sent, []);
});
