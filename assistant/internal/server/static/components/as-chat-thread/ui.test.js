import { readFileSync } from "node:fs";
import { test, mock, afterEach } from "node:test";
import assert from "node:assert/strict";
import { load, reset, mockFetch, jsonResponse, flush, conversation, chatMessage, tasksApi } from "../testenv.js";

await load("format.js", "api.js", "components/as-chat-thread/component.js");

afterEach(() => {
  mock.timers.reset();
  reset();
  delete window.asA2uiLoader;
});

const C1 = "cccccccc-1111-4222-8333-444444444444";

// A stand-in for the renderer chunk (/ui/a2ui.js): it records what the thread
// hands it, and lets a test make the renderer report to the server.
function fakeRenderer() {
  const state = { received: [], hosts: null, surfaces: new Map(), clients: [], send: null, catalogs: [], agentCalls: [] };
  window.asA2uiLoader = async () => ({
    setMediaHosts: (hosts) => (state.hosts = hosts),
    defineCatalog: (definition) => {
      state.catalogs.push(definition);
      return definition;
    },
    createClient: ({ send, catalogs }) => {
      state.send = send;
      state.clients.push(send);
      state.clientCatalogs = catalogs;
      return {
        callAgentFunction: async (surfaceId, call) => {
          state.agentCalls.push({ surfaceId, call });
          return "hello: Asks for a name.";
        },
        receive: async (messages) => {
          for (const m of messages) {
            state.received.push(m);
            if (m.createSurface) {
              const id = m.createSurface.surfaceId;
              const model = {};
              state.surfaces.set(id, {
                id,
                dataModel: { get: () => model.value ?? state.models?.[id] ?? {} },
                componentsModel: { values: state.components?.[id] ?? [] },
                setModel: (v) => (model.value = v),
              });
            }
            if (m.updateDataModel) {
              state.updates = [...(state.updates || []), m.updateDataModel];
              state.surfaces.get(m.updateDataModel.surfaceId)?.setModel(m.updateDataModel.value);
            }
            if (m.deleteSurface) state.surfaces.delete(m.deleteSurface.surfaceId);
          }
        },
        processor: { getSurface: (id) => state.surfaces.get(id) },
      };
    },
  });
  if (!customElements.get("dp-a2ui-surface")) customElements.define("dp-a2ui-surface", class extends HTMLElement {});
  return state;
}

const surfaceMessages = (id) => [
  { version: "v1.0", createSurface: { surfaceId: id, catalogId: "c" } },
  { version: "v1.0", updateComponents: { surfaceId: id, components: [{ id: "root", component: "Text", text: "x" }] } },
];

async function mount(chatMessages, extra = () => null) {
  reset();
  const base = tasksApi({ conversations: [conversation({ id: C1 })], chatMessages });
  const posts = [];
  const calls = mockFetch((url, options) => {
    if (url === "/a2ui-config.json") return jsonResponse(200, { media_hosts: ["img.example.com"] });
    if (url.endsWith("/ui-events")) {
      posts.push(JSON.parse(options.body));
      return extra(url, options) || jsonResponse(201, chatMessage({ id: 99, role: "user", kind: "ui_action", text: "(go)" }));
    }
    return base(url, options);
  });
  const el = document.createElement("as-chat-thread");
  el.setAttribute("conversation-id", C1);
  el.setAttribute("refresh-ms", "0");
  document.body.appendChild(el);
  await flush();
  await flush();
  await flush();
  return { el, calls, posts };
}

test("an interface message draws a surface for each createSurface, in an element of its own", async () => {
  const state = fakeRenderer();
  const { el } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: [...surfaceMessages("s1"), ...surfaceMessages("s2")] })]);
  const row = el.querySelector(".as-kind-ui");
  assert.ok(row, "a row for the interface");
  assert.equal(row.querySelectorAll("dp-a2ui-surface").length, 2);
  assert.deepEqual([...row.querySelectorAll("dp-a2ui-surface")].map((n) => n.surface.id), ["s1", "s2"]);
  assert.equal(state.received.length, 4, "every A2UI message went to the renderer, in order");
  assert.deepEqual(state.received.map((m) => Object.keys(m)[1]), ["createSurface", "updateComponents", "createSurface", "updateComponents"]);
});

test("the renderer is told which media hosts the server allows", async () => {
  const state = fakeRenderer();
  await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  assert.deepEqual(state.hosts, ["img.example.com"]);
});

