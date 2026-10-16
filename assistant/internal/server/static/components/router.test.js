import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { JSDOM } from "jsdom";
import { load, staticDir, reset, mockFetch, jsonResponse, flush, task, tasksApi, user, usageRow, modelInfo, conversation, chatMessage, USER_ID } from "./testenv.js";

await load(
  "format.js",
  "api.js",
  "components/as-nav/component.js",
  "components/as-card/component.js",
  "components/as-status-bar/component.js",
  "components/as-pager/component.js",
  "components/as-new-task/component.js",
  "components/as-chat-list/component.js",
  "components/as-chat-thread/component.js",
  "components/as-task-actions/component.js",
  "components/as-task-list/component.js",
  "components/as-answer-box/component.js",
  "components/as-run-trace/component.js",
  "components/as-task-detail/component.js",
  "components/as-usage/component.js",
  "components/as-model-list/component.js",
  "app.js",
);
const App = window.AssistantApp;

const ID = "11111111-2222-4333-8444-555555555555";
const indexHtml = readFileSync(new URL("index.html", staticDir), "utf8");

let stop = null;
afterEach(() => {
  if (stop) stop();
  stop = null;
  window.location.hash = "";
  mock.timers.reset();
  reset();
});

const tick = () => new Promise((resolve) => window.setTimeout(resolve, 10));

// boot puts the page on the document and starts the app. fakeTimers turns on
// the test runner's fake setTimeout only after the address has settled, since
// jsdom's hashchange (awaited by tick) would otherwise never fire.
async function boot(hash = "", handler = tasksApi({ tasks: [task()] }), { fakeTimers = false } = {}) {
  reset();
  document.body.innerHTML = new JSDOM(indexHtml).window.document.body.innerHTML;
  window.location.hash = hash;
  await tick();
  if (fakeTimers) mock.timers.enable({ apis: ["setTimeout"] });
  const calls = mockFetch(handler);
  window.toggleTheme = mock.fn();
  stop = App.start();
  await flush();
  return { calls, view: document.getElementById("view") };
}

// ── routing ──────────────────────────────────────────────────────────────────

test("parseHash: the list, with or without a query", () => {
  for (const h of ["", "#", "#/", "#/tasks", "#/tasks/"]) assert.deepEqual(App.parseHash(h), { view: "tasks", status: "", page: 1 }, h);
  assert.deepEqual(App.parseHash("#/tasks?status=failed&page=3"), { view: "tasks", status: "failed", page: 3 });
  assert.deepEqual(App.parseHash("#/tasks?page=2"), { view: "tasks", status: "", page: 2 });
});

test("parseHash: a bad status or page in the query is ignored", () => {
  assert.deepEqual(App.parseHash("#/tasks?status=waiting"), { view: "tasks", status: "waiting", page: 1 });
  assert.deepEqual(App.parseHash("#/tasks?status=bogus&page=abc"), { view: "tasks", status: "", page: 1 });
  assert.deepEqual(App.parseHash("#/tasks?page=0"), { view: "tasks", status: "", page: 1 });
  assert.deepEqual(App.parseHash("#/tasks?page=-4"), { view: "tasks", status: "", page: 1 });
  assert.deepEqual(App.parseHash("#/tasks?status=failed%26limit%3D5"), { view: "tasks", status: "", page: 1 });
});

test("parseHash: one task by its id", () => {
  assert.deepEqual(App.parseHash(`#/tasks/${ID}`), { view: "task", id: ID });
  assert.deepEqual(App.parseHash(`#/tasks/${encodeURIComponent(ID)}`), { view: "task", id: ID });
});

test("parseHash: anything else is not found", () => {
  for (const h of ["#/users", "#/tasks/nope", `#/tasks/${ID}/extra`, "#/tasks/%E0%A4%A", "#/tasks/<script>", "#/../etc", "#/tasks/' OR 1=1"]) {
    assert.deepEqual(App.parseHash(h), { view: "notfound" }, h);
  }
});

