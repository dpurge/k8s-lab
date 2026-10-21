import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, task, call, step, user, USER_ID } from "../testenv.js";

await load("format.js", "api.js", "components/as-task-actions/component.js", "components/as-answer-box/component.js", "components/as-run-trace/component.js", "components/as-task-detail/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const ID = "11111111-2222-4333-8444-555555555555";

function api({ t = task(), users = [user(USER_ID, "Ada")], taskStatus = 200, taskBody, calls = [], callsStatus = 200, steps = [], stepsStatus = 200 } = {}) {
  return (url) => {
    if (url.startsWith("/api/v1/users")) return jsonResponse(200, { items: users });
    if (url.endsWith("/steps")) {
      return stepsStatus === 200 ? jsonResponse(200, { items: steps }) : jsonResponse(stepsStatus, { error: { code: "internal", message: "internal error" } });
    }
    if (url.endsWith("/llm-calls")) {
      return callsStatus === 200 ? jsonResponse(200, { items: calls }) : jsonResponse(callsStatus, { error: { code: "internal", message: "internal error" } });
    }
    if (url.startsWith("/api/v1/tasks/")) {
      if (taskStatus === 404) return jsonResponse(404, { error: { code: "not_found", message: "task not found" } });
      if (taskStatus !== 200) return jsonResponse(taskStatus, { error: { code: "internal", message: "internal error" } });
      return jsonResponse(200, taskBody || t);
    }
    return jsonResponse(404, { error: { message: "no such route" } });
  };
}

async function mount(attrs = {}, handler = api()) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-task-detail");
  el.setAttribute("task-id", ID);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

const fields = (el) => {
  const out = {};
  const dts = [...el.querySelectorAll("dt")];
  for (const dt of dts) out[dt.textContent] = dt.nextElementSibling;
  return out;
};
const taskCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/tasks/") && !u.endsWith("/llm-calls") && !u.endsWith("/steps") && !u.endsWith("/messages") && !u.endsWith("/cancel"));
const callCalls = (calls) => calls.filter((u) => u.endsWith("/llm-calls"));

test("shows every field of the task", async () => {
  const { el } = await mount({}, api({ t: task({ workflow_id: "learn-topic", source: "telegram", queue_seconds: 2, execution_seconds: 65 }) }));
  const f = fields(el);
  assert.equal(f.ID.textContent, ID);
  assert.equal(f.User.textContent, "Ada");
  assert.equal(f.Workflow.textContent, "learn-topic");
  assert.equal(f.Source.textContent, "telegram");
  assert.equal(f.Status.textContent, "succeeded");
  assert.equal(f.Status.querySelector(".as-badge").className, "as-badge as-status-succeeded");
  assert.match(f.Created.textContent, /2026/);
  assert.match(f.Started.textContent, /2026/);
  assert.match(f.Finished.textContent, /2026/);
  assert.equal(f["Queue time"].textContent, "2.0 s");
  assert.equal(f["Execution time"].textContent, "1 min 05 s");
});

test("asks for the task by its id, encoded", async () => {
  const { calls } = await mount();
  assert.deepEqual(taskCalls(calls), [`/api/v1/tasks/${ID}`]);
  const { calls: weird } = await mount({ "task-id": "a/b c?d" });
  assert.deepEqual(taskCalls(weird), ["/api/v1/tasks/a%2Fb%20c%3Fd"]);
});

test("the user's id is the tooltip of their name, and the fallback if the name is unknown", async () => {
  const { el } = await mount();
  assert.equal(fields(el).User.title, USER_ID);
  const { el: lonely } = await mount({}, api({ users: [] }));
  assert.equal(fields(lonely).User.textContent, "aaaaaaaa");
});

test("payload and result are shown as indented JSON", async () => {
  const { el } = await mount({}, api({ t: task({ payload: { a: 1, nested: { b: [1, 2] } }, result: { ok: true } }) }));
  const pres = [...el.querySelectorAll("pre.as-task-detail-json")].map((p) => p.textContent);
  assert.equal(pres[0], JSON.stringify({ a: 1, nested: { b: [1, 2] } }, null, 2));
  assert.equal(pres[1], JSON.stringify({ ok: true }, null, 2));
});

test("a task without a result shows a dash, and one without an error shows no error section", async () => {
  const { el } = await mount({}, api({ t: task({ result: null, error: null, status: "pending", started_at: null, finished_at: null, execution_seconds: null }) }));
  assert.deepEqual([...el.querySelectorAll(".as-task-detail-none")].map((n) => n.textContent), ["—", "No model calls."]);
  assert.equal(el.querySelector("pre.as-task-detail-error"), null);
  assert.equal(fields(el).Started.textContent, "—");
  assert.equal(fields(el)["Execution time"].textContent, "—");
});

test("a failed task shows its error", async () => {
  const { el } = await mount({}, api({ t: task({ status: "failed", result: null, error: "unknown workflow: nope" }) }));
  const error = el.querySelector("pre.as-task-detail-error");
  assert.equal(error.textContent, "unknown workflow: nope");
  assert.equal(fields(el).Status.querySelector(".as-badge").className, "as-badge as-status-failed");
});

test("an error whose text is empty is still shown as an error section", async () => {
  const { el } = await mount({}, api({ t: task({ status: "failed", error: "" }) }));
  assert.ok(el.querySelector("pre.as-task-detail-error"));
});

test("a pending task counts its queue time up; a running one its execution time", async () => {
  const { el: pending } = await mount({}, api({ t: task({ status: "pending", started_at: null, finished_at: null, queue_seconds: 30, execution_seconds: null }) }));
  assert.equal(fields(pending)["Queue time"].textContent, "30 s…");
  assert.ok(fields(pending)["Queue time"].classList.contains("as-live"));
  const { el: running } = await mount({}, api({ t: task({ status: "running", finished_at: null, queue_seconds: 2, execution_seconds: 5 }) }));
  assert.equal(fields(running)["Queue time"].textContent, "2.0 s");
  assert.equal(fields(running)["Execution time"].textContent, "5.0 s…");
});

test("has a link back to the list", async () => {
  const { el } = await mount();
  const back = el.querySelector("a.as-task-detail-back");
  assert.equal(back.getAttribute("href"), "#/tasks");
});

test("a task that does not exist says so, offers the way back, and does not retry", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ "refresh-ms": 1000 }, api({ taskStatus: 404 }));
  assert.equal(el.querySelector(".as-task-detail-error").textContent, "Task not found.");
  assert.ok(el.querySelector("a.as-task-detail-back"));
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("a server error is shown with its message and retried", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ "refresh-ms": 1000 }, api({ taskStatus: 500 }));
  assert.equal(el.querySelector(".as-task-detail-error").textContent, "Could not load task: internal error");
  mock.timers.tick(1000);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
});

