// Tests for llm.js's pure parts (kbLlm): the rows the shared tables are given and
// the query of the attempts page. The page functions need app.js and the shared
// components in a browser, and are checked there.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const kbLlm = createRequire(import.meta.url)(path.join(path.dirname(here), "llm.js"));

test("sinceFor gives the start of a period ending now, and the epoch for all time", () => {
  const now = new Date("2026-10-22T12:00:00Z");
  assert.equal(kbLlm.sinceFor("24h", now), "2026-10-21T12:00:00.000Z");
  assert.equal(kbLlm.sinceFor("7d", now), "2026-10-15T12:00:00.000Z");
  assert.equal(kbLlm.sinceFor("30d", now), "2026-09-22T12:00:00.000Z");
  assert.equal(kbLlm.sinceFor("all", now), "1970-01-01T00:00:00Z");
  assert.equal(kbLlm.sinceFor("anything else", now), "1970-01-01T00:00:00Z");
});

test("modelRows maps availability to the table's states and shows an embedding model's space as a tag", () => {
  const rows = kbLlm.modelRows([
    { id: "gemma", provider_id: "ollama", tags: ["default", "chat"], priority: 10, available: true },
    { id: "glm", provider_id: "openrouter", tags: ["default"], priority: 90, available: false, unavailable_reason: "cooldown" },
    { id: "gem", provider_id: "vertex", tags: ["default"], priority: 80, available: false, unavailable_reason: "missing_credentials" },
    { id: "bge", provider_id: "ollama", tags: ["embed"], priority: 10, embedding_space: "bge-m3", available: true },
  ]);
  assert.deepEqual(rows.map((r) => r.state), ["available", "held_out", "no_credentials", "available"]);
  assert.deepEqual(rows[0], { provider: "ollama", model: "gemma", tags: ["default", "chat"], priority: 10, state: "available" });
  assert.deepEqual(rows[3].tags, ["embed", "space bge-m3"]);
  assert.deepEqual(kbLlm.modelRows(null), []);
});

test("usageRows put the kind in the user column, count successful calls only and leave out a row with none", () => {
  const rows = kbLlm.usageRows([
    { kind: "chat", provider_id: "ollama", model_id: "gemma", calls: 5, ok_calls: 4, input_tokens: 100, output_tokens: 20, cost_usd: 0.5 },
    { kind: "embed", provider_id: "ollama", model_id: "bge", calls: 2, ok_calls: 0, input_tokens: 0, output_tokens: 0, cost_usd: 0 },
  ]);
  assert.deepEqual(rows, [{ user: "chat", provider: "ollama", model: "gemma", calls: 4, inputTokens: 100, outputTokens: 20, costUsd: 0.5 }]);
  assert.deepEqual(kbLlm.usageRows(undefined), []);
});

test("attemptRows map every column and keep the error for the badge's tooltip", () => {
  const rows = kbLlm.attemptRows([
    { started_at: "2026-10-22T10:00:00Z", kind: "translate", provider_id: "ollama", model_id: "gemma", outcome: "error", error: "boom", input_tokens: 9, output_tokens: 0, cost_usd: 0, duration_ms: 120 },
  ]);
  assert.deepEqual(rows[0], { startedAt: "2026-10-22T10:00:00Z", user: "translate", provider: "ollama", model: "gemma", outcome: "error", error: "boom", inputTokens: 9, outputTokens: 0, costUsd: 0, durationMs: 120 });
});

test("attemptsQuery pages by 25 and only sends the filters that are set", () => {
  const now = new Date("2026-10-22T12:00:00Z");
  const base = { period: "24h", model: "", kind: "", outcome: "" };
  const q1 = new URLSearchParams(kbLlm.attemptsQuery(base, 1, now));
  assert.equal(q1.get("limit"), "25");
  assert.equal(q1.get("offset"), "0");
  assert.equal(q1.get("since"), "2026-10-21T12:00:00.000Z");
  assert.deepEqual([q1.has("model_id"), q1.has("kind"), q1.has("outcome")], [false, false, false]);
  const q3 = new URLSearchParams(kbLlm.attemptsQuery({ period: "all", model: "gemma", kind: "chat", outcome: "error" }, 3, now));
  assert.equal(q3.get("offset"), "50");
  assert.equal(q3.get("model_id"), "gemma");
  assert.equal(q3.get("kind"), "chat");
  assert.equal(q3.get("outcome"), "error");
  assert.equal(new URLSearchParams(kbLlm.attemptsQuery(base, 0, now)).get("offset"), "0", "a page below 1 is page 1");
});

test("pageCount is at least one page", () => {
  assert.equal(kbLlm.pageCount(0), 1);
  assert.equal(kbLlm.pageCount(25), 1);
  assert.equal(kbLlm.pageCount(26), 2);
  assert.equal(kbLlm.pageCount(200), 8);
});

test("the options of a select mark the chosen one", () => {
  const html = kbLlm.options([["a", "Alpha"], ["b", "Beta"]], "b");
  assert.equal(html, '<option value="a">Alpha</option><option value="b" selected>Beta</option>');
});
