import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, task, user, tasksApi, USER_ID } from "../testenv.js";

await load("format.js", "api.js", "components/as-task-list/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const ID1 = "11111111-2222-4333-8444-555555555555";
const ID2 = "22222222-2222-4333-8444-555555555555";
const ID3 = "33333333-2222-4333-8444-555555555555";

async function mount(attrs = {}, handler = tasksApi({ tasks: [task()] })) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-task-list");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

const rows = (el) => [...el.querySelectorAll("tbody tr")];
const cells = (tr) => [...tr.children].map((td) => td.textContent);
const taskCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/tasks"));
const userCalls = (calls) => calls.filter((u) => u.startsWith("/api/v1/users"));

test("renders one row per task with every column", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ workflow_id: "learn-topic", source: "telegram", status: "succeeded", queue_seconds: 2, execution_seconds: 65 })] }));
  const [tr] = rows(el);
  const text = cells(tr);
  assert.equal(text[0], "11111111");
  assert.equal(text[1], "Ada");
  assert.equal(text[2], "learn-topic");
  assert.equal(text[3], "telegram");
  assert.equal(text[4], "succeeded");
  assert.match(text[5], /2026/);
  assert.equal(text[6], "2.0 s");
  assert.equal(text[7], "1 min 05 s");
  assert.deepEqual([...el.querySelectorAll("thead th")].map((th) => th.textContent), ["ID", "User", "Workflow", "Source", "Status", "Created", "Queue time", "Execution time", "Model", "Tokens in / out", "Cost"]);
});

test("the id links to the task's own page and carries the full id", async () => {
  const { el } = await mount();
  const a = el.querySelector("a.as-task-id");
  assert.equal(a.getAttribute("href"), `#/tasks/${ID1}`);
  assert.equal(a.title, ID1);
  assert.equal(rows(el)[0].dataset.taskId, ID1);
});

test("the status is a badge named after the status", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ id: ID1, status: "failed" }), task({ id: ID2, status: "pending", started_at: null, finished_at: null, execution_seconds: null })] }));
  assert.equal(rows(el)[0].querySelector(".as-badge").className, "as-badge as-status-failed");
  assert.equal(rows(el)[1].querySelector(".as-badge").className, "as-badge as-status-pending");
});

test("a pending task counts its queue time up, a running one its execution time", async () => {
  const { el } = await mount({}, tasksApi({
    tasks: [
      task({ id: ID1, status: "pending", started_at: null, finished_at: null, queue_seconds: 12, execution_seconds: null }),
      task({ id: ID2, status: "running", finished_at: null, queue_seconds: 3, execution_seconds: 7 }),
      task({ id: ID3, status: "succeeded", queue_seconds: 3, execution_seconds: 7 }),
    ],
  }));
  const [pending, running, done] = rows(el).map(cells);
  assert.equal(pending[6], "12 s…");
  assert.equal(pending[7], "—");
  assert.equal(running[6], "3.0 s");
  assert.equal(running[7], "7.0 s…");
  assert.equal(done[6], "3.0 s");
  assert.equal(done[7], "7.0 s");
  assert.ok(rows(el)[0].querySelector(".as-task-queue").classList.contains("as-live"));
  assert.ok(rows(el)[1].querySelector(".as-task-exec").classList.contains("as-live"));
  assert.equal(rows(el)[2].querySelector(".as-live"), null);
});

test("the created time keeps its exact timestamp as a tooltip", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-task-created").title, "2026-10-16T12:00:00Z");
});

test("an empty queue says so and shows no table", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [] }));
  assert.equal(el.querySelector(".as-task-list-message").textContent, "No tasks yet.");
  assert.equal(el.querySelector(".as-task-list-table-wrap").hidden, true);
  assert.equal(el.querySelector(".as-task-list-summary").textContent, "0 tasks");
  assert.equal(Number(el.querySelector("dp-pager").getAttribute("page-count")), 1, "one page, which the pager draws as nothing");
});

test("an empty filtered list names the filter", async () => {
  const { el } = await mount({ status: "failed" }, tasksApi({ tasks: [] }));
  assert.equal(el.querySelector(".as-task-list-message").textContent, "No tasks with status failed.");
});