test("hashFor leaves defaults out and round-trips", () => {
  assert.equal(App.hashFor({ view: "tasks", status: "", page: 1 }), "#/tasks");
  assert.equal(App.hashFor({ view: "tasks", status: "failed", page: 1 }), "#/tasks?status=failed");
  assert.equal(App.hashFor({ view: "tasks", status: "", page: 3 }), "#/tasks?page=3");
  assert.equal(App.hashFor({ view: "tasks", status: "failed", page: 3 }), "#/tasks?status=failed&page=3");
  assert.equal(App.hashFor({ view: "task", id: ID }), `#/tasks/${ID}`);
  for (const route of [{ view: "tasks", status: "running", page: 2 }, { view: "tasks", status: "", page: 1 }, { view: "task", id: ID }]) {
    assert.deepEqual(App.parseHash(App.hashFor(route)), route);
  }
});

// ── the page ─────────────────────────────────────────────────────────────────

test("opens on the task list inside a card", async () => {
  const { view, calls } = await boot("");
  assert.deepEqual([...view.querySelectorAll("as-card")].map((c) => c.getAttribute("heading")), ["New task", "Tasks"]);
  const card = view.querySelectorAll("as-card")[1];
  const list = card.querySelector("as-task-list");
  assert.equal(list.getAttribute("status"), "");
  assert.equal(list.getAttribute("page"), "1");
  assert.equal(document.getElementById("mainNav").getAttribute("active"), "tasks");
  assert.ok(calls.some((u) => u.startsWith("/api/v1/tasks?")));
  assert.equal(view.querySelectorAll("tbody tr").length, 1);
});

test("the address decides the status and page of the list", async () => {
  const { calls } = await boot("#/tasks?status=failed&page=2");
  assert.ok(calls.includes("/api/v1/tasks?limit=25&offset=25&status=failed"), calls.join("\n"));
});

test("changing the address updates the list without replacing it", async () => {
  const { view } = await boot("#/tasks");
  const list = view.querySelector("as-task-list");
  window.location.hash = "#/tasks?status=running&page=2";
  await tick();
  assert.equal(view.querySelector("as-task-list"), list, "the same element is reused");
  assert.equal(list.getAttribute("status"), "running");
  assert.equal(list.getAttribute("page"), "2");
});

test("a filter or page chosen in the list becomes the address", async () => {
  const { view } = await boot("");
  const list = view.querySelector("as-task-list");
  list.dispatchEvent(new window.CustomEvent("as-list-change", { detail: { status: "failed", page: 1 }, bubbles: true }));
  await tick();
  assert.equal(window.location.hash, "#/tasks?status=failed");
  assert.equal(list.getAttribute("status"), "failed");
  list.dispatchEvent(new window.CustomEvent("as-list-change", { detail: { status: "failed", page: 4 }, bubbles: true }));
  await tick();
  assert.equal(window.location.hash, "#/tasks?status=failed&page=4");
  assert.equal(list.getAttribute("page"), "4");
});

test("the status filter of the real list drives the address end to end", async () => {
  const { view } = await boot("");
  const select = view.querySelector("select.as-task-list-status");
  select.value = "failed";
  select.dispatchEvent(new window.Event("change", { bubbles: true }));
  await tick();
  assert.equal(window.location.hash, "#/tasks?status=failed");
});

test("a task's address shows the task in a card, and Back returns to the list", async () => {
  const { view } = await boot(`#/tasks/${ID}`, (url) => (url.startsWith("/api/v1/tasks/") ? jsonResponse(200, task()) : tasksApi({ tasks: [task()] })(url)));
  assert.equal(view.querySelector("as-card").getAttribute("heading"), "Task");
  const detail = view.querySelector("as-task-detail");
  assert.equal(detail.getAttribute("task-id"), ID);
  assert.equal(view.querySelector("a.as-task-detail-back").getAttribute("href"), "#/tasks");
  window.location.hash = "#/tasks";
  await tick();
  assert.ok(view.querySelector("as-task-list"));
  assert.equal(view.querySelector("as-task-detail"), null);
});

test("going from one task to another reuses the detail element", async () => {
  const { view } = await boot(`#/tasks/${ID}`, (url) => (url.startsWith("/api/v1/tasks/") ? jsonResponse(200, task()) : jsonResponse(200, { items: [] })));
  const detail = view.querySelector("as-task-detail");
  window.location.hash = "#/tasks/99999999-2222-4333-8444-555555555555";
  await tick();
  assert.equal(view.querySelector("as-task-detail"), detail);
  assert.equal(detail.getAttribute("task-id"), "99999999-2222-4333-8444-555555555555");
});