test("a network failure is shown with its reason", async () => {
  const { el } = await mount({}, () => {
    throw new TypeError("Failed to fetch");
  });
  assert.equal(el.querySelector(".as-task-detail-error").textContent, "Could not load task: Failed to fetch");
});

// ── live refresh ─────────────────────────────────────────────────────────────

test("a pending or running task is reloaded at the interval until it finishes", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const states = [
    task({ status: "pending", started_at: null, finished_at: null, queue_seconds: 1, execution_seconds: null }),
    task({ status: "running", finished_at: null, queue_seconds: 2, execution_seconds: 1 }),
    task({ status: "succeeded", queue_seconds: 2, execution_seconds: 3 }),
  ];
  let fetched = 0;
  const { el, calls } = await mount({ "refresh-ms": 1000 }, (url) =>
    url.startsWith("/api/v1/tasks/") ? jsonResponse(200, states[Math.min(fetched++, 2)]) : api()(url));
  assert.equal(taskCalls(calls).length, 1);
  assert.equal(fields(el).Status.textContent, "pending");
  mock.timers.tick(1000);
  await flush();
  assert.equal(fields(el).Status.textContent, "running");
  mock.timers.tick(1000);
  await flush();
  assert.equal(fields(el).Status.textContent, "succeeded");
  const done = taskCalls(calls).length;
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, done, "a finished task is not reloaded");
});