test("the summary counts the tasks", async () => {
  assert.equal((await mount({}, tasksApi({ tasks: [task()] }))).el.querySelector(".as-task-list-summary").textContent, "1 task");
  assert.equal((await mount({}, tasksApi({ tasks: [task()], total: 60 }))).el.querySelector(".as-task-list-summary").textContent, "60 tasks");
});

test("the request carries the page size, offset and status", async () => {
  assert.deepEqual(taskCalls((await mount()).calls), ["/api/v1/tasks?limit=25&offset=0"]);
  assert.deepEqual(taskCalls((await mount({ page: 3, "page-size": 10, status: "failed" })).calls), ["/api/v1/tasks?limit=10&offset=20&status=failed"]);
});

test("nonsense attributes fall back to the defaults", async () => {
  const { calls } = await mount({ page: "abc", "page-size": 1000, status: "bogus" });
  assert.deepEqual(taskCalls(calls), ["/api/v1/tasks?limit=25&offset=0"]);
  assert.deepEqual(taskCalls((await mount({ page: -2, "page-size": 0 })).calls), ["/api/v1/tasks?limit=25&offset=0"]);
});

test("a status filter that is injected into the URL cannot add parameters", async () => {
  const { calls } = await mount({ status: "failed&limit=1000" });
  assert.deepEqual(taskCalls(calls), ["/api/v1/tasks?limit=25&offset=0"]);
});

test("users are fetched once and their names shown", async () => {
  const { el, calls } = await mount({}, tasksApi({
    tasks: [task({ id: ID1 }), task({ id: ID2 })],
    users: [user(USER_ID, "Ada")],
  }));
  assert.equal(userCalls(calls).length, 1);
  assert.deepEqual(rows(el).map((tr) => cells(tr)[1]), ["Ada", "Ada"]);
});

test("a failure to load users still shows the tasks, with the user's id", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task()], usersStatus: 500 }));
  assert.equal(rows(el).length, 1);
  assert.equal(cells(rows(el)[0])[1], "aaaaaaaa");
  assert.equal(el.querySelector(".as-task-list-message").textContent, "");
});

test("a user that is not known yet triggers one more fetch of the users", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const state = { users: [user(USER_ID, "Ada")], tasks: [task({ id: ID1, status: "running", finished_at: null })] };
  const { el, calls } = await mount({ "refresh-ms": 1000 }, (url) => tasksApi({ tasks: state.tasks, users: state.users })(url));
  assert.equal(userCalls(calls).length, 1);
  const NEW_USER = "bbbbbbbb-0000-4000-8000-000000000002";
  state.users = [user(USER_ID, "Ada"), user(NEW_USER, "Grace")];
  state.tasks = [task({ id: ID1, status: "running", finished_at: null }), task({ id: ID2, user_id: NEW_USER })];
  mock.timers.tick(1000);
  await flush();
  assert.equal(userCalls(calls).length, 2, "refetched because a new user appeared");
  assert.deepEqual(rows(el).map((tr) => cells(tr)[1]), ["Ada", "Grace"]);
  mock.timers.tick(1000);
  await flush();
  assert.equal(userCalls(calls).length, 2, "and not again once every user is known");
});

test("an HTTP error shows the server's message", async () => {
  const { el } = await mount({}, tasksApi({ tasksStatus: 500 }));
  const message = el.querySelector(".as-task-list-message");
  assert.equal(message.textContent, "Could not load tasks: internal error");
  assert.ok(message.classList.contains("error"));
});

test("a network error shows its reason", async () => {
  const { el } = await mount({}, () => {
    throw new TypeError("Failed to fetch");
  });
  assert.equal(el.querySelector(".as-task-list-message").textContent, "Could not load tasks: Failed to fetch");
});

test("after a failure the rows already shown stay, and recovery clears the error", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let failing = false;
  const ok = tasksApi({ tasks: [task({ status: "running", finished_at: null })] });
  const { el } = await mount({ "refresh-ms": 1000 }, (url) => (failing ? jsonResponse(503, { error: { message: "database is down" } }) : ok(url)));
  assert.equal(rows(el).length, 1);
  failing = true;
  mock.timers.tick(1000);
  await flush();
  assert.equal(rows(el).length, 1, "the last good data stays visible");
  assert.equal(el.querySelector(".as-task-list-message").textContent, "Could not load tasks: database is down");
  failing = false;
  mock.timers.tick(1000);
  await flush();
  assert.equal(el.querySelector(".as-task-list-message").textContent, "");
  assert.equal(el.querySelector(".as-task-list-message").classList.contains("error"), false);
});

