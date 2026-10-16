import { test } from "node:test";
import assert from "node:assert/strict";
import { load, reset } from "../testenv.js";

await load("components/as-nav/component.js");

function mount(attrs) {
  reset();
  const el = document.createElement("as-nav");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  document.body.appendChild(el);
  return el;
}

const items = JSON.stringify([
  { key: "tasks", href: "#/tasks", label: "Tasks" },
  { key: "users", href: "#/users", label: "Users" },
]);
const links = (el) => [...el.querySelectorAll("a.as-nav-item")];

test("renders one link per item", () => {
  const el = mount({ items });
  assert.deepEqual(links(el).map((a) => [a.textContent, a.getAttribute("href")]), [["Tasks", "#/tasks"], ["Users", "#/users"]]);
});

test("marks only the active item and tells assistive technology", () => {
  const el = mount({ items, active: "users" });
  const active = links(el).filter((a) => a.classList.contains("active"));
  assert.equal(active.length, 1);
  assert.equal(active[0].textContent, "Users");
  assert.equal(active[0].getAttribute("aria-current"), "page");
  assert.equal(links(el)[0].hasAttribute("aria-current"), false);
});

test("an active key that matches nothing marks nothing", () => {
  assert.equal(links(mount({ items, active: "nope" })).filter((a) => a.classList.contains("active")).length, 0);
});

test("changing active or items re-renders", () => {
  const el = mount({ items, active: "tasks" });
  el.setAttribute("active", "users");
  assert.equal(links(el).find((a) => a.classList.contains("active")).textContent, "Users");
  el.setAttribute("items", JSON.stringify([{ key: "only", href: "#/only", label: "Only" }]));
  assert.deepEqual(links(el).map((a) => a.textContent), ["Only"]);
});

test("bad items render nothing and do not throw", () => {
  for (const bad of ["not json", "{}", "null", "[1,2]", '[{"href":5,"label":"x"}]', '[{"label":"no href"}]']) {
    assert.equal(links(mount({ items: bad })).length, 0, bad);
  }
  assert.equal(links(mount({})).length, 0);
});

test("labels are text, never markup", () => {
  const el = mount({ items: JSON.stringify([{ key: "x", href: "#/x", label: "<img src=x onerror=alert(1)>" }]) });
  assert.equal(el.querySelector("img"), null);
  assert.equal(links(el)[0].textContent, "<img src=x onerror=alert(1)>");
});

test("injects its stylesheet once", () => {
  reset();
  mount({ items });
  mount({ items });
  assert.equal(document.querySelectorAll("#as-nav-style").length, 1);
  assert.equal(document.getElementById("as-nav-style").getAttribute("href"), "/admin/components/as-nav/component.css");
});