test("an unknown address shows a not-found page with a way back", async () => {
  const { view, calls } = await boot("#/nonsense");
  assert.equal(view.querySelector("as-card").getAttribute("heading"), "Not found");
  assert.match(view.querySelector(".as-not-found").textContent, /Page not found/);
  assert.equal(view.querySelector(".as-not-found a").getAttribute("href"), "#/tasks");
  assert.equal(calls.length, 0, "nothing is fetched for a page that does not exist");
});

test("a malformed task id never reaches the server", async () => {
  const { calls } = await boot("#/tasks/../../etc/passwd");
  assert.equal(calls.length, 0);
});

// ── status bar and theme ─────────────────────────────────────────────────────

test("a good load shows when the data was last updated", async () => {
  await boot("", tasksApi({ tasks: [task({ status: "running", finished_at: null })] }), { fakeTimers: true });
  const job = document.querySelector(".as-status-job").textContent;
  assert.match(job, /^Updated \d\d:\d\d:\d\d · refreshing$/);
  assert.equal(document.querySelector(".as-status-message").textContent, "");
});

test("without anything to refresh the status bar does not say it is refreshing", async () => {
  await boot("", tasksApi({ tasks: [task()] }));
  assert.match(document.querySelector(".as-status-job").textContent, /^Updated \d\d:\d\d:\d\d$/);
});

test("a failed load is reported in the status bar as an error", async () => {
  await boot("", tasksApi({ tasksStatus: 500 }));
  const message = document.querySelector(".as-status-message");
  assert.equal(message.textContent, "Could not load tasks: internal error");
  assert.ok(message.classList.contains("error"));
});

test("a later good load clears the error in the status bar", async () => {
  let failing = true;
  const ok = tasksApi({ tasks: [task({ status: "running", finished_at: null })] });
  await boot("", (url) => (failing && url.startsWith("/api/v1/tasks") ? jsonResponse(500, { error: { message: "down" } }) : ok(url)), { fakeTimers: true });
  assert.equal(document.querySelector(".as-status-message").textContent, "Could not load tasks: down");
  failing = false;
  mock.timers.tick(3000);
  await flush();
  assert.equal(document.querySelector(".as-status-message").textContent, "");
  assert.equal(document.querySelector(".as-status-message").classList.contains("error"), false);
});

test("the theme button toggles the theme", async () => {
  await boot("");
  document.getElementById("themeToggle").click();
  assert.equal(window.toggleTheme.mock.callCount(), 1);
});

test("stopping detaches every listener", async () => {
  const { view } = await boot("");
  stop();
  stop = null;
  const list = view.querySelector("as-task-list");
  window.location.hash = "#/tasks?status=failed";
  await tick();
  assert.equal(list.getAttribute("status"), "", "no more routing");
  list.dispatchEvent(new window.CustomEvent("as-list-change", { detail: { status: "failed", page: 2 }, bubbles: true }));
  assert.equal(window.location.hash, "#/tasks?status=failed", "no more handling of list changes");
  document.getElementById("themeToggle").click();
  assert.equal(window.toggleTheme.mock.callCount(), 0);
});

test("the page works without a status bar or a nav", async () => {
  reset();
  document.body.innerHTML = '<main id="view"></main>';
  mockFetch(tasksApi({ tasks: [task()] }));
  window.location.hash = "";
  stop = App.start();
  await flush();
  assert.equal(document.querySelectorAll("tbody tr").length, 1);
});

// ── index.html ───────────────────────────────────────────────────────────────

test("index.html loads only files that exist, from the admin prefix", () => {
  const dom = new JSDOM(indexHtml);
  const refs = [...dom.window.document.querySelectorAll("script[src], link[rel=stylesheet]")].map((n) => n.getAttribute("src") || n.getAttribute("href"));
  assert.ok(refs.length >= 12, `${refs.length} references`);
  for (const ref of refs) {
    assert.ok(ref.startsWith("/admin/"), `${ref} must be under /admin/`);
    assert.ok(existsSync(new URL(ref.slice("/admin/".length), staticDir)), `${ref} does not exist`);
  }
});

test("index.html has no inline script, style or event handler, so a strict content security policy can apply", () => {
  const dom = new JSDOM(indexHtml);
  const doc = dom.window.document;
  assert.equal([...doc.querySelectorAll("script")].filter((s) => !s.hasAttribute("src")).length, 0, "inline script");
  assert.equal(doc.querySelectorAll("style").length, 0, "inline style element");
  assert.equal(doc.querySelectorAll("[style]").length, 0, "style attribute");
  for (const node of doc.querySelectorAll("*")) {
    for (const attr of node.attributes) assert.doesNotMatch(attr.name, /^on/i, `${node.tagName} has ${attr.name}`);
  }
});

