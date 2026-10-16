import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, task, user, USER_ID, DEFAULT_WORKFLOWS } from "../testenv.js";

await load("format.js", "api.js", "components/as-new-task/component.js");

afterEach(() => reset());

const BOB = "bbbbbbbb-0000-4000-8000-000000000002";

async function mount({ users = [user(USER_ID, "Ada"), user(BOB, "Bob")], workflows = DEFAULT_WORKFLOWS, workflowsStatus = 200, onPost = () => jsonResponse(201, task()) } = {}) {
  reset();
  const sent = [];
  mockFetch((url, options) => {
    if (options && options.method === "POST") {
      sent.push({ url, body: JSON.parse(options.body) });
      return onPost(url, options);
    }
    if (url.startsWith("/api/v1/workflows")) return workflowsStatus === 200 ? jsonResponse(200, { items: workflows }) : jsonResponse(workflowsStatus, { error: { message: "no workflows for you" } });
    if (url.startsWith("/api/v1/users")) return users === null ? jsonResponse(500, { error: { message: "no users for you" } }) : jsonResponse(200, { items: users });
    return jsonResponse(404, { error: { message: "no such route" } });
  });
  const el = document.createElement("as-new-task");
  document.body.appendChild(el);
  await flush();
  return { el, sent };
}

const click = async (el) => {
  el.querySelector("button").click();
  await flush();
};
const error = (el) => el.querySelector(".as-new-task-error");
const choose = (el, id) => {
  const select = el.querySelector("select.as-new-task-workflow");
  select.value = id;
  select.dispatchEvent(new window.Event("change"));
};

test("offers every workflow the server lists, including learn-topic, and starts on hello", async () => {
  const { el } = await mount();
  const select = el.querySelector("select.as-new-task-workflow");
  assert.deepEqual([...select.options].map((o) => o.value), ["ask", "hello", "learn-topic", "noop"]);
  assert.equal(select.value, "hello");
  assert.equal(el.querySelector(".as-new-task-about").textContent, "Asks for a name, then greets.");
  assert.deepEqual(JSON.parse(el.querySelector("textarea").value), { message: "start" });
});

test("offers the users by name", async () => {
  const { el } = await mount();
  assert.deepEqual([...el.querySelectorAll("select.as-new-task-user option")].map((o) => [o.value, o.textContent]), [[USER_ID, "Ada"], [BOB, "Bob"]]);
  assert.equal(error(el).hidden, true);
});

test("picking another workflow describes it and offers a payload that works for it", async () => {
  const { el } = await mount();
  choose(el, "ask");
  assert.equal(el.querySelector(".as-new-task-about").textContent, "One call to a model.");
  assert.deepEqual(JSON.parse(el.querySelector("textarea").value), { prompt: "Say ready", tags: ["chat"] });
  choose(el, "noop");
  assert.deepEqual(JSON.parse(el.querySelector("textarea").value), { sleep_ms: 1500 });
});

test("without a hello workflow it starts on the first one", async () => {
  const { el } = await mount({ workflows: DEFAULT_WORKFLOWS.filter((w) => w.id !== "hello") });
  assert.equal(el.querySelector("select.as-new-task-workflow").value, "ask");
});

test("starting posts the chosen user, workflow and payload and announces the task", async () => {
  const created = [];
  const { el, sent } = await mount({ onPost: () => jsonResponse(201, task({ id: "99999999-2222-4333-8444-555555555555" })) });
  el.addEventListener("as-task-created", (e) => created.push(e.detail.task.id));
  el.querySelector("select.as-new-task-user").value = BOB;
  choose(el, "learn-topic");
  el.querySelector("textarea").value = '{"message": "I want to learn Arabic", "language": "eng"}';
  await click(el);
  assert.equal(sent.length, 1);
  assert.equal(sent[0].url, "/api/v1/tasks");
  assert.deepEqual(sent[0].body, { user_id: BOB, workflow_id: "learn-topic", payload: { message: "I want to learn Arabic", language: "eng" } });
  assert.deepEqual(created, ["99999999-2222-4333-8444-555555555555"]);
  assert.equal(error(el).hidden, true);
});

