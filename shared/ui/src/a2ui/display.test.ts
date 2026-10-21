import { test } from "node:test";
import assert from "node:assert/strict";
import { components, create, data, draw, settle, text } from "./testkit.ts";
import { ICONS } from "./icons.ts";

const one = (c: Record<string, unknown>, model: Record<string, unknown> = {}) => [
  create("s"),
  data("s", "/", model),
  components("s", [{ id: "root", ...c }]),
];

// ── Icon ─────────────────────────────────────────────────────────────────────

test("every icon name of the catalog has a path, and each draws as an svg", async () => {
  const names = Object.keys(ICONS);
  assert.equal(names.length, 59);
  for (const name of names) {
    const { el } = await draw(one({ component: "Icon", name }), "s");
    await settle(el);
    const path = el.querySelector("svg path");
    assert.ok(path, name);
    assert.equal(path.getAttribute("d"), ICONS[name]);
  }
});

test("an Icon takes its name from the data model, or its own svgPath", async () => {
  const bound = await draw(one({ component: "Icon", name: { "@path": "/icon" } }, { icon: "home" }), "s");
  await settle(bound.el);
  assert.equal(bound.el.querySelector("svg path")!.getAttribute("d"), ICONS.home);
  const own = await draw(one({ component: "Icon", name: { svgPath: "M0 0h24v24H0z" } }), "s");
  await settle(own.el);
  assert.equal(own.el.querySelector("svg path")!.getAttribute("d"), "M0 0h24v24H0z");
  assert.equal(own.el.querySelector("svg")!.getAttribute("aria-hidden"), "true");
});

test("an Icon whose bound name is not in the catalog draws a marked gap, not an error (a literal one is refused by the schema)", async () => {
  const { el } = await draw(one({ component: "Icon", name: { "@path": "/icon" } }, { icon: "teleport" }), "s");
  await settle(el);
  assert.equal(el.querySelector("svg"), null);
  assert.ok(el.querySelector(".missing"));
});

test("an svgPath is only ever a path's d attribute: markup in it stays an attribute value", async () => {
  const { el } = await draw(one({ component: "Icon", name: { svgPath: '"><script>alert(1)</script>' } }), "s");
  await settle(el);
  assert.equal(el.querySelector("script"), null);
});

// ── Tabs ─────────────────────────────────────────────────────────────────────

const TABS = [
  create("s"),
  components("s", [
    { id: "root", component: "Tabs", tabs: [{ title: "One", child: "a" }, { title: "Two", child: "b" }] },
    { id: "a", component: "Text", text: "first panel" },
    { id: "b", component: "Text", text: "second panel" },
  ]),
];

test("Tabs shows the bar and the selected tab's content, and a click switches", async () => {
  const { el } = await draw(TABS, "s");
  await settle(el);
  const bar = el.querySelector("dp-tabs")!;
  const labels = [...bar.shadowRoot!.querySelectorAll(".item")].map((n) => n.textContent!.trim());
  assert.deepEqual(labels, ["One", "Two"]);
  assert.deepEqual(text(el), ["first panel"]);
  (bar.shadowRoot!.querySelectorAll(".item")[1] as HTMLElement).click();
  await settle(el);
  assert.deepEqual(text(el), ["second panel"]);
});

// ── Modal ────────────────────────────────────────────────────────────────────

test("a Modal draws its trigger, and activating it opens the content in a dialog", async () => {
  const { el } = await draw(
    [
      create("s"),
      components("s", [
        { id: "root", component: "Modal", trigger: "open", content: "body" },
        { id: "open", component: "Button", child: "ot", action: { event: { name: "noop" } } },
        { id: "ot", component: "Text", text: "Open" },
        { id: "body", component: "Text", text: "inside the dialog" },
      ]),
    ],
    "s",
  );
  await settle(el);
  const dialog = el.querySelector("dialog")!;
  assert.equal(dialog.hasAttribute("open"), false);
  el.querySelector<HTMLElement>(".trigger")!.click();
  assert.equal(dialog.hasAttribute("open"), true);
  assert.ok(dialog.textContent!.includes("inside the dialog"));
  dialog.querySelector<HTMLElement>(".close")!.click();
  assert.equal(dialog.hasAttribute("open"), false);
});
