import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { mount } from "../testenv.ts";
import { createClient, type DpA2uiSurface } from "./index.ts";
import { components, create, data, draw, settle, text, type Message } from "./testkit.ts";

const FIXTURES = new URL("./fixtures/", import.meta.url);

test("a Text component draws its text, as text and never as markup", async () => {
  const { el } = await draw(
    [create("s"), components("s", [{ id: "root", component: "Text", text: "<img src=x onerror=alert(1)> hi" }])],
    "s",
  );
  await settle(el);
  assert.equal(el.querySelector("img"), null);
  assert.deepEqual(text(el), ["<img src=x onerror=alert(1)> hi"]);
});

test("Column and Row nest their children in order", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Column", children: ["a", "row"] },
        { id: "a", component: "Text", text: "first" },
        { id: "row", component: "Row", children: ["b", "c"] },
        { id: "b", component: "Text", text: "second" },
        { id: "c", component: "Text", text: "third" },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.deepEqual(text(el), ["first", "second", "third"]);
  assert.ok(el.querySelector("dp-a2ui-column > dp-a2ui-row"));
});

test("weight becomes flex-grow, and a Column's align and justify become flex alignment", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Column", children: ["t"], align: "center", justify: "spaceBetween" },
        { id: "t", component: "Text", text: "x", weight: 2 },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.match(el.querySelector<HTMLElement>("dp-a2ui-text")!.style.flex, /^2\b/);
  const column = el.querySelector<HTMLElement>("dp-a2ui-column")!;
  assert.equal(column.style.alignItems, "center");
  assert.equal(column.style.justifyContent, "space-between");
});

test("a Card draws its child inside dp-card, and a Divider draws a separator", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Card", child: "col" },
        { id: "col", component: "Column", children: ["t", "d"] },
        { id: "t", component: "Text", text: "in the card" },
        { id: "d", component: "Divider" },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.deepEqual(text(el), ["in the card"]);
  assert.ok(el.querySelector("dp-card dp-a2ui-text"));
  assert.ok(el.querySelector("dp-a2ui-divider hr.horizontal"));
});

