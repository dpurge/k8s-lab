import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpUsageTable, grouped, type UsageRow } from "./component.ts";

const row = (over: Partial<UsageRow> = {}): UsageRow => ({ user: "Ada", userId: "u-1", provider: "gpu", model: "big", calls: 1200, inputTokens: 1500000, outputTokens: 30000, costUsd: 12.3456, ...over });
const cells = (tr: Element) => Array.from(tr.children).map((td) => td.textContent!.trim());

async function table(rows: UsageRow[], extra = "") {
  const el = await mount<DpUsageTable>(`<dp-usage-table ${extra}></dp-usage-table>`);
  el.rows = rows;
  await el.updateComplete;
  return el;
}

test("one row per entry with the seven columns and formatted figures", async () => {
  const el = await table([row(), row({ user: "Bob", provider: "cloud", model: "small", calls: 1, inputTokens: 10, outputTokens: 2, costUsd: 0.5 })]);
  const root = el.shadowRoot!;
  assert.deepEqual(Array.from(root.querySelectorAll("th")).map((th) => th.textContent!.trim()), ["User", "Provider", "Model", "Calls", "Input tokens", "Output tokens", "Cost"]);
  assert.deepEqual(Array.from(root.querySelectorAll("tbody tr")).map(cells), [
    ["Ada", "gpu", "big", "1,200", "1,500,000", "30,000", "$12.35"],
    ["Bob", "cloud", "small", "1", "10", "2", "$0.5000"],
  ]);
});

test("a null user, provider or model is a dash, and the user's id is the tooltip", async () => {
  const el = await table([row({ user: null, provider: null })]);
  const tds = el.shadowRoot!.querySelectorAll("tbody td");
  assert.equal(tds[0].textContent!.trim(), "—");
  assert.equal(tds[1].textContent!.trim(), "—");
  const named = await table([row()]);
  assert.equal(named.shadowRoot!.querySelector("td")!.getAttribute("title"), "u-1");
});

test("labels replace headings by key", async () => {
  const el = await table([row()]);
  el.labels = { user: "Utilisateur", cost: "Coût" };
  await el.updateComplete;
  const heads = Array.from(el.shadowRoot!.querySelectorAll("th")).map((th) => th.textContent!.trim());
  assert.equal(heads[0], "Utilisateur");
  assert.equal(heads[6], "Coût");
  assert.equal(heads[1], "Provider");
});

test("no rows shows the empty text and no table; with no empty text, nothing", async () => {
  const el = await table([], 'empty-text="No model calls in this period."');
  assert.equal(el.shadowRoot!.querySelector("table"), null);
  assert.equal(el.shadowRoot!.querySelector(".empty")!.textContent, "No model calls in this period.");
  const bare = await table([]);
  assert.equal(bare.shadowRoot!.querySelector("table"), null);
  assert.equal(bare.shadowRoot!.querySelector(".empty"), null);
});

test("names reach the page as text, never as markup", async () => {
  const el = await table([row({ user: "<b>Ada</b>", model: "<img src=x onerror=alert(1)>" })]);
  assert.equal(el.shadowRoot!.querySelector("img"), null);
  assert.equal(el.shadowRoot!.querySelector("b"), null);
  assert.equal(el.shadowRoot!.querySelector("td")!.textContent!.trim(), "<b>Ada</b>");
});

const FINE = [
  row({ calls: 2, inputTokens: 100, outputTokens: 10, costUsd: 3 }),
  row({ provider: "cloud", calls: 1, inputTokens: 50, outputTokens: 5, costUsd: 2 }),
  row({ user: "Bob", userId: "u-2", model: "small", calls: 4, inputTokens: 40, outputTokens: 4, costUsd: 0.5 }),
];
const shown = (el: DpUsageTable) => Array.from(el.shadowRoot!.querySelectorAll("tbody tr")).map(cells);

test("grouping by user sums the rows and leaves the other names as dashes", async () => {
  const el = await table(FINE, 'group-by="user"');
  assert.deepEqual(shown(el), [
    ["Ada", "—", "—", "3", "150", "15", "$5.00"],
    ["Bob", "—", "—", "4", "40", "4", "$0.5000"],
  ]);
});

test("grouping by provider and by model, highest cost first", async () => {
  const byProvider = await table(FINE, 'group-by="provider"');
  assert.deepEqual(shown(byProvider).map((c) => [c[1], c[3], c[6]]), [["gpu", "6", "$3.50"], ["cloud", "1", "$2.00"]]);
  const byModel = await table(FINE, 'group-by="model"');
  assert.deepEqual(shown(byModel).map((c) => [c[2], c[3], c[6]]), [["big", "3", "$5.00"], ["small", "4", "$0.5000"]]);
});

test("rows are told apart by user id, so two users with one name stay two", () => {
  const twins = [row({ userId: "u-1", calls: 1 }), row({ userId: "u-9", calls: 2 })];
  assert.equal(grouped(twins, "user").length, 2);
  assert.equal(grouped(twins, "provider").length, 1);
});

test("rows with no user are one group, and no or unknown grouping leaves the rows alone", () => {
  const none = [row({ user: null, userId: undefined, calls: 1 }), row({ user: null, userId: undefined, calls: 2 })];
  assert.equal(grouped(none, "user").length, 1);
  assert.equal(grouped(none, "user")[0].calls, 3);
  assert.equal(grouped(FINE, ""), FINE);
  assert.equal(grouped(FINE, "x" as never), FINE);
});

test("changing groupBy redraws without new rows", async () => {
  const el = await table(FINE);
  assert.equal(shown(el).length, 3);
  el.groupBy = "model";
  await el.updateComplete;
  assert.equal(shown(el).length, 2);
});