test("a finished task is not reloaded at all", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 500 });
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("refresh-ms 0 turns refreshing off", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 0 }, api({ t: task({ status: "running", finished_at: null }) }));
  mock.timers.tick(60000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("removing the element stops the refreshing", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ "refresh-ms": 1000 }, api({ t: task({ status: "running", finished_at: null }) }));
  el.remove();
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("changing the task-id loads the other task", async () => {
  const { el, calls } = await mount();
  el.setAttribute("task-id", "99999999-2222-4333-8444-555555555555");
  await flush();
  assert.equal(taskCalls(calls).at(-1), "/api/v1/tasks/99999999-2222-4333-8444-555555555555");
});

test("a slow response for the task shown before is ignored", async () => {
  reset();
  const pending = [];
  mockFetch((url) => {
    if (url.startsWith("/api/v1/users")) return jsonResponse(200, { items: [user(USER_ID, "Ada")] });
    if (url.endsWith("/llm-calls") || url.endsWith("/steps")) return jsonResponse(200, { items: [] });
    return new Promise((resolve) => pending.push(resolve));
  });
  const el = document.createElement("as-task-detail");
  el.setAttribute("task-id", ID);
  document.body.appendChild(el);
  await flush();
  el.setAttribute("task-id", "99999999-2222-4333-8444-555555555555");
  await flush();
  pending[1](jsonResponse(200, task({ id: "99999999-2222-4333-8444-555555555555", workflow_id: "newer" })));
  await flush();
  pending[0](jsonResponse(200, task({ workflow_id: "older" })));
  await flush();
  assert.equal(fields(el).Workflow.textContent, "newer");
});

test("a response after removal is ignored", async () => {
  reset();
  let release;
  mockFetch((url) => (url.startsWith("/api/v1/tasks/") && !url.endsWith("/llm-calls") && !url.endsWith("/steps") ? new Promise((r) => (release = () => r(jsonResponse(200, task())))) : jsonResponse(200, { items: [] })));
  const el = document.createElement("as-task-detail");
  el.setAttribute("task-id", ID);
  document.body.appendChild(el);
  el.remove();
  release();
  await flush();
  assert.equal(el.children.length, 0);
});

test("loading marks the element busy until it is done", async () => {
  reset();
  let release;
  mockFetch((url) => (url.startsWith("/api/v1/tasks/") && !url.endsWith("/llm-calls") && !url.endsWith("/steps") ? new Promise((r) => (release = () => r(jsonResponse(200, task())))) : jsonResponse(200, { items: [] })));
  const el = document.createElement("as-task-detail");
  el.setAttribute("task-id", ID);
  document.body.appendChild(el);
  assert.equal(el.getAttribute("aria-busy"), "true");
  release();
  await flush();
  assert.equal(el.hasAttribute("aria-busy"), false);
});

test("as-load reports success with the refresh state, and failure with its message", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const seen = [];
  reset();
  document.body.addEventListener("as-load", (e) => seen.push(e.detail));
  await mount({ "refresh-ms": 1000 }, api({ t: task({ status: "running", finished_at: null }) }));
  assert.equal(seen[0].ok, true);
  assert.equal(seen[0].live, true);
  await mount({}, api({ t: task() }));
  assert.equal(seen[1].live, false);
  await mount({}, api({ taskStatus: 500 }));
  assert.deepEqual([seen[2].ok, seen[2].message], [false, "Could not load task: internal error"]);
});

// ── safety ───────────────────────────────────────────────────────────────────

test("everything from the server is shown as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)>";
  const { el } = await mount({}, api({
    t: task({ workflow_id: evil, error: evil, status: "failed", payload: { x: evil }, result: { y: evil } }),
    users: [user(USER_ID, "<script>alert(1)</script>")],
  }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("script"), null);
  assert.equal(fields(el).Workflow.textContent, evil);
  assert.equal(fields(el).User.textContent, "<script>alert(1)</script>");
  assert.equal(el.querySelector("pre.as-task-detail-error").textContent, evil);
  assert.ok(el.querySelector("pre.as-task-detail-json").textContent.includes(evil));
});

