import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, user, conversation, chatMessage, tasksApi, USER_ID } from "../testenv.js";

await load("format.js", "api.js", "components/as-chat-thread/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const C1 = "cccccccc-1111-4222-8333-444444444444";
const TASK = "11111111-2222-4333-8444-555555555555";

async function mount(api = {}, attrs = { "conversation-id": C1 }, handler = tasksApi({ conversations: [conversation({ id: C1 })], ...api })) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-chat-thread");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

const bubbles = (el) => [...el.querySelectorAll(".as-msg")].map((m) => [m.className.split(" ").slice(1).join(" "), m.querySelector(".as-msg-text").textContent]);
const sendError = (el) => el.querySelector(".as-thread-send-error");
const type = (el, text) => {
  el.querySelector("textarea").value = text;
};

test("shows the messages in order, the user's on one side and the assistant's by kind", async () => {
  const { el } = await mount({
    chatMessages: [
      chatMessage({ id: 1, role: "user", text: "hi" }),
      chatMessage({ id: 2, kind: "question", text: "What is your name?" }),
      chatMessage({ id: 3, kind: "result", text: "Hello, Ada!" }),
      chatMessage({ id: 4, kind: "error", text: "⚠ The run failed: boom" }),
      chatMessage({ id: 5, kind: "info", text: "The run was cancelled." }),
    ],
  });
  assert.deepEqual(bubbles(el), [
    ["as-msg-user as-kind-text", "hi"],
    ["as-msg-assistant as-kind-question", "What is your name?"],
    ["as-msg-assistant as-kind-result", "Hello, Ada!"],
    ["as-msg-assistant as-kind-error", "⚠ The run failed: boom"],
    ["as-msg-assistant as-kind-info", "The run was cancelled."],
  ]);
  assert.equal(el.querySelector(".as-msg-user .as-msg-who").textContent, "You");
});

test("the header names the conversation, the user and the state of the run, with a link to the task", async () => {
  const { el } = await mount({ conversations: [conversation({ id: C1, workflow_id: "learn-topic", title: "Arabic", task_id: TASK, task_status: "waiting" })] });
  assert.equal(el.querySelector(".as-thread-title").textContent, "Arabic");
  assert.equal(el.querySelector(".as-thread-meta").textContent, "learn-topic · Ada");
  assert.equal(el.querySelector(".as-thread-status").textContent, "The assistant is waiting for your answer.");
  assert.ok(el.querySelector(".as-thread-status").classList.contains("as-state-waiting"));
  assert.equal(el.querySelector(".as-thread-task").getAttribute("href"), `#/tasks/${TASK}`);
  assert.equal(el.querySelector(".as-thread-task").hidden, false);
});

test("a conversation with no run yet invites the first message and has no task link", async () => {
  const { el } = await mount();
  assert.equal(el.querySelector(".as-thread-status").textContent, "Write a message to start.");
  assert.equal(el.querySelector(".as-thread-task").hidden, true);
});

test("each state of the run is explained in words", async () => {
  for (const [status, text] of [["pending", /starting/], ["running", /working/], ["failed", /failed/], ["cancelled", /cancelled/], ["succeeded", /finished/]]) {
    const { el } = await mount({ conversations: [conversation({ id: C1, task_id: TASK, task_status: status })] });
    assert.match(el.querySelector(".as-thread-status").textContent, text, status);
  }
});

test("while the assistant works the box is closed; otherwise it is open", async () => {
  for (const status of ["pending", "running"]) {
    const { el } = await mount({ conversations: [conversation({ id: C1, task_id: TASK, task_status: status })] });
    assert.equal(el.querySelector("button.as-thread-send").disabled, true, status);
  }
  for (const status of [null, "waiting", "failed", "succeeded", "cancelled"]) {
    const { el } = await mount({ conversations: [conversation({ id: C1, task_id: status ? TASK : null, task_status: status })] });
    assert.equal(el.querySelector("button.as-thread-send").disabled, false, String(status));
  }
});

test("sending posts the text as typed, clears the box and shows the new messages at once", async () => {
  let messages = [];
  const sent = [];
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") {
      sent.push({ url, body: JSON.parse(options.body) });
      messages = [chatMessage({ id: 1, role: "user", text: "line one\nعربي" })];
      return jsonResponse(201, messages[0]);
    }
    return tasksApi({ conversations: [conversation({ id: C1 })], chatMessages: messages })(url);
  });
  type(el, "line one\nعربي");
  el.querySelector("button.as-thread-send").click();
  await flush();
  await flush();
  assert.deepEqual(sent, [{ url: `/api/v1/conversations/${C1}/messages`, body: { text: "line one\nعربي" } }]);
  assert.equal(el.querySelector("textarea").value, "");
  assert.deepEqual(bubbles(el), [["as-msg-user as-kind-text", "line one\nعربي"]]);
});