test("the pager follows the total and the page", async () => {
  const { el } = await mount({ page: 2 }, tasksApi({ tasks: Array.from({ length: 25 }, (_, i) => task({ id: `${i}`.padStart(8, "0") + "-2222-4333-8444-555555555555` ".slice(0, 28) })), total: 60 }));
  const pager = el.querySelector("dp-pager");
  assert.equal(pager.getAttribute("page"), "2");
  assert.equal(pager.getAttribute("page-count"), "3");
});

test("picking a page emits as-list-change with the current status", async () => {
  const { el } = await mount({ page: 1, status: "failed" }, tasksApi({ tasks: [task({ status: "failed" })], total: 60 }));
  const seen = [];
  document.body.addEventListener("as-list-change", (e) => seen.push(e.detail));
  el.querySelector("dp-pager").dispatchEvent(new window.CustomEvent("dp-pager-select", { detail: { page: 2 }, bubbles: true }));
  assert.deepEqual(seen, [{ status: "failed", page: 2 }]);
  assert.equal(el.getAttribute("page"), "1", "the list leaves the decision to its host");
});

test("choosing a status emits as-list-change and goes back to page 1", async () => {
  const { el } = await mount({ page: 3 }, tasksApi({ tasks: [task()], total: 90 }));
  const seen = [];
  document.body.addEventListener("as-list-change", (e) => seen.push(e.detail));
  const select = el.querySelector("select.as-task-list-status");
  select.value = "failed";
  select.dispatchEvent(new window.Event("change", { bubbles: true }));
  assert.deepEqual(seen, [{ status: "failed", page: 1 }]);
});

test("the filter offers every status and shows the current one", async () => {
  const { el } = await mount({ status: "running" });
  const select = el.querySelector("select.as-task-list-status");
  assert.deepEqual([...select.options].map((o) => o.value), ["", "pending", "running", "waiting", "succeeded", "failed", "cancelled"]);
  assert.deepEqual([...select.options].map((o) => o.textContent), ["All", "pending", "running", "waiting", "succeeded", "failed", "cancelled"]);
  assert.equal(select.value, "running");
});

test("changing the status attribute reloads and updates the filter", async () => {
  const { el, calls } = await mount();
  el.setAttribute("status", "failed");
  await flush();
  assert.equal(el.querySelector("select").value, "failed");
  assert.equal(taskCalls(calls).at(-1), "/api/v1/tasks?limit=25&offset=0&status=failed");
});

test("changing the page attribute reloads", async () => {
  const { el, calls } = await mount();
  el.setAttribute("page", "2");
  await flush();
  assert.equal(taskCalls(calls).at(-1), "/api/v1/tasks?limit=25&offset=25");
});

test("setting an attribute to the value it has does not reload", async () => {
  const { el, calls } = await mount({ page: 2 });
  const before = calls.length;
  el.setAttribute("page", "2");
  await flush();
  assert.equal(calls.length, before);
});

test("a page beyond the end asks the host for the last page", async () => {
  const seen = [];
  reset();
  document.body.addEventListener("as-list-change", (e) => seen.push(e.detail), { once: true });
  const { el } = await mount({ page: 9 }, tasksApi({ tasks: [], total: 30, tasksBody: { items: [], total: 30, limit: 25, offset: 200 } }));
  assert.ok(el.isConnected);
  assert.deepEqual(seen, [{ status: "", page: 2 }]);
});

test("loading marks the element busy until it is done", async () => {
  reset();
  let release;
  mockFetch((url) => (url.startsWith("/api/v1/tasks") ? new Promise((r) => (release = () => r(jsonResponse(200, { items: [], total: 0, limit: 25, offset: 0 })))) : jsonResponse(200, { items: [] })));
  const el = document.createElement("as-task-list");
  document.body.appendChild(el);
  assert.equal(el.getAttribute("aria-busy"), "true");
  release();
  await flush();
  assert.equal(el.hasAttribute("aria-busy"), false);
});

// ── live refresh ─────────────────────────────────────────────────────────────