test("a non-string error value is shown as text", async () => {
  const { el } = await mount({}, api({ t: task({ status: "failed", error: 42 }) }));
  assert.equal(el.querySelector("pre.as-task-detail-error").textContent, "42");
});

test("injects its stylesheet once", async () => {
  await mount();
  assert.equal(document.getElementById("as-task-detail-style").getAttribute("href"), "/static/components/as-task-detail/component.css");
});

test("without a task-id it asks for nothing and shows nothing", async () => {
  reset();
  const calls = mockFetch(api());
  const el = document.createElement("as-task-detail");
  document.body.appendChild(el);
  await flush();
  assert.deepEqual(calls, []);
  assert.equal(el.children.length, 0);
  el.setAttribute("task-id", ID);
  await flush();
  assert.deepEqual(taskCalls(calls), [`/api/v1/tasks/${ID}`]);
});

const usage = { calls: 2, input_tokens: 1200, output_tokens: 300, cost_usd: 0.0135, models: ["mid", "big"] };

test("shows what the task's model calls used", async () => {
  const { el } = await mount({}, api({ t: task({ usage }), calls: [call()] }));
  const f = fields(el);
  assert.equal(f.Model.textContent, "mid, big");
  assert.equal(f.Tokens.textContent, "1,200 in / 300 out");
  assert.equal(f.Cost.textContent, "$0.0135");
});

test("a task that made no calls has no model, tokens or cost fields", async () => {
  const { el } = await mount();
  for (const name of ["Model", "Tokens", "Cost"]) assert.equal(fields(el)[name], undefined, name);
});

test("lists every attempt, the failed one beside the one that answered", async () => {
  const calls = [
    call({ model_id: "big", outcome: "timeout", input_tokens: 0, output_tokens: 0, cost_usd: 0, error: "no first token for 60s", duration_ms: 60000 }),
    call({ id: "cccccccc-0000-4000-8000-000000000002", model_id: "mid", model_name: "mid-model:12b", tags: [], duration_ms: 12400 }),
  ];
  const { el } = await mount({}, api({ t: task({ usage }), calls }));
  const trs = [...el.querySelectorAll(".as-task-detail-calls-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
  assert.deepEqual(trs, [
    ["big", "chat, strong", "0", "0", "$0.00", "1 min 00 s", "timeout", "no first token for 60s"],
    ["mid", "—", "1,200", "300", "$0.0135", "12 s", "ok", "—"],
  ]);
  assert.deepEqual([...el.querySelectorAll(".as-task-detail-calls-table thead th")].map((th) => th.textContent), ["Model", "Tags", "Input", "Output", "Cost", "Duration", "Outcome", "Error"]);
  assert.equal(el.querySelectorAll(".as-task-detail-calls-table .as-badge")[0].className, "as-badge as-outcome-timeout");
  assert.equal(el.querySelectorAll(".as-call-model")[1].title, "mid-model:12b");
});

test("an error text from a provider is shown as text, never as markup", async () => {
  const { el } = await mount({}, api({ t: task({ usage }), calls: [call({ outcome: "error", error: "<script>alert(1)</script>" })] }));
  assert.equal(el.querySelector("script"), null);
  assert.equal(el.querySelector(".as-call-error").textContent, "<script>alert(1)</script>");
});

test("a task that has made no calls says so", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-task-detail-calls-table"), null);
  assert.equal(el.querySelector(".as-task-detail-calls .as-task-detail-none").textContent, "No model calls.");
});

test("a task that has not started is not asked for model calls", async () => {
  const { calls } = await mount({}, api({ t: task({ status: "pending", started_at: null, finished_at: null, execution_seconds: null }) }));
  assert.equal(callCalls(calls).length, 0);
});

test("a task that has started is asked for its calls, once", async () => {
  const { calls } = await mount({}, api({ t: task({ usage }), calls: [call()] }));
  assert.deepEqual(callCalls(calls), [`/api/v1/tasks/${ID}/llm-calls`]);
});

test("failing to load the calls shows a message in their section, not a failed page", async () => {
  const { el } = await mount({}, api({ t: task({ usage }), callsStatus: 500 }));
  assert.equal(fields(el).ID.textContent, ID);
  assert.equal(el.querySelector(".as-task-detail-calls .as-task-detail-error").textContent, "Could not load model calls: internal error");
});

test("a running task's calls refresh with the task", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const running = task({ status: "running", finished_at: null, execution_seconds: 4, usage });
  const { el, calls } = await mount({}, api({ t: running, calls: [call()] }));
  assert.equal(callCalls(calls).length, 1);
  mock.timers.tick(3000);
  await flush();
  assert.equal(callCalls(calls).length, 2);
  assert.equal(el.querySelectorAll(".as-task-detail-calls-table tbody tr").length, 1);
});