test("every custom element index.html uses is defined by a script it loads", () => {
  const dom = new JSDOM(indexHtml);
  const doc = dom.window.document;
  const scripts = [...doc.querySelectorAll("script[src]")].map((s) => s.getAttribute("src"));
  for (const tag of new Set([...doc.querySelectorAll("*")].map((n) => n.localName).filter((n) => n.includes("-")))) {
    assert.ok(scripts.some((s) => s.includes(`/${tag}/component.js`)), `${tag} is used but its component.js is not loaded`);
  }
  assert.equal(doc.documentElement.lang, "en");
  assert.ok(doc.querySelector('meta[name="viewport"]'));
});

test("every component script is loaded by index.html, so none is dead", () => {
  const dom = new JSDOM(indexHtml);
  const scripts = [...dom.window.document.querySelectorAll("script[src]")].map((s) => s.getAttribute("src"));
  for (const tag of ["as-nav", "as-card", "as-status-bar", "as-pager", "as-new-task", "as-chat-list", "as-chat-thread", "as-task-actions", "as-task-list", "as-task-detail", "as-answer-box", "as-run-trace", "as-usage", "as-model-list"]) {
    assert.ok(scripts.includes(`/admin/components/${tag}/component.js`), tag);
  }
});

test("the task page asks for its own task once, never for a task with no id", async () => {
  const { calls } = await boot(`#/tasks/${ID}`, (url) => (url.startsWith("/api/v1/tasks/") && !url.endsWith("/llm-calls") && !url.endsWith("/steps") ? jsonResponse(200, task()) : tasksApi({ tasks: [task()] })(url)));
  assert.deepEqual(calls.filter((u) => u.startsWith("/api/v1/tasks") && !u.endsWith("/llm-calls") && !u.endsWith("/steps")), [`/api/v1/tasks/${ID}`]);
});

test("the list page asks for its page once, with the address's status and page already applied", async () => {
  const { calls } = await boot("#/tasks?status=failed&page=2");
  assert.deepEqual(calls.filter((u) => u.startsWith("/api/v1/tasks")), ["/api/v1/tasks?limit=25&offset=25&status=failed"]);
});

// ── usage page ───────────────────────────────────────────────────────────────

test("parseHash: the usage page, with a period that must be known", () => {
  assert.deepEqual(App.parseHash("#/usage"), { view: "usage", period: "all" });
  assert.deepEqual(App.parseHash("#/usage?period=7d"), { view: "usage", period: "7d" });
  assert.deepEqual(App.parseHash("#/usage?period=24h"), { view: "usage", period: "24h" });
  assert.deepEqual(App.parseHash("#/usage?period=bogus"), { view: "usage", period: "all" });
  assert.deepEqual(App.parseHash("#/usage/extra"), { view: "notfound" });
});

test("hashFor: the usage page leaves the default period out and round-trips", () => {
  assert.equal(App.hashFor({ view: "usage", period: "all" }), "#/usage");
  assert.equal(App.hashFor({ view: "usage", period: "30d" }), "#/usage?period=30d");
  for (const route of [{ view: "usage", period: "all" }, { view: "usage", period: "7d" }]) {
    assert.deepEqual(App.parseHash(App.hashFor(route)), route);
  }
});

test("the usage page shows the usage and the models, each in a card", async () => {
  const { view, calls } = await boot("#/usage?period=7d", tasksApi({ usage: [usageRow()], models: [modelInfo()] }));
  assert.deepEqual([...view.querySelectorAll("as-card")].map((c) => c.getAttribute("heading")), ["Usage", "Models"]);
  assert.equal(view.querySelector("as-usage").getAttribute("period"), "7d");
  assert.equal(view.querySelectorAll("as-usage tbody tr").length, 1);
  assert.equal(view.querySelectorAll("as-model-list tbody tr").length, 1);
  assert.equal(calls.filter((u) => u.startsWith("/api/v1/usage")).length, 1, "the period from the address is applied before the first load");
  assert.match(calls.find((u) => u.startsWith("/api/v1/usage")), /since=/);
  assert.equal(calls.some((u) => u.startsWith("/api/v1/tasks")), false);
});

