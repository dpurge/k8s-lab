import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, user, conversation, tasksApi, USER_ID, DEFAULT_WORKFLOWS } from "../testenv.js";

await load("format.js", "api.js", "components/as-chat-list/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
});

const C1 = "cccccccc-1111-4222-8333-444444444444";
const C2 = "dddddddd-1111-4222-8333-444444444444";
const BOB = "bbbbbbbb-0000-4000-8000-000000000002";

async function mount(attrs = {}, api = {}, handler = tasksApi(api)) {
  reset();
  const calls = mockFetch(handler);
  const el = document.createElement("as-chat-list");
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  document.body.appendChild(el);
  await flush();
  return { el, calls };
}

const items = (el) => [...el.querySelectorAll("li.as-chat-item")];
const formError = (el) => el.querySelector(".as-chat-new-error");

test("lists the conversations as links to their pages, with the last words and the user", async () => {
  const { el } = await mount({}, {
    conversations: [
      conversation({ id: C1, workflow_id: "hello", last_text: "Hello, Ada!\nsecond   line", task_status: "succeeded", messages: 4 }),
      conversation({ id: C2, workflow_id: "learn-topic", title: "Arabic", user_id: BOB, last_text: "Which level?", task_status: "waiting" }),
    ],
    users: [user(USER_ID, "Ada"), user(BOB, "Bob")],
  });
  const [first, second] = items(el);
  assert.equal(first.querySelector("a").getAttribute("href"), `#/chat/${C1}`);
  assert.equal(first.querySelector(".as-chat-item-title").textContent, "hello");
  assert.equal(first.querySelector(".as-chat-item-meta").textContent, "Ada");
  assert.equal(first.querySelector(".as-chat-item-last").textContent, "Hello, Ada! second line", "a multi-line message is squeezed onto one line");
  assert.equal(first.querySelector(".as-badge"), null, "a finished run needs no badge");
  assert.equal(second.querySelector(".as-chat-item-title").textContent, "Arabic");
  assert.equal(second.querySelector(".as-chat-item-meta").textContent, "learn-topic · Bob");
  assert.equal(second.querySelector(".as-badge").className, "as-badge as-status-waiting");
});

test("a conversation with nothing said says so, and an empty list says how to start", async () => {
  const { el } = await mount({}, { conversations: [conversation({ id: C1 })] });
  assert.equal(el.querySelector(".as-chat-item-last").textContent, "(nothing said yet)");
  const empty = await mount({}, { conversations: [] });
  assert.match(empty.el.querySelector(".as-chat-list-message").textContent, /No conversations yet/);
});

test("the active conversation is marked, and changing it moves the mark", async () => {
  const { el } = await mount({ active: C2 }, { conversations: [conversation({ id: C1 }), conversation({ id: C2 })] });
  assert.deepEqual(items(el).map((li) => li.classList.contains("active")), [false, true]);
  el.setAttribute("active", C1);
  assert.deepEqual(items(el).map((li) => li.classList.contains("active")), [true, false]);
});

test("the form offers the users and only the workflows a conversation can run", async () => {
  const { el } = await mount();
  assert.deepEqual([...el.querySelectorAll("select.as-chat-new-user option")].map((o) => o.textContent), ["Ada"]);
  assert.deepEqual([...el.querySelectorAll("select.as-chat-new-workflow option")].map((o) => o.value), ["ask", "hello", "learn-topic"], "noop cannot hold a conversation");
});

test("hello-live is preselected when it exists, else hello", async () => {
  const withLive = [...DEFAULT_WORKFLOWS, { id: "hello-live", description: "live", kind: "yaml", payload_hint: "{}" }];
  assert.equal((await mount({}, { workflows: withLive })).el.querySelector("select.as-chat-new-workflow").value, "hello-live");
  assert.equal((await mount()).el.querySelector("select.as-chat-new-workflow").value, "hello");
});

