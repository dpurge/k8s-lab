import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, flush, jsonResponse, task } from "../testenv.js";

await load("format.js", "api.js", "components/as-task-actions/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const ID = "11111111-2222-4333-8444-555555555555";

function mount(status) {
  reset();
  const el = document.createElement("as-task-actions");
  el.task = task({ id: ID, status });
  document.body.appendChild(el);
  return el;
}

function fetchSays(handler) {
  const sent = [];
  global.fetch = async (url, options) => {
    sent.push({ url: String(url), method: options && options.method });
    return handler(String(url), options);
  };
  return sent;
}

const labels = (el) => [...el.querySelectorAll("button")].map((b) => b.textContent);
const click = async (el, text) => {
  [...el.querySelectorAll("button")].find((b) => b.textContent === text).click();
  await flush();
};
const events = (el, names) => {
  const seen = [];
  for (const n of names) el.addEventListener(n, (e) => seen.push([n, e.detail]));
  return seen;
};

test("each status offers exactly the actions that make sense", () => {
  assert.deepEqual(labels(mount("pending")), ["Cancel", "Remove"]);
  assert.deepEqual(labels(mount("waiting")), ["Cancel", "Remove"]);
  assert.deepEqual(labels(mount("running")), ["Stop"], "a running task cannot be removed");
  assert.deepEqual(labels(mount("succeeded")), ["Remove"]);
  assert.deepEqual(labels(mount("failed")), ["Retry", "Remove"]);
  assert.deepEqual(labels(mount("cancelled")), ["Retry", "Remove"]);
});

test("with no task there are no buttons", () => {
  reset();
  const el = document.createElement("as-task-actions");
  document.body.appendChild(el);
  assert.deepEqual(labels(el), []);
});

test("cancelling posts to the task's cancel address and announces the changed task", async () => {
  const el = mount("waiting");
  const sent = fetchSays(() => jsonResponse(200, task({ id: ID, status: "cancelled" })));
  const seen = events(el, ["as-task-changed"]);
  await click(el, "Cancel");
  assert.deepEqual(sent, [{ url: `/api/v1/tasks/${ID}/cancel`, method: "POST" }]);
  assert.equal(seen[0][1].task.status, "cancelled");
});

test("stopping a running task works the same way, with the 202 answer", async () => {
  const el = mount("running");
  const sent = fetchSays(() => jsonResponse(202, task({ id: ID, status: "running" })));
  const seen = events(el, ["as-task-changed"]);
  await click(el, "Stop");
  assert.equal(sent[0].url, `/api/v1/tasks/${ID}/cancel`);
  assert.equal(seen.length, 1);
});

test("retrying posts to the retry address and announces the new task", async () => {
  const el = mount("failed");
  const sent = fetchSays(() => jsonResponse(201, task({ id: "99999999-2222-4333-8444-555555555555", status: "pending" })));
  const seen = events(el, ["as-task-retried"]);
  await click(el, "Retry");
  assert.deepEqual(sent, [{ url: `/api/v1/tasks/${ID}/retry`, method: "POST" }]);
  assert.equal(seen[0][1].task.id, "99999999-2222-4333-8444-555555555555");
});

test("the first click on Remove only arms it; the second removes and announces", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const el = mount("failed");
  const sent = fetchSays(() => ({ ok: true, status: 204, json: async () => ({}) }));
  const seen = events(el, ["as-task-removed"]);
  await click(el, "Remove");
  assert.equal(sent.length, 0, "one click removes nothing");
  assert.ok(labels(el).includes("Click again to remove"));
  await click(el, "Click again to remove");
  assert.deepEqual(sent, [{ url: `/api/v1/tasks/${ID}`, method: "DELETE" }]);
  assert.deepEqual(seen, [["as-task-removed", { id: ID }]]);
});

test("an armed Remove disarms by itself after a few seconds", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const el = mount("failed");
  const sent = fetchSays(() => ({ ok: true, status: 204 }));
  await click(el, "Remove");
  mock.timers.tick(4000);
  assert.ok(labels(el).includes("Remove"), "back to the plain label");
  await click(el, "Remove");
  assert.equal(sent.length, 0, "and it needs arming again");
});

test("a refused action shows the server's message next to the buttons and announces nothing", async () => {
  const el = mount("pending");
  fetchSays(() => jsonResponse(409, { error: { code: "conflict", message: "only a pending, waiting or running task can be cancelled" } }));
  const seen = events(el, ["as-task-changed"]);
  await click(el, "Cancel");
  assert.equal(el.querySelector(".as-actions-error").textContent, "Could not stop the task: only a pending, waiting or running task can be cancelled");
  assert.equal(el.querySelector(".as-actions-error").hidden, false);
  assert.deepEqual(seen, []);
  assert.ok(el.querySelector("button").disabled === false, "one can try again");
});

test("a refused retry, a refused remove and a network failure are all shown", async () => {
  let el = mount("failed");
  fetchSays(() => jsonResponse(409, { error: { message: "only a failed or cancelled task can be retried" } }));
  await click(el, "Retry");
  assert.match(el.querySelector(".as-actions-error").textContent, /^Could not retry the task: only a failed/);

  mock.timers.enable({ apis: ["setTimeout"] });
  el = mount("waiting");
  fetchSays(() => jsonResponse(500, { error: { message: "internal error" } }));
  await click(el, "Remove");
  await click(el, "Click again to remove");
  assert.equal(el.querySelector(".as-actions-error").textContent, "Could not remove the task: internal error");

  el = mount("pending");
  fetchSays(() => {
    throw new Error("Failed to fetch");
  });
  await click(el, "Cancel");
  assert.equal(el.querySelector(".as-actions-error").textContent, "Could not stop the task: Failed to fetch");
});

test("a later success clears an earlier error", async () => {
  const el = mount("pending");
  let fail = true;
  fetchSays(() => (fail ? jsonResponse(500, { error: { message: "boom" } }) : jsonResponse(200, task({ status: "cancelled" }))));
  await click(el, "Cancel");
  assert.match(el.querySelector(".as-actions-error").textContent, /boom/);
  fail = false;
  await click(el, "Cancel");
  assert.equal(el.querySelector(".as-actions-error").hidden, true);
});

test("giving the element a new task replaces the buttons and disarms Remove", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const el = mount("failed");
  await click(el, "Remove");
  el.task = task({ id: ID, status: "succeeded" });
  assert.deepEqual(labels(el), ["Remove"]);
});

test("the task id is encoded into the address", async () => {
  reset();
  const el = document.createElement("as-task-actions");
  el.task = task({ id: "a/b?c", status: "pending" });
  document.body.appendChild(el);
  const sent = fetchSays(() => jsonResponse(200, task()));
  await click(el, "Cancel");
  assert.equal(sent[0].url, "/api/v1/tasks/a%2Fb%3Fc/cancel");
});
