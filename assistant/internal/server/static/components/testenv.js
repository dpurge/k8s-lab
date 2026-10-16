// Shared setup for the component tests: a jsdom window installed as the
// globals the components expect, the scripts under test loaded on demand, and
// a scripted fetch. Each test file runs in its own process, so this state is
// never shared between files.
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><head></head><body></body></html>", {
  url: "http://localhost/admin/",
  pretendToBeVisual: true,
});
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;
global.URLSearchParams = dom.window.URLSearchParams;
global.localStorage = dom.window.localStorage;
window.__assistantTest = true; // app.js must not start itself

export const staticDir = new URL("../", import.meta.url);

// load imports a script that attaches itself to window or defines elements.
export async function load(...paths) {
  for (const p of paths) await import(new URL(p, staticDir).href);
}

// runScript runs a classic script again every time it is called, which
// import() would not do (it caches the module), for scripts that act when
// they are loaded, such as theme.js.
export function runScript(path) {
  new Function(readFileSync(new URL(path, staticDir), "utf8"))();
}

export function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

export function textResponse(status, text) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => {
      throw new SyntaxError(`not JSON: ${text}`);
    },
  };
}

// mockFetch installs a fetch that calls handler(url, options) and records the
// URLs it was asked for. handler may return a response, a promise of one, or
// throw / reject to simulate a network failure.
export function mockFetch(handler) {
  const calls = [];
  global.fetch = async (url, options) => {
    calls.push(String(url));
    return handler(String(url), options);
  };
  return calls;
}

// flush lets pending promise callbacks run (timers faked with mock.timers are
// not affected, since only setTimeout is faked).
export const flush = () => new Promise((resolve) => setImmediate(resolve));

export function reset() {
  document.body.textContent = "";
  document.head.textContent = "";
}

export function user(id, name) {
  return { id, telegram_id: null, display_name: name, created_at: "2026-10-16T10:00:00Z" };
}

const USER_ID = "aaaaaaaa-0000-4000-8000-000000000001";

export function task(over = {}) {
  return {
    id: "11111111-2222-4333-8444-555555555555",
    user_id: USER_ID,
    workflow_id: "noop",
    source: "api",
    status: "succeeded",
    payload: { hello: "world" },
    result: { hello: "world" },
    error: null,
    created_at: "2026-10-16T12:00:00Z",
    started_at: "2026-10-16T12:00:02Z",
    finished_at: "2026-10-16T12:00:03Z",
    queue_seconds: 2,
    execution_seconds: 1,
    usage: { calls: 0, input_tokens: 0, output_tokens: 0, cost_usd: 0, models: [] },
    ...over,
  };
}

// call is one attempt to call a model, as GET /api/v1/tasks/{id}/llm-calls
// returns it.
export function call(over = {}) {
  return {
    id: "cccccccc-0000-4000-8000-000000000001",
    task_id: "11111111-2222-4333-8444-555555555555",
    user_id: USER_ID,
    model_id: "big",
    provider_id: "gpu",
    model_name: "big-model:30b",
    tags: ["chat", "strong"],
    input_tokens: 1200,
    output_tokens: 300,
    cost_usd: 0.0135,
    started_at: "2026-10-16T12:00:02Z",
    duration_ms: 2500,
    outcome: "ok",
    error: null,
    ...over,
  };
}

// step is one attempt of a workflow run, as GET /api/v1/tasks/{id}/steps
// returns it.
export function step(over = {}) {
  return {
    id: 1,
    task_id: "11111111-2222-4333-8444-555555555555",
    step: 1,
    attempt: 1,
    state: "intake",
    outcome: "ok",
    error: null,
    action: { type: "tool", tool: "memory_search", args: { query: "learner" } },
    patch: { goal: "Arabic" },
    tool: "memory_search",
    tool_result: "ok: 0 memories",
    reply: '{"state_patch":{"goal":"Arabic"},"action":{}}',
    input_tokens: 900,
    output_tokens: 60,
    duration_ms: 2500,
    at: "2026-10-16T12:00:03Z",
    ...over,
  };
}

export const DEFAULT_WORKFLOWS = [
  { id: "ask", description: "One call to a model.", kind: "builtin", payload_hint: '{"prompt": "Say ready", "tags": ["chat"]}' },
  { id: "hello", description: "Asks for a name, then greets.", kind: "yaml", payload_hint: '{"message": "start"}' },
  { id: "learn-topic", description: "Teaches a topic.", kind: "yaml", payload_hint: '{"message": "I want to learn Arabic"}' },
  { id: "noop", description: "Returns its payload.", kind: "builtin", payload_hint: '{"sleep_ms": 1500}' },
];

