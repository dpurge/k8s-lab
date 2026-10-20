import { test } from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("./component.js");

const labels = {
  heading: "Attempt records", label: "Delete attempt records older than", option7: "7 days", option30: "30 days", option90: "90 days",
  optionAll: "everything", button: "Delete…", confirmDays: "Delete every attempt record older than %s days? This cannot be undone.",
  confirmAll: "Delete every attempt record? This cannot be undone.", confirmButton: "Yes, delete", cancel: "Cancel",
  hint: "Old records are also removed automatically according to the retention setting.",
};

function mount() {
  const el = document.createElement("pf-llm-purge");
  el.labels = labels;
  document.body.appendChild(el);
  const seen = [];
  el.addEventListener("pf-llm-purge-confirm", (e) => seen.push(e.detail));
  return { el, seen };
}

function choose(el, days) {
  const select = el.querySelector("select");
  select.value = String(days);
  select.dispatchEvent(new dom.window.Event("change"));
}

test("offers 7, 30, 90 days and everything, and mentions the automatic removal", () => {
  const { el } = mount();
  assert.deepEqual([...el.querySelectorAll("option")].map((o) => [o.value, o.textContent]), [
    ["7", "7 days"], ["30", "30 days"], ["90", "90 days"], ["0", "everything"],
  ]);
  assert.ok(el.querySelector(".pf-llm-hint").textContent.includes("according to the retention setting"));
  assert.equal(el.querySelector(".pf-llm-confirmation"), null, "no confirmation before Delete is pressed");
});

test("pressing Delete asks first and dispatches nothing", () => {
  const { el, seen } = mount();
  choose(el, 90);
  el.querySelector(".pf-llm-start").click();
  assert.equal(el.querySelector(".pf-llm-confirm-text").textContent, "Delete every attempt record older than 90 days? This cannot be undone.");
  assert.equal(seen.length, 0);
  assert.equal(el.querySelector("select").disabled, true, "the age cannot change while the question is open");
});

test("confirming dispatches pf-llm-purge-confirm with the chosen age", () => {
  const { el, seen } = mount();
  choose(el, 7);
  el.querySelector(".pf-llm-start").click();
  el.querySelector(".pf-llm-confirm").click();
  assert.deepEqual(seen, [{ olderThanDays: 7 }]);
  assert.equal(el.querySelector(".pf-llm-confirmation"), null);
});

test("the default age is 30 days", () => {
  const { el, seen } = mount();
  el.querySelector(".pf-llm-start").click();
  el.querySelector(".pf-llm-confirm").click();
  assert.deepEqual(seen, [{ olderThanDays: 30 }]);
});

test("everything says so in the question and sends 0", () => {
  const { el, seen } = mount();
  choose(el, 0);
  el.querySelector(".pf-llm-start").click();
  assert.equal(el.querySelector(".pf-llm-confirm-text").textContent, "Delete every attempt record? This cannot be undone.");
  el.querySelector(".pf-llm-confirm").click();
  assert.deepEqual(seen, [{ olderThanDays: 0 }]);
});

test("cancelling dispatches nothing and closes the question", () => {
  const { el, seen } = mount();
  el.querySelector(".pf-llm-start").click();
  el.querySelector(".pf-llm-cancel").click();
  assert.equal(seen.length, 0);
  assert.equal(el.querySelector(".pf-llm-confirmation"), null);
  assert.equal(el.querySelector("select").disabled, false);
});

test("changing the age after cancelling is used by the next question", () => {
  const { el, seen } = mount();
  el.querySelector(".pf-llm-start").click();
  el.querySelector(".pf-llm-cancel").click();
  choose(el, 90);
  el.querySelector(".pf-llm-start").click();
  el.querySelector(".pf-llm-confirm").click();
  assert.deepEqual(seen, [{ olderThanDays: 90 }]);
});

test("the result text is shown, and cleared when a new question opens", () => {
  const { el } = mount();
  el.result = "Deleted 12 attempt records.";
  assert.equal(el.querySelector(".pf-llm-result").textContent, "Deleted 12 attempt records.");
  el.querySelector(".pf-llm-start").click();
  assert.equal(el.querySelector(".pf-llm-result"), null);
});

test("injects its stylesheet once", () => {
  mount();
  mount();
  assert.equal(document.querySelectorAll("#pf-llm-purge-style").length, 1);
});