test("while a task is pending or running the list reloads at the refresh interval", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 2000 }, tasksApi({ tasks: [task({ status: "running", finished_at: null })] }));
  assert.equal(taskCalls(calls).length, 1);
  mock.timers.tick(1999);
  await flush();
  assert.equal(taskCalls(calls).length, 1, "not before the interval");
  mock.timers.tick(1);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
  mock.timers.tick(2000);
  await flush();
  assert.equal(taskCalls(calls).length, 3, "and again, for as long as something is active");
});

test("a list of finished tasks does not reload by itself", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 1000 }, tasksApi({ tasks: [task({ status: "succeeded" }), task({ id: ID2, status: "failed" })] }));
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("refreshing stops once the last active task has finished", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let status = "running";
  const { el, calls } = await mount({ "refresh-ms": 1000 }, (url) => tasksApi({ tasks: [task({ status })] })(url));
  status = "succeeded";
  mock.timers.tick(1000);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
  assert.equal(cells(rows(el)[0])[4], "succeeded");
  mock.timers.tick(5000);
  await flush();
  assert.equal(taskCalls(calls).length, 2, "nothing is active, so nothing is scheduled");
});

test("refresh-ms 0 turns refreshing off", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 0 }, tasksApi({ tasks: [task({ status: "running", finished_at: null })] }));
  mock.timers.tick(60000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("a bad refresh-ms means the default of three seconds", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": "soon" }, tasksApi({ tasks: [task({ status: "pending", started_at: null, finished_at: null })] }));
  mock.timers.tick(2999);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
  mock.timers.tick(1);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
});

test("a failed load is retried at the interval", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ "refresh-ms": 1000 }, tasksApi({ tasksStatus: 500 }));
  mock.timers.tick(1000);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
});

test("removing the element stops the refreshing", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ "refresh-ms": 1000 }, tasksApi({ tasks: [task({ status: "running", finished_at: null })] }));
  el.remove();
  mock.timers.tick(10000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

test("changing refresh-ms reschedules", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ "refresh-ms": 60000 }, tasksApi({ tasks: [task({ status: "running", finished_at: null })] }));
  el.setAttribute("refresh-ms", "500");
  mock.timers.tick(500);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
  el.setAttribute("refresh-ms", "0");
  mock.timers.tick(100000);
  await flush();
  assert.equal(taskCalls(calls).length, 2);
});

test("a slow response to an old request never overwrites a newer one", async () => {
  reset();
  const pending = [];
  mockFetch((url) => {
    if (url.startsWith("/api/v1/users")) return jsonResponse(200, { items: [user(USER_ID, "Ada")] });
    return new Promise((resolve) => pending.push({ url, resolve }));
  });
  const el = document.createElement("as-task-list");
  document.body.appendChild(el);
  await flush();
  el.setAttribute("status", "failed");
  await flush();
  assert.equal(pending.length, 2);
  pending[1].resolve(jsonResponse(200, { items: [task({ id: ID2, status: "failed", workflow_id: "newer" })], total: 1, limit: 25, offset: 0 }));
  await flush();
  pending[0].resolve(jsonResponse(200, { items: [task({ id: ID1, workflow_id: "older" })], total: 1, limit: 25, offset: 0 }));
  await flush();
  assert.deepEqual(rows(el).map((tr) => cells(tr)[2]), ["newer"]);
});

test("a response that arrives after the element was removed is ignored", async () => {
  reset();
  let release;
  mockFetch((url) => (url.startsWith("/api/v1/tasks") ? new Promise((r) => (release = () => r(jsonResponse(200, { items: [task()], total: 1, limit: 25, offset: 0 })))) : jsonResponse(200, { items: [] })));
  const el = document.createElement("as-task-list");
  document.body.appendChild(el);
  el.remove();
  const events = [];
  document.body.addEventListener("as-load", (e) => events.push(e));
  release();
  await flush();
  assert.equal(rows(el).length, 0);
  assert.equal(events.length, 0);
});

// ── events and safety ────────────────────────────────────────────────────────

test("as-load reports a good load and whether refreshing is on", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  reset();
  const seen = [];
  document.body.addEventListener("as-load", (e) => seen.push(e.detail));
  await mount({ "refresh-ms": 1000 }, tasksApi({ tasks: [task({ status: "running", finished_at: null })] }));
  assert.equal(seen.length, 1);
  assert.equal(seen[0].ok, true);
  assert.equal(seen[0].live, true);
  assert.ok(seen[0].at instanceof Date);
});