test("Enter sends and Shift+Enter only adds a line", async () => {
  const sent = [];
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") {
      sent.push(1);
      return jsonResponse(201, chatMessage({ role: "user" }));
    }
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  type(el, "x");
  el.querySelector("textarea").dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }));
  await flush();
  assert.equal(sent.length, 0);
  el.querySelector("textarea").dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  await flush();
  assert.equal(sent.length, 1);
});

test("an empty message is refused with a note and nothing is sent", async () => {
  const sent = [];
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") sent.push(1);
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  type(el, "  \n");
  el.querySelector("button.as-thread-send").click();
  await flush();
  assert.equal(sendError(el).textContent, "Write something first.");
  assert.equal(sent.length, 0);
});

test("a refused message shows the server's reason and keeps what was typed", async () => {
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") return jsonResponse(409, { error: { code: "conflict", message: "the assistant is still working on your last message" } });
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  type(el, "impatient");
  el.querySelector("button.as-thread-send").click();
  await flush();
  assert.equal(sendError(el).textContent, "Could not send the message: the assistant is still working on your last message");
  assert.equal(sendError(el).hidden, false);
  assert.equal(el.querySelector("textarea").value, "impatient");
});

test("a message stored but not started (502) shows the reason and the conversation's own error message", async () => {
  let messages = [];
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") {
      messages = [chatMessage({ id: 1, role: "user", text: "go" }), chatMessage({ id: 2, kind: "error", text: "⚠ could not start the workflow \"w\": the queue is down" })];
      return jsonResponse(502, { error: { code: "internal", message: "could not start the workflow \"w\": the queue is down" } });
    }
    return tasksApi({ conversations: [conversation({ id: C1 })], chatMessages: messages })(url);
  });
  type(el, "go");
  el.querySelector("button.as-thread-send").click();
  await flush();
  await flush();
  assert.match(sendError(el).textContent, /the queue is down/);
  assert.equal(bubbles(el).length, 2);
  assert.ok(el.querySelector(".as-kind-error"));
});

test("a network failure while sending is shown", async () => {
  const { el } = await mount({}, { "conversation-id": C1 }, (url, options) => {
    if (options && options.method === "POST") throw new Error("Failed to fetch");
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  type(el, "x");
  el.querySelector("button.as-thread-send").click();
  await flush();
  assert.equal(sendError(el).textContent, "Could not send the message: Failed to fetch");
});

test("it polls quickly while the run works and fetches only what is new", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let messages = [chatMessage({ id: 1, role: "user", text: "hi" })];
  let status = "running";
  const { el, calls } = await mount({}, { "conversation-id": C1 }, (url) =>
    tasksApi({ conversations: [conversation({ id: C1, task_id: TASK, task_status: status })], chatMessages: messages })(url));
  assert.equal(bubbles(el).length, 1);
  messages = [...messages, chatMessage({ id: 2, kind: "question", text: "What is your name?" })];
  status = "waiting";
  mock.timers.tick(1500);
  await flush();
  await flush();
  assert.equal(bubbles(el).length, 2);
  assert.ok(calls.some((u) => u.endsWith("/messages?after=1")), "only what is new is asked for");
  assert.equal(el.querySelector("button.as-thread-send").disabled, false, "the box opens when the run waits");
  const count = calls.length;
  mock.timers.tick(1500);
  await flush();
  assert.equal(calls.length, count, "after the run stopped working the polling is slower");
  mock.timers.tick(5000);
  await flush();
  assert.ok(calls.length > count);
});

test("a message is never shown twice, even if it comes back again", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const all = [chatMessage({ id: 1, text: "once" })];
  const { el } = await mount({}, { "conversation-id": C1 }, (url) => {
    if (/messages/.test(url)) return jsonResponse(200, { items: all });
    return tasksApi({ conversations: [conversation({ id: C1, task_status: "running", task_id: TASK })] })(url);
  });
  mock.timers.tick(1500);
  await flush();
  await flush();
  assert.equal(bubbles(el).length, 1);
});

test("refresh-ms 0 turns polling off", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { calls } = await mount({ conversations: [conversation({ id: C1, task_id: TASK, task_status: "running" })] }, { "conversation-id": C1, "refresh-ms": 0 });
  const count = calls.length;
  mock.timers.tick(60000);
  await flush();
  assert.equal(calls.length, count);
});