// conversation is one item of GET /api/v1/conversations.
export function conversation(over = {}) {
  return {
    id: "cccccccc-1111-4222-8333-444444444444",
    user_id: USER_ID,
    workflow_id: "hello-live",
    title: "",
    task_id: null,
    created_at: "2026-10-16T12:00:00Z",
    task_status: null,
    task_error: null,
    last_text: null,
    last_at: "2026-10-16T12:00:00Z",
    messages: 0,
    ...over,
  };
}

// chatMessage is one item of GET /api/v1/conversations/{id}/messages.
export function chatMessage(over = {}) {
  return {
    id: 1,
    conversation_id: "cccccccc-1111-4222-8333-444444444444",
    role: "assistant",
    kind: "text",
    text: "hello",
    task_id: null,
    created_at: "2026-10-16T12:00:05Z",
    ...over,
  };
}

// usageRow is one row of GET /api/v1/usage.
export function usageRow(over = {}) {
  return { user_id: USER_ID, model_id: "big", calls: 2, input_tokens: 1500, output_tokens: 300, cost_usd: 3, ...over };
}

// modelInfo is one item of GET /api/v1/models.
export function modelInfo(over = {}) {
  return { id: "big", provider: "gpu", tags: ["chat", "strong"], priority: 20, available: true, unavailable_until: null, ...over };
}

export { USER_ID };

// tasksApi answers the two endpoints the task list uses. pages maps a request
// URL's query to a response body; by default it returns the given tasks.
export function tasksApi({
  tasks = [], total, users = [user(USER_ID, "Ada")], usersStatus = 200, tasksStatus = 200, tasksBody,
  calls = [], callsStatus = 200, steps = [], stepsStatus = 200, workflows = DEFAULT_WORKFLOWS, workflowsStatus = 200, conversations = [], conversationsStatus = 200, chatMessages = [], chatStatus = 200, usage = [], usageStatus = 200, models = [], modelsStatus = 200,
} = {}) {
  const failure = (status) => jsonResponse(status, { error: { code: "internal", message: "internal error" } });
  return (url) => {
    if (/^\/api\/v1\/tasks\/[^/?]+\/steps/.test(url)) return stepsStatus === 200 ? jsonResponse(200, { items: steps }) : failure(stepsStatus);
    if (/^\/api\/v1\/tasks\/[^/?]+\/llm-calls/.test(url)) return callsStatus === 200 ? jsonResponse(200, { items: calls }) : failure(callsStatus);
    const messagesUrl = url.match(/^\/api\/v1\/conversations\/([^/?]+)\/messages(\?after=(\d+))?/);
    if (messagesUrl) {
      if (chatStatus !== 200) return failure(chatStatus);
      const after = Number(messagesUrl[3] || 0);
      return jsonResponse(200, { items: chatMessages.filter((m) => m.id > after) });
    }
    const oneConversation = url.match(/^\/api\/v1\/conversations\/([^/?]+)$/);
    if (oneConversation) {
      if (chatStatus !== 200) return jsonResponse(chatStatus, { error: { code: chatStatus === 404 ? "not_found" : "internal", message: chatStatus === 404 ? "conversation not found" : "internal error" } });
      return jsonResponse(200, conversations.find((c) => c.id === oneConversation[1]) || conversations[0]);
    }
    if (url.startsWith("/api/v1/conversations")) return conversationsStatus === 200 ? jsonResponse(200, { items: conversations }) : failure(conversationsStatus);
    if (url.startsWith("/api/v1/workflows")) return workflowsStatus === 200 ? jsonResponse(200, { items: workflows }) : failure(workflowsStatus);
    if (url.startsWith("/api/v1/usage")) return usageStatus === 200 ? jsonResponse(200, { items: usage }) : failure(usageStatus);
    if (url.startsWith("/api/v1/models")) return modelsStatus === 200 ? jsonResponse(200, { items: models }) : failure(modelsStatus);
    if (url.startsWith("/api/v1/users")) {
      return usersStatus === 200 ? jsonResponse(200, { items: users }) : jsonResponse(usersStatus, { error: { code: "internal", message: "internal error" } });
    }
    if (url.startsWith("/api/v1/tasks")) {
      if (tasksStatus !== 200) return jsonResponse(tasksStatus, { error: { code: "internal", message: "internal error" } });
      const query = new URLSearchParams(url.split("?")[1] || "");
      return jsonResponse(200, tasksBody || {
        items: tasks,
        total: total ?? tasks.length,
        limit: Number(query.get("limit")),
        offset: Number(query.get("offset")),
      });
    }
    return jsonResponse(404, { error: { code: "not_found", message: "no such route" } });
  };
}