test("a conversation with no interface never loads the renderer", async () => {
  let loaded = false;
  window.asA2uiLoader = async () => {
    loaded = true;
    return {};
  };
  const { calls } = await mount([chatMessage({ id: 1, text: "hello" })]);
  assert.equal(loaded, false);
  assert.ok(!calls.includes("/a2ui-config.json"));
});

test("later messages that update or delete a surface reach the same renderer, and a deleted surface disappears", async () => {
  const state = fakeRenderer();
  const { el } = await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, kind: "ui", text: "", ui: [{ version: "v1.0", updateComponents: { surfaceId: "s1", components: [{ id: "root", component: "Text", text: "y" }] } }] }),
    chatMessage({ id: 3, kind: "ui", text: "", ui: [{ version: "v1.0", deleteSurface: { surfaceId: "s1" } }] }),
  ]);
  assert.equal(state.clients.length, 1, "one renderer client for the conversation");
  assert.equal(state.received.length, 4);
  assert.equal(el.querySelectorAll("dp-a2ui-surface").length, 0);
});

test("an interface message that creates no surface leaves no empty row", async () => {
  fakeRenderer();
  const { el } = await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, kind: "ui", text: "", ui: [{ version: "v1.0", updateDataModel: { surfaceId: "s1", value: {} } }] }),
  ]);
  const rows = [...el.querySelectorAll(".as-kind-ui")];
  assert.equal(rows.length, 2);
  assert.equal(rows[0].hidden, false);
  assert.equal(rows[1].hidden, true);
});

test("an action goes to the server as the user's turn, and the thread then reloads", async () => {
  const state = fakeRenderer();
  const { el, posts, calls } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  const before = calls.filter((c) => /\/messages/.test(c)).length;
  const action = { version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "2026-10-21T12:00:00Z", context: {} } };
  await state.send(action);
  await flush();
  assert.deepEqual(posts, [action]);
  assert.ok(calls.filter((c) => /\/messages/.test(c)).length > before, "the thread loaded the new messages");
  assert.equal(el.querySelector(".as-thread-send-error").hidden, true);
});

test("a refused action is shown to the user, with the server's reason", async () => {
  const state = fakeRenderer();
  const { el } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })], () =>
    jsonResponse(409, { error: { code: "conflict", message: "the assistant is still working on your last message" } }),
  );
  await state.send({ version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "t", context: {} } });
  await flush();
  const note = el.querySelector(".as-thread-send-error");
  assert.equal(note.hidden, false);
  assert.match(note.textContent, /still working/);
});

test("an error from a live interface is reported to the server, and one from the history is not", async () => {
  const state = fakeRenderer();
  const { el, posts } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("old") })]);
  await state.send({ version: "v1.0", error: { code: "RENDERER_ERROR", surfaceId: "old", message: "at load" } });
  assert.deepEqual(posts, [], "the history was already reported once");
  const newer = chatMessage({ id: 2, kind: "ui", text: "", ui: surfaceMessages("fresh") });
  el._append([newer]);
  await flush();
  await flush();
  await state.send({ version: "v1.0", error: { code: "RENDERER_ERROR", surfaceId: "fresh", message: "now" } });
  assert.equal(posts.length, 1);
  assert.equal(posts[0].error.surfaceId, "fresh");
});

test("an action message shows as the user's words, and makes the interfaces before it read-only", async () => {
  fakeRenderer();
  const { el } = await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, role: "user", kind: "ui_action", text: "Submitted the form" }),
    chatMessage({ id: 3, kind: "ui", text: "", ui: surfaceMessages("s2") }),
  ]);
  const rows = [...el.querySelectorAll(".as-kind-ui")];
  assert.equal(rows[0].hasAttribute("inert"), true);
  assert.ok(rows[0].classList.contains("as-answered"));
  assert.equal(rows[1].hasAttribute("inert"), false, "an interface after the action is live");
  const action = el.querySelector(".as-kind-ui_action");
  assert.equal(action.querySelector(".as-msg-text").textContent, "Submitted the form");
  assert.ok(action.classList.contains("as-msg-user"));
});

test("a renderer that cannot be loaded is said in the thread, and the conversation still works", async () => {
  window.asA2uiLoader = async () => {
    throw new Error("the renderer is gone");
  };
  const { el } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }), chatMessage({ id: 2, text: "still here" })]);
  assert.match(el.querySelector(".as-kind-ui .as-thread-error").textContent, /could not be drawn: the renderer is gone/);
  assert.ok([...el.querySelectorAll(".as-msg-text")].some((n) => n.textContent === "still here"));
});