test("another conversation replaces the first one's messages", async () => {
  const C2 = "dddddddd-1111-4222-8333-444444444444";
  const { el } = await mount({
    conversations: [conversation({ id: C1, title: "first" }), conversation({ id: C2, title: "second" })],
    chatMessages: [chatMessage({ id: 1, text: "shared message list" })],
  });
  assert.equal(el.querySelector(".as-thread-title").textContent, "first");
  el.setAttribute("conversation-id", C2);
  await flush();
  await flush();
  assert.equal(el.querySelector(".as-thread-title").textContent, "second");
  assert.equal(bubbles(el).length, 1);
});

test("with no conversation chosen it says so and the box is closed", async () => {
  const { el } = await mount({}, {});
  assert.match(el.querySelector(".as-thread-none").textContent, /Pick a conversation/);
  assert.equal(el.querySelector("textarea").disabled, true);
  assert.equal(el.querySelector("button.as-thread-send").disabled, true);
  assert.equal(el.querySelector(".as-thread-remove").hidden, true);
});

test("a conversation that does not exist is said, and not polled again", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const events = [];
  reset();
  const calls = mockFetch(tasksApi({ chatStatus: 404 }));
  const el = document.createElement("as-chat-thread");
  el.setAttribute("conversation-id", C1);
  el.addEventListener("as-load", (e) => events.push(e.detail));
  document.body.appendChild(el);
  await flush();
  assert.match(el.querySelector(".as-thread-error").textContent, /does not exist/);
  assert.equal(events[0].ok, false);
  assert.equal(events[0].live, false);
  const count = calls.length;
  mock.timers.tick(60000);
  await flush();
  assert.equal(calls.length, count);
});

test("any other load failure is shown with the server's message and retried", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el, calls } = await mount({ chatStatus: 500 });
  assert.equal(el.querySelector(".as-thread-error").textContent, "Could not load the conversation: internal error");
  const count = calls.length;
  mock.timers.tick(6000);
  await flush();
  assert.ok(calls.length > count);
});

test("deleting takes two clicks and announces the removal", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const deleted = [];
  const removed = [];
  const { el } = await mount({}, { "conversation-id": C1, "refresh-ms": 0 }, (url, options) => {
    if (options && options.method === "DELETE") {
      deleted.push(url);
      return { ok: true, status: 204 };
    }
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  el.addEventListener("as-conversation-removed", (e) => removed.push(e.detail.id));
  const button = el.querySelector("button.as-thread-remove");
  button.click();
  await flush();
  assert.deepEqual(deleted, []);
  assert.equal(button.textContent, "Click again to delete");
  button.click();
  await flush();
  assert.deepEqual(deleted, [`/api/v1/conversations/${C1}`]);
  assert.deepEqual(removed, [C1]);
});

test("an armed delete disarms by itself, and a refused delete is shown", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  const { el } = await mount({}, { "conversation-id": C1, "refresh-ms": 0 }, (url, options) => {
    if (options && options.method === "DELETE") return jsonResponse(500, { error: { message: "boom" } });
    return tasksApi({ conversations: [conversation({ id: C1 })] })(url);
  });
  const button = el.querySelector("button.as-thread-remove");
  button.click();
  mock.timers.tick(4000);
  assert.equal(button.textContent, "Delete");
  button.click();
  await flush();
  button.click();
  await flush();
  assert.equal(sendError(el).textContent, "Could not delete the conversation: boom");
});

test("messages reach the page as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)><script>alert(1)</script>";
  const { el } = await mount({ chatMessages: [chatMessage({ id: 1, text: evil }), chatMessage({ id: 2, role: "user", text: evil })], conversations: [conversation({ id: C1, title: evil })] });
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector("script"), null);
  assert.equal(bubbles(el)[0][1], evil);
  assert.equal(el.querySelector(".as-thread-title").textContent, evil);
});

test("loading marks the element busy until it is done", async () => {
  reset();
  let release;
  mockFetch((url) => (url === `/api/v1/conversations/${C1}` ? new Promise((r) => (release = () => r(jsonResponse(200, conversation({ id: C1 }))))) : tasksApi({ chatMessages: [] })(url)));
  const el = document.createElement("as-chat-thread");
  el.setAttribute("conversation-id", C1);
  document.body.appendChild(el);
  assert.equal(el.getAttribute("aria-busy"), "true");
  release();
  await flush();
  await flush();
  assert.equal(el.hasAttribute("aria-busy"), false);
});