test("only a real error is marked as one; the dash of a good call is not", async () => {
  const { el } = await mount({}, api({ t: task({ usage }), calls: [call({ outcome: "error", error: "boom" }), call({ id: "cccccccc-0000-4000-8000-000000000002" })] }));
  const [bad, good] = [...el.querySelectorAll(".as-call-error")];
  assert.ok(bad.classList.contains("has-error"));
  assert.equal(good.classList.contains("has-error"), false);
});

// ── a workflow run: failure, question, state and trace ───────────────────────

const run = { run_id: ID, workflow_id: "learn-topic", status: "waiting", state_name: "assess", state: { goal: "Arabic", level: "unknown" }, steps: 3, tokens: 4200 };

test("a failed task shows why at the top, as an alert, before anything else", async () => {
  const { el } = await mount({}, api({ t: task({ status: "failed", result: null, error: "retries_exhausted: the model kept sending invalid JSON", run: { ...run, status: "failed" } }) }));
  const banner = el.querySelector(".as-task-detail-failure");
  assert.equal(banner.getAttribute("role"), "alert");
  assert.equal(banner.querySelector("pre").textContent, "retries_exhausted: the model kept sending invalid JSON");
  assert.match(banner.textContent, /This task failed/);
  assert.ok(banner.compareDocumentPosition(el.querySelector("dl")) & window.Node.DOCUMENT_POSITION_FOLLOWING, "the banner comes before the fields");
});

test("a task that did not fail shows no failure banner, even with an error text left over", async () => {
  const { el } = await mount({}, api({ t: task({ status: "succeeded", error: null }) }));
  assert.equal(el.querySelector(".as-task-detail-failure"), null);
});

test("a waiting task shows the question and an answer box for it", async () => {
  const { el } = await mount({}, api({ t: task({ status: "waiting", result: null, finished_at: null, execution_seconds: 4, run, ask: { question: "Which level?" } }) }));
  const box = el.querySelector("as-answer-box");
  assert.equal(box.getAttribute("task-id"), ID);
  assert.equal(box.querySelector(".as-answer-question").textContent, "Which level?");
  assert.equal(fields(el).Status.textContent, "waiting");
  assert.equal(fields(el).Status.querySelector(".as-badge").className, "as-badge as-status-waiting");
});

test("a task that is not waiting has no answer box", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector("as-answer-box"), null);
});

test("answering reloads the task at once", async () => {
  const waiting = task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } });
  let current = waiting;
  const { el, calls } = await mount({}, (url, options) => {
    if (options && options.method === "POST") {
      current = task({ status: "pending", started_at: "2026-10-16T12:00:02Z", result: null, finished_at: null, run });
      return jsonResponse(200, current);
    }
    return api({ t: current })(url);
  });
  assert.equal(taskCalls(calls).length, 1);
  el.querySelector("textarea").value = "beginner";
  el.querySelector("button.as-answer-send").click();
  await flush();
  await flush();
  assert.equal(taskCalls(calls).length, 2, "the page looks at the task again");
  assert.equal(fields(el).Status.textContent, "pending");
  assert.equal(el.querySelector("as-answer-box"), null);
});