test("a bound value follows the data model, and only what depends on it changes", async () => {
  const { el, processor } = await draw(
    [
      create("s"),
      data("s", "/", { name: "Ada" }),
      components("s", [
        { id: "root", component: "Column", children: ["bound", "fixed"] },
        { id: "bound", component: "Text", text: { "@path": "/name" } },
        { id: "fixed", component: "Text", text: "static" },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.deepEqual(text(el), ["Ada", "static"]);
  const fixed = el.querySelector("dp-a2ui-text:last-child .text");
  processor.processMessages([data("s", "/name", "Grace")] as never);
  await settle(el);
  assert.deepEqual(text(el), ["Grace", "static"]);
  assert.equal(el.querySelector("dp-a2ui-text:last-child .text"), fixed, "the other Text was not redrawn");
});

test("a List with a template draws one copy per item and follows additions", async () => {
  const { el, processor } = await draw(
    [
      create("s"),
      data("s", "/", { items: [{ label: "one" }, { label: "two" }] }),
      components("s", [
        { id: "root", component: "List", children: { componentId: "row", path: "/items" } },
        { id: "row", component: "Text", text: { "@path": "label" } },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.deepEqual(text(el), ["one", "two"]);
  processor.processMessages([data("s", "/items", [{ label: "one" }, { label: "two" }, { label: "three" }])] as never);
  await settle(el);
  assert.deepEqual(text(el), ["one", "two", "three"]);
});

test("a surface with no root draws nothing until the root arrives", async () => {
  const { el, processor } = await draw([create("s")], "s");
  await settle(el);
  assert.equal(el.querySelector("*"), null);
  processor.processMessages([components("s", [{ id: "root", component: "Text", text: "late" }])] as never);
  await settle(el);
  assert.deepEqual(text(el), ["late"]);
});

test("a deleted root clears the surface", async () => {
  const { el, processor } = await draw([create("s"), components("s", [{ id: "root", component: "Text", text: "x" }])], "s");
  await settle(el);
  assert.deepEqual(text(el), ["x"]);
  processor.processMessages([{ version: "v1.0", deleteSurface: { surfaceId: "s" } }] as never);
  el.surface = undefined;
  await el.updateComplete;
  assert.equal(el.querySelector("*"), null);
});

// The reference's own examples: each must draw and show text, and must not make
// the renderer report an error to the agent.
//
// Known divergence (measured, not understood): web_core 0.14.0 reports an
// EXPRESSION_ERROR ("Either '@path' or 'path' must be provided") when `and` or
// `or` has operands that are calls to the validation functions (`required`,
// `email`, `length`, which return a ValidationResult); `returnType` does not
// help. Operands that are plain paths work. The check then counts as failed, so
// a Button with such a rule stays disabled. The reference's conformance cases
// for the same shape pass because they call the function directly, skipping
// that argument validation.
const KNOWN_ERRORS: Record<string, string[]> = {
  "09_login-form.json": ["EXPRESSION_ERROR"],
  "32_advanced-form-validator.json": ["EXPRESSION_ERROR"],
};

for (const file of readdirSync(FIXTURES).filter((name) => name.endsWith(".json"))) {
  test(`reference example ${file} draws`, async () => {
    const example = JSON.parse(readFileSync(new URL(file, FIXTURES), "utf8")) as { messages: Message[] };
    const surfaceId = (example.messages[0].createSurface as { surfaceId: string }).surfaceId;
    const sent: Record<string, unknown>[] = [];
    const client = createClient({ send: (message) => void sent.push(message) });
    await client.receive(example.messages);
    const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
    el.surface = client.processor.getSurface(surfaceId) as never;
    await settle(el);
    assert.equal(el.querySelector(".dp-a2ui-error"), null);
    assert.equal(el.querySelector(".dp-a2ui-unknown"), null);
    assert.ok(text(el).length > 0, "no text drawn");
    const codes = [...new Set(sent.filter((m) => m.error).map((m) => (m.error as { code: string }).code))];
    assert.deepEqual(codes, KNOWN_ERRORS[file] ?? [], "errors the renderer reported to the agent");
  });
}

test("a List's alignment is a class, never a style attribute (the assistant's CSP forbids inline styles)", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "List", align: "center", children: ["a"] },
        { id: "a", component: "Text", text: "A" },
      ]),
    ],
    "s",
  );
  await settle(el);
  const list = el.querySelector("dp-a2ui-list .plain")!;
  assert.ok(list.classList.contains("align-center"));
  assert.equal(list.hasAttribute("style"), false);
});

// From the reference's core/accessibility.yaml (the cases that need only the
// components drawn so far; the interactive ones come with the inputs in A3).
test("accessibility: label, description and live become ARIA attributes", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Column", children: ["t"] },
        { id: "t", component: "Text", text: "Status: Active", accessibility: { label: "Status", description: "Connection state", live: "polite" } },
      ]),
    ],
    "s",
  );
  await settle(el);
  const t = el.querySelector("dp-a2ui-text")!;
  assert.equal(t.getAttribute("aria-label"), "Status");
  assert.equal(t.getAttribute("aria-description"), "Connection state");
  assert.equal(t.getAttribute("aria-live"), "polite");
  assert.equal(t.hasAttribute("aria-hidden"), false);
});

test("accessibility: hidden hides a subtree from assistive technologies, and a binding changes it", async () => {
  const { el, processor } = await draw(
    [
      create("s"),
      data("s", "/", { gone: true, name: "Alert" }),
      components("s", [
        { id: "root", component: "Column", children: ["t"], accessibility: { hidden: true } },
        { id: "t", component: "Text", text: "x", accessibility: { label: { "@path": "/name" }, hidden: { "@path": "/gone" } } },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.equal(el.querySelector("dp-a2ui-column")!.getAttribute("aria-hidden"), "true");
  const t = el.querySelector("dp-a2ui-text")!;
  assert.equal(t.getAttribute("aria-hidden"), "true");
  assert.equal(t.getAttribute("aria-label"), "Alert");
  processor.processMessages([data("s", "/", { gone: false, name: "Fine" })] as never);
  await settle(el);
  assert.equal(t.hasAttribute("aria-hidden"), false);
  assert.equal(t.getAttribute("aria-label"), "Fine");
});

test("a component type the catalog does not have draws a visible note, and its siblings still draw", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Column", children: ["x", "t"] },
        { id: "x", component: "Hologram" },
        { id: "t", component: "Text", text: "still here" },
      ]),
    ],
    "s",
  );
  await settle(el);
  assert.match(el.querySelector(".dp-a2ui-unknown")!.textContent!, /Unknown component “Hologram”/);
  assert.deepEqual(text(el), ["still here"]);
  const root = await draw([create("r"), components("r", [{ id: "root", component: "Hologram" }])], "r");
  await settle(root.el);
  assert.ok(root.el.querySelector(".dp-a2ui-unknown"));
});