test("opening another conversation starts a renderer client of its own and does not keep the first one's interfaces", async () => {
  const state = fakeRenderer();
  const { el } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  assert.equal(el.querySelectorAll("dp-a2ui-surface").length, 1);
  assert.equal(state.clients.length, 1);
  const first = el.querySelector("dp-a2ui-surface");
  el.setAttribute("conversation-id", "dddddddd-1111-4222-8333-444444444444");
  await flush();
  await flush();
  await flush();
  assert.equal(state.clients.length, 2, "a new conversation, a new client");
  assert.equal(el.querySelectorAll("dp-a2ui-surface").length, 1, "only the new conversation's own surface");
  assert.notEqual(el.querySelector("dp-a2ui-surface"), first);
});

// ── the assistant's own functions ────────────────────────────────────────────

test("the catalog the page defines has the id of the server's catalog document and its functions", async () => {
  const state = fakeRenderer();
  await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  const document_ = JSON.parse(readFileSync(new URL("../../../../../schemas/a2ui/assistant_catalog.json", import.meta.url), "utf8"));
  const [catalog] = state.catalogs;
  assert.equal(catalog.id, document_.catalogId);
  assert.deepEqual(catalog.functions.map((f) => f.name).sort(), Object.keys(document_.functions).sort());
  for (const fn of catalog.functions) {
    assert.equal(fn.allowedCallers ?? "rendererOnly", document_.functions[fn.name].allowedCallers, `${fn.name}: callers as the document says`);
    assert.equal(fn.returnType, document_.functions[fn.name].returnType, `${fn.name}: return type`);
  }
  assert.deepEqual(state.clientCatalogs, [catalog]);
});

test("viewport answers with the window's size and colour scheme", async () => {
  const state = fakeRenderer();
  await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  const viewport = state.catalogs[0].functions.find((f) => f.name === "viewport");
  const value = viewport.execute({});
  assert.equal(value.width, window.innerWidth);
  assert.equal(value.height, window.innerHeight);
  assert.equal(typeof value.pixelRatio, "number");
  assert.ok(["light", "dark"].includes(value.colorScheme));
});

test("listWorkflows asks the assistant, on a surface of the conversation", async () => {
  const state = fakeRenderer();
  await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  const list = state.catalogs[0].functions.find((f) => f.name === "listWorkflows");
  assert.equal(await list.execute({}), "hello: Asks for a name.");
  assert.deepEqual(state.agentCalls, [{ surfaceId: "s1", call: { "@call": "listWorkflows", catalogId: state.catalogs[0].id, args: {} } }]);
});

test("listWorkflows without any interface to ask from says so", async () => {
  const state = fakeRenderer();
  await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: [{ version: "v1.0", updateDataModel: { surfaceId: "x", value: {} } }] })]);
  const list = state.catalogs[0].functions.find((f) => f.name === "listWorkflows");
  await assert.rejects(Promise.resolve(list.execute({})), /no interface/);
});

test("function calls and their answers are not shown as things the user said, and do not freeze the interface", async () => {
  fakeRenderer();
  const { el } = await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, role: "user", kind: "ui_action", text: "(call listWorkflows)", ui: { version: "v1.0", callAgentFunction: { surfaceId: "s1", functionCallId: "p1", callFunction: {} } } }),
    chatMessage({ id: 3, kind: "ui", text: "", ui: [{ version: "v1.0", agentFunctionResponse: { functionCallId: "p1", value: "x" } }] }),
    chatMessage({ id: 4, role: "user", kind: "ui_action", text: "(answer to call-1)", ui: { version: "v1.0", rendererFunctionResponse: { functionCallId: "call-1", value: 1 } } }),
  ]);
  assert.equal(el.querySelectorAll(".as-kind-ui_action").length, 0);
  const rows = [...el.querySelectorAll(".as-kind-ui")];
  assert.equal(rows[0].hasAttribute("inert"), false);
  assert.equal(rows[1].hidden, true, "a message that only carries an answer shows nothing");
});

// ── entered values travel with an action and come back ───────────────────────