test("starting a conversation posts the choices and announces it", async () => {
  const created = [];
  const sent = [];
  const { el } = await mount({}, {}, (url, options) => {
    if (options && options.method === "POST") {
      sent.push(JSON.parse(options.body));
      return jsonResponse(201, conversation({ id: C2, workflow_id: "learn-topic", title: "Arabic" }));
    }
    return tasksApi({})(url);
  });
  el.addEventListener("as-conversation-created", (e) => created.push(e.detail.conversation.id));
  el.querySelector("select.as-chat-new-workflow").value = "learn-topic";
  el.querySelector("input.as-chat-new-title").value = "  Arabic ";
  el.querySelector("button.as-chat-new-start").click();
  await flush();
  assert.deepEqual(sent, [{ user_id: USER_ID, workflow_id: "learn-topic", title: "Arabic" }]);
  assert.deepEqual(created, [C2]);
  assert.equal(el.querySelector("input.as-chat-new-title").value, "");
});

test("a refused start shows the server's message, a network failure too", async () => {
  let mode = "refuse";
  const { el } = await mount({}, {}, (url, options) => {
    if (options && options.method === "POST") {
      if (mode === "network") throw new Error("Failed to fetch");
      return jsonResponse(400, { error: { code: "invalid_request", message: "a conversation can run a YAML workflow or ask, not this one" } });
    }
    return tasksApi({})(url);
  });
  el.querySelector("button.as-chat-new-start").click();
  await flush();
  assert.equal(formError(el).textContent, "Could not start the conversation: a conversation can run a YAML workflow or ask, not this one");
  assert.equal(formError(el).hidden, false);
  mode = "network";
  el.querySelector("button.as-chat-new-start").click();
  await flush();
  assert.equal(formError(el).textContent, "Could not start the conversation: Failed to fetch");
  assert.equal(el.querySelector("button.as-chat-new-start").disabled, false);
});

test("no users, no workflows, or a form that cannot load are all said", async () => {
  assert.match(formError((await mount({}, { users: [] })).el).textContent, /There are no users yet/);
  assert.match(formError((await mount({}, { workflows: [DEFAULT_WORKFLOWS[3]] })).el).textContent, /offers no workflow a conversation can run/);
  assert.match(formError((await mount({}, { workflowsStatus: 500 })).el).textContent, /^Could not load the form: internal error/);
});

test("a load failure shows the server's message and as-load reports it", async () => {
  reset();
  mockFetch(tasksApi({ conversationsStatus: 500 }));
  const events = [];
  const el = document.createElement("as-chat-list");
  el.addEventListener("as-load", (e) => events.push(e.detail));
  document.body.appendChild(el);
  await flush();
  assert.equal(el.querySelector(".as-chat-list-message").textContent, "Could not load the conversations: internal error");
  assert.equal(events[0].ok, false);
  assert.equal(events[0].live, true, "it keeps trying");
});

test("it refreshes itself so a reply shows in the list, and stops when removed", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  let last = "one";
  const { el, calls } = await mount({}, {}, (url) => (url.startsWith("/api/v1/conversations") ? jsonResponse(200, { items: [conversation({ id: C1, last_text: last })] }) : tasksApi({})(url)));
  assert.equal(el.querySelector(".as-chat-item-last").textContent, "one");
  last = "two";
  mock.timers.tick(4000);
  await flush();
  assert.equal(el.querySelector(".as-chat-item-last").textContent, "two");
  const before = calls.filter((u) => u === "/api/v1/conversations").length;
  el.remove();
  mock.timers.tick(20000);
  await flush();
  assert.equal(calls.filter((u) => u === "/api/v1/conversations").length, before);
});

test("titles, workflow names and last words reach the page as text, never as markup", async () => {
  const evil = "<img src=x onerror=alert(1)>";
  const { el } = await mount({}, { conversations: [conversation({ id: C1, title: evil, last_text: evil })] });
  assert.equal(el.querySelector("img"), null);
  assert.equal(el.querySelector(".as-chat-item-title").textContent, evil);
});

test("a long last message is clipped with an ellipsis", async () => {
  const { el } = await mount({}, { conversations: [conversation({ id: C1, last_text: "x".repeat(300) })] });
  const text = el.querySelector(".as-chat-item-last").textContent;
  assert.equal(text.length, 90);
  assert.ok(text.endsWith("…"));
});