test("as-load reports that nothing is refreshing when all tasks are done", async () => {
  const seen = [];
  reset();
  document.body.addEventListener("as-load", (e) => seen.push(e.detail));
  await mount({}, tasksApi({ tasks: [task()] }));
  assert.equal(seen[0].live, false);
});

test("as-load carries the message of a failure", async () => {
  const seen = [];
  reset();
  document.body.addEventListener("as-load", (e) => seen.push(e.detail));
  await mount({}, tasksApi({ tasksStatus: 500 }));
  assert.equal(seen[0].ok, false);
  assert.equal(seen[0].message, "Could not load tasks: internal error");
});

test("everything from the server is shown as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)>";
  const { el } = await mount({}, tasksApi({
    tasks: [task({ workflow_id: evil, source: "api", id: ID1 })],
    users: [user(USER_ID, "<script>alert(1)</script>")],
  }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("script"), null);
  assert.equal(cells(rows(el)[0])[2], evil);
  assert.equal(cells(rows(el)[0])[1], "<script>alert(1)</script>");
});

test("an id that is not what it should be cannot break out of the link", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ id: 'x"><a href="javascript:alert(1)">' })] }));
  const links = el.querySelectorAll("tbody a");
  assert.equal(links.length, 1);
  assert.ok(links[0].getAttribute("href").startsWith("#/tasks/"));
  assert.doesNotMatch(links[0].getAttribute("href"), /javascript:/);
});

test("injects its stylesheet once", async () => {
  await mount();
  await mount();
  assert.equal(document.querySelectorAll("#as-task-list-style").length <= 1, true);
  assert.equal(document.getElementById("as-task-list-style").getAttribute("href"), "/static/components/as-task-list/component.css");
});

test("a task that called a model shows the model, tokens and cost", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ usage: { calls: 2, input_tokens: 1200, output_tokens: 300, cost_usd: 0.0135, models: ["mid", "big"] } })] }));
  const text = cells(rows(el)[0]);
  assert.equal(text[8], "mid, big");
  assert.equal(text[9], "1,200 / 300");
  assert.equal(text[10], "$0.0135");
});

test("a free model's task shows a zero cost, not a dash", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ usage: { calls: 1, input_tokens: 10, output_tokens: 2, cost_usd: 0, models: ["small"] } })] }));
  assert.equal(cells(rows(el)[0])[10], "$0.00");
});

test("a task that made no model call shows dashes", async () => {
  const { el } = await mount();
  assert.deepEqual(cells(rows(el)[0]).slice(8), ["—", "—", "—"]);
});

test("a server that sends no usage does not break the row", async () => {
  const t = task();
  delete t.usage;
  const { el } = await mount({}, tasksApi({ tasks: [t] }));
  assert.deepEqual(cells(rows(el)[0]).slice(8), ["—", "—", "—"]);
});

test("model names reach the page as text, never as markup", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ usage: { calls: 1, input_tokens: 1, output_tokens: 1, cost_usd: 0, models: ["<img src=x onerror=alert(1)>"] } })] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(cells(rows(el)[0])[8], "<img src=x onerror=alert(1)>");
});

test("a failed task shows the first line of its error right in the row, the whole error on hover", async () => {
  const error = "retries_exhausted: the model kept sending invalid JSON\nsecond line";
  const { el } = await mount({}, tasksApi({ tasks: [task({ status: "failed", result: null, error })] }));
  const shown = el.querySelector("tbody tr .as-task-error");
  assert.equal(shown.textContent, "retries_exhausted: the model kept sending invalid JSON");
  assert.equal(shown.title, error);
});

test("a long error is clipped, with an ellipsis", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ status: "failed", result: null, error: "x".repeat(300) })] }));
  const text = el.querySelector(".as-task-error").textContent;
  assert.equal(text.length, 120);
  assert.ok(text.endsWith("…"));
});

test("a task that did not fail shows no error line", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-task-error"), null);
});

test("a waiting task has its own badge and shows what it asked", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ status: "waiting", result: null, finished_at: null, execution_seconds: 4, ask: { question: "Which language do you want to learn?" } })] }));
  const tr = rows(el)[0];
  assert.equal(tr.querySelector(".as-badge").className, "as-badge as-status-waiting");
  assert.equal(tr.querySelector(".as-task-ask").textContent, "Which language do you want to learn?");
  assert.deepEqual(JSON.parse(tr.querySelector(".as-task-ask").title), { question: "Which language do you want to learn?" });
});