test("an action carries the values entered in the interface, as the data-model extension", async () => {
  const state = fakeRenderer();
  state.models = { s1: { name: "Janusz", topics: ["a"] } };
  const { posts } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  await state.send({ version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "t", context: {} } });
  await flush();
  assert.deepEqual(posts[0].action.metadata, { extensions: { assistant_data_model: { s1: { name: "Janusz", topics: ["a"] } } } });
  assert.equal(posts[0].action.name, "go", "the action itself is unchanged");
});

test("a surface with a password field is never included, and an action keeps the metadata it had", async () => {
  const state = fakeRenderer();
  state.models = { s1: { password: "hunter2" }, s2: { ok: 1 } };
  state.components = { s1: [{ properties: { variant: "obscured" } }] };
  const { posts } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: [...surfaceMessages("s1"), ...surfaceMessages("s2")] })]);
  await state.send({ version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "t", context: {}, metadata: { extensions: { other_extension: { x: 1 } } } } });
  await flush();
  const extensions = posts[0].action.metadata.extensions;
  assert.deepEqual(extensions.assistant_data_model, { s2: { ok: 1 } });
  assert.deepEqual(extensions.other_extension, { x: 1 });
  assert.ok(!JSON.stringify(posts[0]).includes("hunter2"));
});

test("an interface with nothing entered sends the action as it is", async () => {
  const state = fakeRenderer();
  const { posts } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: [{ version: "v1.0", updateDataModel: { surfaceId: "x", value: {} } }] })]);
  const action = { version: "v1.0", action: { name: "go", surfaceId: "x", sourceComponentId: "root", timestamp: "t", context: {} } };
  await state.send(action);
  await flush();
  assert.deepEqual(posts[0], action);
});

test("reopening a conversation puts the submitted values back into the surface they were entered in", async () => {
  const state = fakeRenderer();
  await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, role: "user", kind: "ui_action", text: "Sent", ui: { version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "t", context: {}, metadata: { extensions: { assistant_data_model: { s1: { name: "Janusz" }, gone: { x: 1 } } } } } } }),
  ]);
  await flush();
  assert.deepEqual(state.updates, [{ surfaceId: "s1", path: "/", value: { name: "Janusz" } }], "only the surface that exists, and only once it was drawn");
});

test("an action without saved values changes nothing on reopening", async () => {
  const state = fakeRenderer();
  await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, role: "user", kind: "ui_action", text: "Sent", ui: { version: "v1.0", action: { name: "go", surfaceId: "s1", sourceComponentId: "root", timestamp: "t", context: {} } } }),
  ]);
  await flush();
  assert.equal(state.updates, undefined);
});

// ── function calls in the history ────────────────────────────────────────────

test("function calls and their answers in the history are not replayed", async () => {
  const state = fakeRenderer();
  await mount([
    chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") }),
    chatMessage({ id: 2, kind: "ui", text: "", ui: [{ version: "v1.0", callRendererFunction: { functionCallId: "call-1", callFunction: { catalogId: "c", "@call": "viewport", args: {} } } }] }),
    chatMessage({ id: 3, kind: "ui", text: "", ui: [{ version: "v1.0", agentFunctionResponse: { functionCallId: "p1", value: "x" } }] }),
  ]);
  assert.deepEqual(state.received.map((m) => Object.keys(m)[1]), ["createSurface", "updateComponents"], "the call and the answer were not handed to the renderer");
});

test("a function call that arrives while the conversation is open is run", async () => {
  const state = fakeRenderer();
  const { el } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  el._append([chatMessage({ id: 2, kind: "ui", text: "", ui: [{ version: "v1.0", callRendererFunction: { functionCallId: "call-2", callFunction: { catalogId: "c", "@call": "viewport", args: {} } } }] })]);
  await flush();
  await flush();
  assert.ok(state.received.some((m) => m.callRendererFunction && m.callRendererFunction.functionCallId === "call-2"));
});

test("asking the assistant for something reloads the thread at once, so the answer is not left waiting for the next poll", async () => {
  const state = fakeRenderer();
  const { calls } = await mount([chatMessage({ id: 1, kind: "ui", text: "", ui: surfaceMessages("s1") })]);
  const before = calls.filter((c) => /\/messages/.test(c)).length;
  await state.send({ version: "v1.0", callAgentFunction: { surfaceId: "s1", functionCallId: "p1", callFunction: { "@call": "listWorkflows", catalogId: "c", args: {} } } });
  await flush();
  assert.ok(calls.filter((c) => /\/messages/.test(c)).length > before);
});