test("a payload that is not JSON is refused with the parser's reason and nothing is sent", async () => {
  const { el, sent } = await mount();
  el.querySelector("textarea").value = "{message: start}";
  await click(el);
  assert.equal(sent.length, 0);
  assert.match(error(el).textContent, /^The payload is not valid JSON: /);
  assert.equal(error(el).hidden, false);
});

test("a payload that is JSON but not an object is refused", async () => {
  const { el, sent } = await mount();
  for (const text of ["[1]", '"x"', "5", "null", "true"]) {
    el.querySelector("textarea").value = text;
    await click(el);
    assert.equal(error(el).textContent, "The payload must be a JSON object.", text);
  }
  assert.equal(sent.length, 0);
});

test("with no workflow to pick the form says so and starts nothing", async () => {
  const { el, sent } = await mount({ workflows: [] });
  assert.equal(error(el).textContent, "The server lists no workflows.");
  await click(el);
  assert.equal(error(el).textContent, "Pick a workflow.");
  assert.equal(sent.length, 0);
});

test("failing to load the workflows is shown", async () => {
  const { el } = await mount({ workflowsStatus: 500 });
  assert.equal(error(el).textContent, "Could not load the workflows: no workflows for you");
});

test("a refused task shows the server's message and keeps the form as it was", async () => {
  const { el } = await mount({ onPost: () => jsonResponse(404, { error: { code: "not_found", message: "user not found" } }) });
  el.querySelector("textarea").value = '{"message": "keep me"}';
  await click(el);
  assert.equal(error(el).textContent, "Could not start the task: user not found");
  assert.equal(error(el).hidden, false);
  assert.equal(el.querySelector("textarea").value, '{"message": "keep me"}');
  assert.equal(el.querySelector("button").disabled, false, "one can try again");
});

test("a network failure is shown, not swallowed", async () => {
  const { el } = await mount({ onPost: () => { throw new Error("Failed to fetch"); } });
  await click(el);
  assert.equal(error(el).textContent, "Could not start the task: Failed to fetch");
});

test("failing to load the users is shown, and starting says to pick a user", async () => {
  const { el, sent } = await mount({ users: null });
  assert.equal(error(el).textContent, "Could not load the users: no users for you");
  await click(el);
  assert.equal(error(el).textContent, "Pick a user.");
  assert.equal(sent.length, 0);
});

test("no users at all says how to create one", async () => {
  const { el } = await mount({ users: [] });
  assert.match(error(el).textContent, /There are no users yet/);
});

test("a later success clears the earlier error", async () => {
  let fail = true;
  const { el } = await mount({ onPost: () => (fail ? jsonResponse(500, { error: { message: "boom" } }) : jsonResponse(201, task())) });
  await click(el);
  assert.match(error(el).textContent, /boom/);
  fail = false;
  await click(el);
  assert.equal(error(el).hidden, true);
  assert.equal(error(el).textContent, "");
});

test("a second click while sending does not start two tasks", async () => {
  let release;
  const { el, sent } = await mount({ onPost: () => new Promise((resolve) => (release = () => resolve(jsonResponse(201, task())))) });
  el.querySelector("button").click();
  el.querySelector("button").click();
  assert.equal(el.querySelector("button").disabled, true);
  assert.equal(sent.length, 1);
  release();
  await flush();
  assert.equal(el.querySelector("button").disabled, false);
});

test("user and workflow names reach the page as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)>";
  const { el } = await mount({ users: [user(USER_ID, evil)], workflows: [{ id: "w", description: evil, kind: "yaml", payload_hint: "{}" }] });
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("select.as-new-task-user option").textContent, evil);
  assert.equal(el.querySelector(".as-new-task-about").textContent, evil);
});