test("the nav marks Usage active on its page and Tasks everywhere else", async () => {
  await boot("#/usage");
  const nav = document.getElementById("mainNav");
  assert.equal(nav.getAttribute("active"), "usage");
  assert.deepEqual([...nav.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("href")]), [["Chat", "#/chat"], ["Tasks", "#/tasks"], ["Usage", "#/usage"]]);
  window.location.hash = `#/tasks/${ID}`;
  await tick();
  assert.equal(nav.getAttribute("active"), "tasks");
  window.location.hash = "#/usage";
  await tick();
  assert.equal(nav.getAttribute("active"), "usage");
});

test("picking a period on the usage page changes the address", async () => {
  const { view } = await boot("#/usage");
  view.querySelector("as-usage").dispatchEvent(new window.CustomEvent("as-list-change", { detail: { period: "30d" }, bubbles: true }));
  assert.equal(window.location.hash, "#/usage?period=30d");
  await tick();
  assert.equal(view.querySelector("as-usage").getAttribute("period"), "30d");
});

test("going between the tasks and the usage pages replaces the view", async () => {
  const { view } = await boot("#/usage");
  window.location.hash = "#/tasks";
  await tick();
  assert.equal(view.querySelector("as-usage"), null);
  assert.ok(view.querySelector("as-task-list"));
  window.location.hash = "#/usage";
  await tick();
  assert.equal(view.querySelector("as-task-list"), null);
  assert.ok(view.querySelector("as-usage"));
});

test("a usage failure is reported in the status bar", async () => {
  await boot("#/usage", tasksApi({ usageStatus: 500 }));
  assert.equal(document.querySelector(".as-status-message").textContent.startsWith("Could not load"), true);
});

test("an error stays in the status bar until the component that raised it loads well", async () => {
  await boot("#/usage", tasksApi({ usageStatus: 500, models: [modelInfo()] }));
  const message = () => document.querySelector(".as-status-message");
  assert.match(message().textContent, /^Could not load usage/, "the models loading well must not hide it");
  assert.ok(message().classList.contains("error"));
  // The models load again, well, while the usage still has not.
  document.querySelector("as-model-list")._load();
  await flush();
  assert.match(message().textContent, /^Could not load usage/);
});

test("when the failing component loads well the error goes", async () => {
  let failing = true;
  const ok = tasksApi({ usage: [usageRow()], models: [modelInfo()] });
  await boot("#/usage", (url) => (failing && url.startsWith("/api/v1/usage") ? jsonResponse(500, { error: { message: "down" } }) : ok(url)));
  assert.equal(document.querySelector(".as-status-message").textContent, "Could not load usage: down");
  failing = false;
  document.querySelector("as-usage").setAttribute("period", "7d");
  await flush();
  assert.equal(document.querySelector(".as-status-message").textContent, "");
});

test("an error of a view that is gone does not outlive it", async () => {
  await boot("#/usage", tasksApi({ usageStatus: 500, models: [modelInfo()], tasks: [task()] }));
  assert.match(document.querySelector(".as-status-message").textContent, /^Could not load usage/);
  window.location.hash = "#/tasks";
  await tick();
  await flush();
  assert.equal(document.querySelector(".as-status-message").textContent, "");
});

test("starting a task opens its page", async () => {
  const { view } = await boot("");
  view.querySelector("as-new-task").dispatchEvent(new window.CustomEvent("as-task-created", { detail: { task: { id: ID } }, bubbles: true }));
  assert.equal(window.location.hash, `#/tasks/${ID}`);
  await tick();
  assert.ok(view.querySelector("as-task-detail"));
});

test("the new task form is only on the list page", async () => {
  const { view } = await boot(`#/tasks/${ID}`, tasksApi({ tasks: [task()] }));
  assert.equal(view.querySelector("as-new-task"), null);
});

// ── chat ─────────────────────────────────────────────────────────────────────

const CONV = "cccccccc-1111-4222-8333-444444444444";

