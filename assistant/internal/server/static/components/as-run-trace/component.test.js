import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, step } from "../testenv.js";

await load("format.js", "api.js", "components/as-run-trace/component.js");

afterEach(() => reset());

function mount(steps, attrs = {}) {
  reset();
  const el = document.createElement("as-run-trace");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  el.steps = steps;
  document.body.appendChild(el);
  return el;
}

const rows = (el) => [...el.querySelectorAll("tbody tr.as-trace-row")];
const cells = (tr) => [...tr.children].map((td) => td.textContent);

test("one row per attempt with the state, the action, the outcome, tokens, time and error", () => {
  const el = mount([
    step(),
    step({ id: 2, step: 2, state: "plan", action: { type: "goto", state: "assess" }, tool: null, tool_result: null, input_tokens: 1200, output_tokens: 30, duration_ms: 12400 }),
    step({ id: 3, step: 3, state: "assess", action: { type: "ask_user", payload: { question: "?" } }, tool: null, tool_result: null }),
  ]);
  assert.deepEqual(rows(el).map(cells), [
    ["1", "intake", 'tool memory_search "learner"', "ok", "900 / 60", "2.5 s", "—"],
    ["2", "plan", "go to assess", "ok", "1,200 / 30", "12 s", "—"],
    ["3", "assess", "ask the user", "ok", "900 / 60", "2.5 s", "—"],
  ]);
  assert.deepEqual([...el.querySelectorAll("thead th")].map((th) => th.textContent), ["Step", "State", "Action", "Outcome", "Tokens in / out", "Time", "Error"]);
});

test("retries are numbered step.attempt and a refused reply shows its error in red, open", () => {
  const el = mount([
    step({ id: 1, outcome: "retry", error: "the reply is not valid JSON: unexpected end of input", action: null, patch: null, tool: null, tool_result: null, reply: '{"state_patch":{' }),
    step({ id: 2, attempt: 2 }),
  ]);
  const [bad, good] = rows(el);
  assert.equal(cells(bad)[0], "1");
  assert.equal(cells(good)[0], "1.2");
  assert.equal(cells(bad)[2], "no valid action");
  assert.equal(cells(bad)[6], "the reply is not valid JSON: unexpected end of input");
  assert.ok(bad.querySelector(".as-trace-error").classList.contains("has-error"));
  assert.equal(good.querySelector(".as-trace-error").classList.contains("has-error"), false, "the dash of a good step is not red");
  assert.equal(bad.querySelector(".as-badge").className, "as-badge as-outcome-retry");
  const more = el.querySelectorAll("tr.as-trace-more");
  assert.equal(more[0].hidden, false, "a step that went wrong is shown open");
  assert.match(more[0].textContent, /\{"state_patch":\{/);
  assert.equal(more[1].hidden, true);
});

test("clicking a row opens and closes what the model replied, the patch and the tool result", () => {
  const el = mount([step()]);
  const [tr] = rows(el);
  const more = el.querySelector("tr.as-trace-more");
  assert.equal(more.hidden, true);
  tr.click();
  assert.equal(more.hidden, false);
  assert.match(more.textContent, /Model reply/);
  assert.match(more.textContent, /"goal": "Arabic"/);
  assert.match(more.textContent, /ok: 0 memories/);
  tr.click();
  assert.equal(more.hidden, true);
});

test("a row can be opened from the keyboard", () => {
  const el = mount([step()]);
  const [tr] = rows(el);
  tr.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  assert.equal(el.querySelector("tr.as-trace-more").hidden, false);
  assert.equal(tr.tabIndex, 0);
});

test("a failure without a model call is shown as a failure, with its reason", () => {
  const el = mount([step({ id: 9, step: 7, attempt: 0, outcome: "failed", error: "max_steps: the run used its 40 steps", action: null, patch: null, tool: null, tool_result: null, reply: null, input_tokens: 0, output_tokens: 0, duration_ms: 0 })]);
  const [tr] = rows(el);
  assert.deepEqual(cells(tr), ["7!", "intake", "run failed", "failed", "—", "—", "max_steps: the run used its 40 steps"]);
  assert.equal(tr.querySelector(".as-badge").className, "as-badge as-outcome-failed");
});

test("no steps says so and shows no table", () => {
  const el = mount([]);
  assert.equal(el.querySelector(".as-trace-message").textContent, "No steps yet.");
  assert.equal(el.querySelector(".as-trace-table-wrap").hidden, true);
});

test("an error attribute replaces the table with the message, in red", () => {
  const el = mount([step()], { error: "Could not load the trace: internal error" });
  assert.equal(el.querySelector(".as-trace-message").textContent, "Could not load the trace: internal error");
  assert.ok(el.querySelector(".as-trace-message").classList.contains("error"));
  assert.equal(el.querySelector(".as-trace-table-wrap").hidden, true);
  el.removeAttribute("error");
  assert.equal(rows(el).length, 1);
  assert.equal(el.querySelector(".as-trace-message").classList.contains("error"), false);
});

test("setting the steps again replaces the rows", () => {
  const el = mount([step(), step({ id: 2, step: 2 })]);
  el.steps = [step({ id: 3, step: 3 })];
  assert.deepEqual(rows(el).map((tr) => cells(tr)[0]), ["3"]);
  el.steps = null;
  assert.equal(rows(el).length, 0);
});

test("replies, errors and tool results reach the page as text, never as markup", () => {
  const evil = "<img src=x onerror=alert(1)>";
  const el = mount([step({ error: evil, reply: evil, tool_result: evil, state: evil, action: { type: "tool", tool: evil } })]);
  assert.equal(el.querySelector("img"), null);
  assert.equal(cells(rows(el)[0])[1], evil);
  assert.equal(cells(rows(el)[0])[6], evil);
});

test("a step with nothing more recorded says so when opened", () => {
  const el = mount([step({ reply: null, patch: null, tool_result: null, action: null, tool: null })]);
  rows(el)[0].click();
  assert.match(el.querySelector("tr.as-trace-more").textContent, /Nothing more was recorded/);
});

test("a delegation is a row of its own, and the steps of the workflow it handed to are marked and indented", () => {
  const el = mount([
    step({ id: 1, step: 1, state: "route", workflow_id: "orchestrator", action: { type: "delegate", workflow: "hello", message: "hi", reason: "a greeting" }, tool: null, tool_result: null }),
    step({ id: 2, step: 2, state: "ask", workflow_id: "hello", depth: 1, action: { type: "ask_user", payload: { question: "?" } }, tool: null, tool_result: null }),
    step({ id: 3, step: 3, state: "greet", workflow_id: "deeper", depth: 2, action: { type: "goto", state: "done" }, tool: null, tool_result: null }),
  ]);
  const got = rows(el).map(cells);
  assert.deepEqual(got.map((r) => r[1]), ["route", "↳ hello · ask", "↳ ↳ deeper · greet"]);
  assert.equal(got[0][2], "hand over to hello (a greeting)");
});
