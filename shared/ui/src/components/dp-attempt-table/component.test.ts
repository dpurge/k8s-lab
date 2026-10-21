import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpAttemptTable, type AttemptRow } from "./component.ts";

const row = (over: Partial<AttemptRow> = {}): AttemptRow => ({ startedAt: "2026-10-16T12:00:00Z", user: "Ada", userId: "u-1", provider: "gpu", model: "big", outcome: "ok", error: null, inputTokens: 1500, outputTokens: 30, costUsd: 12.3456, durationMs: 90000, ...over });
const cells = (tr: Element) => Array.from(tr.children).map((td) => td.textContent!.trim());

async function table(rows: AttemptRow[], extra = "") {
  const el = await mount<DpAttemptTable>(`<dp-attempt-table ${extra}></dp-attempt-table>`);
  el.rows = rows;
  await el.updateComplete;
  return el;
}

test("one row per attempt with the nine columns", async () => {
  const el = await table([row(), row({ outcome: "timeout", error: "deadline", inputTokens: 0, outputTokens: 0, costUsd: 0, durationMs: 500 })]);
  const root = el.shadowRoot!;
  assert.deepEqual(Array.from(root.querySelectorAll("th")).map((th) => th.textContent!.trim()), ["Started", "User", "Provider", "Model", "Outcome", "Input tokens", "Output tokens", "Cost", "Duration"]);
  const [ok, failed] = Array.from(root.querySelectorAll("tbody tr")).map(cells);
  assert.match(ok[0], /Oct 1[56]/);
  assert.doesNotMatch(ok[0], /2026/, "no year in the cell");
  assert.deepEqual(ok.slice(1), ["Ada", "gpu", "big", "ok", "1,500", "30", "$12.35", "1 min 30 s"]);
  assert.deepEqual(failed.slice(1), ["Ada", "gpu", "big", "timeout", "0", "0", "$0.00", "0.5 s"]);
});

test("the full timestamp is the started cell's tooltip; the error is the outcome badge's", async () => {
  const el = await table([row(), row({ outcome: "timeout", error: "deadline" })]);
  const trs = el.shadowRoot!.querySelectorAll("tbody tr");
  assert.equal(trs[0].querySelector("td")!.getAttribute("title"), "2026-10-16T12:00:00Z");
  assert.ok(trs[0].querySelector(".badge")!.classList.contains("good"));
  assert.equal(trs[1].querySelector(".badge")!.getAttribute("title"), "deadline");
  assert.ok(trs[1].querySelector(".badge")!.classList.contains("bad"));
});

test("labels replace headings by key", async () => {
  const el = await table([row()]);
  el.labels = { started: "Début", duration: "Durée" };
  await el.updateComplete;
  const heads = Array.from(el.shadowRoot!.querySelectorAll("th")).map((th) => th.textContent!.trim());
  assert.equal(heads[0], "Début");
  assert.equal(heads[8], "Durée");
  assert.equal(heads[1], "User");
});

test("no rows shows the empty text; names are text, not markup", async () => {
  const el = await table([], 'empty-text="No model calls in this period."');
  assert.equal(el.shadowRoot!.querySelector("table"), null);
  assert.equal(el.shadowRoot!.querySelector(".empty")!.textContent, "No model calls in this period.");
  const bad = await table([row({ user: "<b>Ada</b>", model: "<img src=x onerror=alert(1)>" })]);
  assert.equal(bad.shadowRoot!.querySelector("img"), null);
  assert.equal(bad.shadowRoot!.querySelector("b"), null);
});