test("a waiting task without a readable question still says it waits", async () => {
  const { el } = await mount({}, tasksApi({ tasks: [task({ status: "waiting", result: null, finished_at: null, ask: { exercise: 3 } })] }));
  assert.equal(el.querySelector(".as-task-ask").textContent, "waiting for an answer");
});

test("error and question reach the page as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)>";
  const { el } = await mount({}, tasksApi({ tasks: [task({ id: ID1, status: "failed", result: null, error: evil }), task({ id: ID2, status: "waiting", result: null, ask: { question: evil } })] }));
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector(".as-task-error").textContent, evil);
  assert.equal(el.querySelector(".as-task-ask").textContent, evil);
});

test("a waiting task is not polled: nothing changes until the user answers", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({}, tasksApi({ tasks: [task({ status: "waiting", result: null, finished_at: null, ask: { question: "?" } })] }));
  mock.timers.tick(60000);
  await flush();
  assert.equal(taskCalls(calls).length, 1);
});

// ── removing all the tasks of a finished status ──────────────────────────────

function bulkApi(state) {
  return (url, options) => {
    if (options && options.method === "DELETE") {
      state.deleted.push(url);
      if (state.fail) return jsonResponse(500, { error: { message: "boom" } });
      state.tasks = [];
      return jsonResponse(200, { removed: 2 });
    }
    return tasksApi({ tasks: state.tasks })(url);
  };
}

test("a finished status filter offers to remove all of them, and an unfinished one does not", async () => {
  const state = { tasks: [task({ status: "failed", error: "x" }), task({ id: ID2, status: "failed", error: "y" })], deleted: [] };
  const { el } = await mount({ status: "failed" }, bulkApi(state));
  const button = el.querySelector(".as-task-list-clear");
  assert.equal(button.hidden, false);
  assert.equal(button.textContent, "Remove all 2 failed tasks");
  for (const status of ["", "pending", "running", "waiting"]) {
    const { el: other } = await mount(status ? { status } : {}, bulkApi({ tasks: [task()], deleted: [] }));
    assert.equal(other.querySelector(".as-task-list-clear").hidden, true, `"${status}"`);
  }
});

test("no tasks of that status, nothing to remove", async () => {
  const { el } = await mount({ status: "failed" }, bulkApi({ tasks: [], deleted: [] }));
  assert.equal(el.querySelector(".as-task-list-clear").hidden, true);
});

test("the first click arms it, the second removes them and the list reloads empty", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const state = { tasks: [task({ status: "failed", error: "x" })], deleted: [] };
  const { el } = await mount({ status: "failed", "refresh-ms": 0 }, bulkApi(state));
  const button = el.querySelector(".as-task-list-clear");
  button.click();
  await flush();
  assert.deepEqual(state.deleted, []);
  assert.equal(button.textContent, "Click again to remove 1 failed tasks");
  button.click();
  await flush();
  await flush();
  assert.deepEqual(state.deleted, ["/api/v1/tasks?status=failed"]);
  assert.equal(rows(el).length, 0);
  assert.equal(el.querySelector(".as-task-list-clear").hidden, true);
});

test("the armed button disarms by itself", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const state = { tasks: [task({ status: "failed", error: "x" })], deleted: [] };
  const { el } = await mount({ status: "failed", "refresh-ms": 0 }, bulkApi(state));
  const button = el.querySelector(".as-task-list-clear");
  button.click();
  await flush();
  mock.timers.tick(4000);
  assert.equal(button.textContent, "Remove all 1 failed tasks");
  button.click();
  await flush();
  assert.deepEqual(state.deleted, []);
});

test("a refused bulk removal shows the server's message and keeps the list", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const state = { tasks: [task({ status: "failed", error: "x" })], deleted: [], fail: true };
  const { el } = await mount({ status: "failed", "refresh-ms": 0 }, bulkApi(state));
  const button = el.querySelector(".as-task-list-clear");
  button.click();
  await flush();
  button.click();
  await flush();
  assert.equal(el.querySelector(".as-task-list-message").textContent, "Could not remove the tasks: boom");
  assert.ok(el.querySelector(".as-task-list-message").classList.contains("error"));
  assert.equal(rows(el).length, 1);
});
