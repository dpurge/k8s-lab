import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpModelTable, type ModelRow } from "./component.ts";

const row = (over: Partial<ModelRow> = {}): ModelRow => ({ provider: "gpu", model: "big", tags: ["chat", "strong"], priority: 20, state: "available", ...over });
const cells = (tr: Element) => Array.from(tr.children).map((td) => td.textContent!.trim());

async function table(rows: ModelRow[], extra = "") {
  const el = await mount<DpModelTable>(`<dp-model-table ${extra}></dp-model-table>`);
  el.rows = rows;
  await el.updateComplete;
  return el;
}

test("columns are provider, model, tags, priority and availability, in the order given", async () => {
  const el = await table([row(), row({ provider: "laptop", model: "small", tags: ["chat"], priority: 10 })]);
  const root = el.shadowRoot!;
  assert.deepEqual(Array.from(root.querySelectorAll("th")).map((th) => th.textContent!.trim()), ["Provider", "Model", "Tags", "Priority", "Availability"]);
  assert.deepEqual(Array.from(root.querySelectorAll("tbody tr")).map(cells), [
    ["gpu", "big", "chat, strong", "20", "available"],
    ["laptop", "small", "chat", "10", "available"],
  ]);
});

test("no tags is a dash", async () => {
  const el = await table([row({ tags: [] })]);
  assert.equal(cells(el.shadowRoot!.querySelector("tbody tr")!)[2], "—");
});

test("a model held out says so and when it returns; no credentials says that", async () => {
  const until = "2026-10-16T12:01:00Z";
  const el = await table([row({ state: "held_out", until }), row({ model: "cloud", state: "no_credentials" })]);
  const [held, noCreds] = Array.from(el.shadowRoot!.querySelectorAll("tbody tr"));
  const heldBadge = held.querySelector(".badge")!;
  assert.equal(heldBadge.textContent, "held out");
  assert.ok(heldBadge.classList.contains("bad"));
  assert.match(held.querySelector(".until")!.textContent!, /^until .*2026/);
  assert.equal(held.querySelector(".until")!.getAttribute("title"), until);
  assert.equal(noCreds.querySelector(".badge")!.textContent, "no credentials");
  assert.equal(noCreds.querySelector(".until"), null);
  const ok = await table([row()]);
  assert.ok(ok.shadowRoot!.querySelector(".badge")!.classList.contains("good"));
});

test("labels replace headings and availability words", async () => {
  const el = await table([row({ state: "held_out", until: "2026-10-16T12:01:00Z" })]);
  el.labels = { availability: "Disponibilité", heldOut: "écarté", until: "jusqu'à" };
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelectorAll("th")[4].textContent!.trim(), "Disponibilité");
  assert.equal(el.shadowRoot!.querySelector(".badge")!.textContent, "écarté");
  assert.match(el.shadowRoot!.querySelector(".until")!.textContent!, /^jusqu'à /);
});

test("no rows shows the empty text; names are text, not markup", async () => {
  const el = await table([], 'empty-text="No models are configured."');
  assert.equal(el.shadowRoot!.querySelector(".empty")!.textContent, "No models are configured.");
  const bad = await table([row({ model: "<img src=x onerror=alert(1)>", tags: ["<b>x</b>"] })]);
  assert.equal(bad.shadowRoot!.querySelector("img"), null);
  assert.equal(bad.shadowRoot!.querySelector("b"), null);
});