test("a refused answer stays visible next to the box and the page is not reloaded", async () => {
  const { el, calls } = await mount({}, (url, options) => {
    if (options && options.method === "POST") return jsonResponse(409, { error: { message: "the task is not waiting for an answer" } });
    return api({ t: task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } }) })(url);
  });
  el.querySelector("textarea").value = "x";
  el.querySelector("button.as-answer-send").click();
  await flush();
  assert.match(el.querySelector(".as-answer-error").textContent, /the task is not waiting for an answer/);
  assert.equal(taskCalls(calls).length, 1);
  assert.equal(el.querySelector("textarea").value, "x");
});

test("a run's state, step count and tokens are shown", async () => {
  const { el } = await mount({}, api({ t: task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } }) }));
  const section = el.querySelector(".as-task-detail-run");
  assert.match(section.querySelector(".as-task-detail-run-facts").textContent, /State: assess · Run status: waiting · Steps: 3 · Tokens: 4,200/);
  assert.match(section.querySelector("pre").textContent, /"goal": "Arabic"/);
});

test("a task without a run has no run section and no trace", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-task-detail-run"), null);
  assert.equal(el.querySelector(".as-task-detail-trace"), null);
});

test("the trace of a run is listed, and a refused reply is visible in it", async () => {
  const steps = [
    step({ id: 1, outcome: "retry", error: "the reply is not valid JSON", action: null, reply: "oops" }),
    step({ id: 2, attempt: 2 }),
  ];
  const { el } = await mount({}, api({ t: task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } }), steps }));
  const trace = el.querySelector(".as-task-detail-trace as-run-trace");
  assert.equal(trace.querySelectorAll("tr.as-trace-row").length, 2);
  assert.match(trace.textContent, /the reply is not valid JSON/);
  assert.match(trace.textContent, /oops/);
});

test("a trace that cannot be loaded says so in its section, not as a failed page", async () => {
  const { el } = await mount({}, api({ t: task({ run }), stepsStatus: 500 }));
  assert.equal(fields(el).ID.textContent, ID);
  assert.equal(el.querySelector(".as-trace-message").textContent, "Could not load the trace: internal error");
  assert.ok(el.querySelector(".as-trace-message").classList.contains("error"));
});

test("a task that has not started is not asked for a trace", async () => {
  const { calls } = await mount({}, api({ t: task({ status: "pending", started_at: null, finished_at: null, execution_seconds: null }) }));
  assert.equal(calls.filter((u) => u.endsWith("/steps")).length, 0);
});

test("a running task's trace refreshes with the task", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const running = task({ status: "running", finished_at: null, execution_seconds: 4, run: { ...run, status: "running" } });
  const { calls } = await mount({}, api({ t: running, steps: [step()] }));
  assert.equal(calls.filter((u) => u.endsWith("/steps")).length, 1);
  mock.timers.tick(3000);
  await flush();
  assert.equal(calls.filter((u) => u.endsWith("/steps")).length, 2);
});

test("a waiting task is not refreshed on its own, so a half-typed answer is not wiped", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({}, api({ t: task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } }) }));
  el.querySelector("textarea").value = "half typed";
  mock.timers.tick(60000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
  assert.equal(el.querySelector("textarea").value, "half typed");
});

// ── the buttons that tidy the queue ──────────────────────────────────────────

test("the task page offers the actions for the task's status", async () => {
  const { el } = await mount({}, api({ t: task({ status: "failed", error: "boom", result: null }) }));
  assert.deepEqual([...el.querySelectorAll("as-task-actions button")].map((b) => b.textContent), ["Retry", "Remove"]);
});

test("cancelling from the page reloads the task", async () => {
  let current = task({ status: "waiting", result: null, finished_at: null, run, ask: { question: "?" } });
  const { el, calls } = await mount({}, (url, options) => {
    if (options && options.method === "POST" && url.endsWith("/cancel")) {
      current = task({ status: "cancelled", result: null });
      return jsonResponse(200, current);
    }
    return api({ t: current })(url);
  });
  assert.equal(taskCalls(calls).length, 1);
  [...el.querySelectorAll("as-task-actions button")].find((b) => b.textContent === "Cancel").click();
  await flush();
  await flush();
  assert.equal(fields(el).Status.textContent, "cancelled");
});