test("parseHash: the chat page, with or without a conversation, and a bad id", () => {
  assert.deepEqual(App.parseHash("#/chat"), { view: "chat", id: "" });
  assert.deepEqual(App.parseHash("#/chat/"), { view: "chat", id: "" });
  assert.deepEqual(App.parseHash(`#/chat/${CONV}`), { view: "chat", id: CONV });
  assert.deepEqual(App.parseHash("#/chat/not-an-id"), { view: "notfound" });
  assert.deepEqual(App.parseHash(`#/chat/${CONV}/extra`), { view: "notfound" });
});

test("hashFor: the chat page round-trips", () => {
  assert.equal(App.hashFor({ view: "chat", id: "" }), "#/chat");
  assert.equal(App.hashFor({ view: "chat", id: CONV }), `#/chat/${CONV}`);
  for (const route of [{ view: "chat", id: "" }, { view: "chat", id: CONV }]) assert.deepEqual(App.parseHash(App.hashFor(route)), route);
});

test("the chat page shows the conversations and the open one side by side, and marks Chat in the nav", async () => {
  const { view } = await boot(`#/chat/${CONV}`, tasksApi({ conversations: [conversation({ id: CONV, title: "Try" })], chatMessages: [chatMessage({ id: 1, text: "hello" })] }));
  assert.deepEqual([...view.querySelectorAll("as-card")].map((c) => c.getAttribute("heading")), ["Conversations", "Chat"]);
  assert.equal(view.querySelector("as-chat-thread").getAttribute("conversation-id"), CONV);
  assert.equal(view.querySelector("as-chat-list").getAttribute("active"), CONV);
  assert.equal(view.querySelectorAll("as-chat-list li.as-chat-item").length, 1);
  assert.equal(view.querySelectorAll("as-chat-thread .as-msg").length, 1);
  assert.equal(document.getElementById("mainNav").getAttribute("active"), "chat");
});

test("with no conversation open the thread asks to pick one", async () => {
  const { view } = await boot("#/chat", tasksApi({ conversations: [] }));
  assert.equal(view.querySelector("as-chat-thread").hasAttribute("conversation-id"), false);
  assert.match(view.querySelector("as-chat-thread").textContent, /Pick a conversation/);
});

test("starting a conversation opens it, and deleting one goes back to the empty chat", async () => {
  const { view } = await boot("#/chat", tasksApi({ conversations: [] }));
  view.querySelector("as-chat-list").dispatchEvent(new window.CustomEvent("as-conversation-created", { detail: { conversation: { id: CONV } }, bubbles: true }));
  assert.equal(window.location.hash, `#/chat/${CONV}`);
  await tick();
  assert.equal(view.querySelector("as-chat-thread").getAttribute("conversation-id"), CONV);
  view.querySelector("as-chat-thread").dispatchEvent(new window.CustomEvent("as-conversation-removed", { detail: { id: CONV }, bubbles: true }));
  assert.equal(window.location.hash, "#/chat");
  await tick();
  assert.equal(view.querySelector("as-chat-thread").hasAttribute("conversation-id"), false);
});

test("moving between conversations keeps the same page elements", async () => {
  const C2 = "dddddddd-1111-4222-8333-444444444444";
  const { view } = await boot(`#/chat/${CONV}`, tasksApi({ conversations: [conversation({ id: CONV }), conversation({ id: C2 })] }));
  const thread = view.querySelector("as-chat-thread");
  window.location.hash = `#/chat/${C2}`;
  await tick();
  assert.equal(view.querySelector("as-chat-thread"), thread);
  assert.equal(thread.getAttribute("conversation-id"), C2);
});

test("a chat failure is reported in the status bar", async () => {
  await boot(`#/chat/${CONV}`, tasksApi({ conversationsStatus: 500, chatStatus: 500 }));
  assert.match(document.querySelector(".as-status-message").textContent, /^Could not load/);
});

test("the task page's retry opens the new task and its remove goes back to the list", async () => {
  const { view } = await boot(`#/tasks/${ID}`, tasksApi({ tasks: [task({ status: "failed", error: "x", result: null })] }));
  view.querySelector("as-task-detail").dispatchEvent(new window.CustomEvent("as-task-retried", { detail: { task: { id: "99999999-2222-4333-8444-555555555555" } }, bubbles: true }));
  assert.equal(window.location.hash, "#/tasks/99999999-2222-4333-8444-555555555555");
  await tick();
  view.querySelector("as-task-detail").dispatchEvent(new window.CustomEvent("as-task-removed", { detail: { id: ID }, bubbles: true }));
  assert.equal(window.location.hash, "#/tasks");
});
