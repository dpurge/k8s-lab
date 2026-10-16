import { test, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, flush, jsonResponse, task } from "../testenv.js";

await load("format.js", "api.js", "components/as-answer-box/component.js");

afterEach(() => reset());

const ID = "11111111-2222-4333-8444-555555555555";

function mount(ask = { question: "Which language do you want to learn?" }) {
  reset();
  const el = document.createElement("as-answer-box");
  el.setAttribute("task-id", ID);
  el.ask = ask;
  document.body.appendChild(el);
  return el;
}

function fetchSays(handler) {
  const sent = [];
  global.fetch = async (url, options) => {
    sent.push({ url: String(url), options });
    return handler(String(url), options);
  };
  return sent;
}

const type = (el, text) => {
  el.querySelector("textarea").value = text;
};
const send = async (el) => {
  el.querySelector("button").click();
  await flush();
};

test("shows the question where it can be read and the full payload behind a click", () => {
  const el = mount({ question: "Which language?", options: ["Arabic", "Hebrew"] });
  assert.equal(el.querySelector(".as-answer-question").textContent, "Which language?");
  assert.equal(el.querySelector(".as-answer-question").hidden, false);
  assert.match(el.querySelector(".as-answer-raw-json").textContent, /"options"/);
  assert.equal(el.querySelector("details").open, false);
});

test("a payload without a readable question shows the whole payload open", () => {
  const el = mount({ exercise: 3, items: ["a", "b"] });
  assert.equal(el.querySelector(".as-answer-question").hidden, true);
  assert.equal(el.querySelector("details").open, true);
  assert.match(el.querySelector(".as-answer-raw-json").textContent, /"exercise": 3/);
});

test("questions are read from question, text, prompt or message, and shown as text", () => {
  for (const key of ["question", "text", "prompt", "message"]) {
    const el = mount({ [key]: `via ${key}` });
    assert.equal(el.querySelector(".as-answer-question").textContent, `via ${key}`);
  }
  const el = mount({ question: "<img src=x onerror=alert(1)>" });
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector(".as-answer-question").textContent, "<img src=x onerror=alert(1)>");
});

test("sending posts the answer as the message of the task and announces the accepted task", async () => {
  const el = mount();
  const sent = fetchSays(() => jsonResponse(200, task({ status: "pending" })));
  const answered = [];
  el.addEventListener("as-answered", (e) => answered.push(e.detail.task.status));
  type(el, "Arabic");
  await send(el);
  assert.equal(sent.length, 1);
  assert.equal(sent[0].url, `/api/v1/tasks/${ID}/messages`);
  assert.equal(sent[0].options.method, "POST");
  assert.deepEqual(JSON.parse(sent[0].options.body), { message: "Arabic" });
  assert.deepEqual(answered, ["pending"]);
  assert.equal(el.querySelector("textarea").value, "", "an accepted answer clears the box");
  assert.equal(el.querySelector(".as-answer-error").textContent, "");
});

test("an answer is sent exactly as typed, with its line breaks", async () => {
  const el = mount();
  const sent = fetchSays(() => jsonResponse(200, task()));
  type(el, "line one\nعربي\n  indented");
  await send(el);
  assert.deepEqual(JSON.parse(sent[0].options.body), { message: "line one\nعربي\n  indented" });
});

test("an empty answer is refused with a message and nothing is sent", async () => {
  const el = mount();
  const sent = fetchSays(() => jsonResponse(200, task()));
  type(el, "   \n");
  await send(el);
  assert.equal(sent.length, 0);
  assert.equal(el.querySelector(".as-answer-error").textContent, "Type an answer first.");
});

test("a refused answer shows the server's message next to the box and keeps what was typed", async () => {
  const el = mount();
  fetchSays(() => jsonResponse(409, { error: { code: "conflict", message: "the task is not waiting for an answer (it may already have been answered)" } }));
  const answered = [];
  el.addEventListener("as-answered", () => answered.push(1));
  type(el, "Arabic");
  await send(el);
  assert.equal(el.querySelector(".as-answer-error").textContent, "Could not send the answer: the task is not waiting for an answer (it may already have been answered)");
  assert.equal(el.querySelector(".as-answer-error").hidden, false);
  assert.equal(el.querySelector("textarea").value, "Arabic");
  assert.deepEqual(answered, []);
  assert.equal(el.querySelector("button").disabled, false, "one can try again");
});

test("a network failure is shown too, not swallowed", async () => {
  const el = mount();
  fetchSays(() => {
    throw new Error("Failed to fetch");
  });
  type(el, "x");
  await send(el);
  assert.equal(el.querySelector(".as-answer-error").textContent, "Could not send the answer: Failed to fetch");
  assert.equal(el.querySelector("textarea").value, "x");
});

test("an error from a body that is not the API's shape still says something", async () => {
  const el = mount();
  fetchSays(() => ({ ok: false, status: 502, json: async () => { throw new SyntaxError("not json"); } }));
  type(el, "x");
  await send(el);
  assert.equal(el.querySelector(".as-answer-error").textContent, "Could not send the answer: HTTP 502");
});

test("a second click while sending does not send twice, and the button is disabled meanwhile", async () => {
  const el = mount();
  let release;
  const sent = fetchSays(() => new Promise((resolve) => (release = () => resolve(jsonResponse(200, task())))));
  type(el, "x");
  el.querySelector("button").click();
  el.querySelector("button").click();
  assert.equal(el.querySelector("button").disabled, true);
  assert.equal(sent.length, 1);
  release();
  await flush();
  assert.equal(el.querySelector("button").disabled, false);
});

test("a later success clears the error of an earlier failure", async () => {
  const el = mount();
  let fail = true;
  fetchSays(() => (fail ? jsonResponse(500, { error: { message: "boom" } }) : jsonResponse(200, task())));
  type(el, "x");
  await send(el);
  assert.match(el.querySelector(".as-answer-error").textContent, /boom/);
  fail = false;
  await send(el);
  assert.equal(el.querySelector(".as-answer-error").textContent, "");
});

test("Ctrl+Enter sends", async () => {
  const el = mount();
  const sent = fetchSays(() => jsonResponse(200, task()));
  type(el, "x");
  el.querySelector("textarea").dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
  await flush();
  assert.equal(sent.length, 1);
});

test("a plain Enter only adds a line", async () => {
  const el = mount();
  const sent = fetchSays(() => jsonResponse(200, task()));
  type(el, "x");
  el.querySelector("textarea").dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  await flush();
  assert.equal(sent.length, 0);
});

test("the task id is encoded into the address", async () => {
  reset();
  const el = document.createElement("as-answer-box");
  el.setAttribute("task-id", "a/b?c");
  el.ask = { question: "q" };
  document.body.appendChild(el);
  const sent = fetchSays(() => jsonResponse(200, task()));
  type(el, "x");
  await send(el);
  assert.equal(sent[0].url, "/api/v1/tasks/a%2Fb%3Fc/messages");
});
