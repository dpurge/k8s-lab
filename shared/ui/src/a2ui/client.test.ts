import { test } from "node:test";
import assert from "node:assert/strict";
import { CATALOG, components, create, data, settle } from "./testkit.ts";
import { createClient, type ClientMessage, type DpA2uiSurface } from "./index.ts";

const newClient = (callTimeoutMs?: number) => {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m), callTimeoutMs });
  return { client, sent };
};
const errors = (sent: ClientMessage[]) => sent.filter((m) => m.error).map((m) => m.error as Record<string, unknown>);

test("an action is sent as a renderer-to-agent action message with a timestamp", async () => {
  const { client, sent } = newClient();
  await client.receive([
    create("s"),
    components("s", [
      { id: "root", component: "Button", child: "t", action: { event: { name: "go", context: { n: 1 } } } },
      { id: "t", component: "Text", text: "Go" },
    ]),
  ]);
  const { mount } = await import("../testenv.ts");
  const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
  el.surface = client.processor.getSurface("s") as never;
  await settle(el);
  el.querySelector<HTMLElement>("dp-button")!.click();
  await settle(el);
  assert.equal(sent.length, 1);
  assert.equal(sent[0].version, "v1.0");
  const action = sent[0].action as Record<string, unknown>;
  assert.equal(action.name, "go");
  assert.equal(action.surfaceId, "s");
  assert.equal(action.sourceComponentId, "root");
  assert.deepEqual(action.context, { n: 1 });
  assert.match(String(action.timestamp), /^\d{4}-\d\d-\d\dT/);
});

test("a message for a surface that does not exist is reported, and the next message is still handled", async () => {
  const { client, sent } = newClient();
  await client.receive([
    components("nope", [{ id: "root", component: "Text", text: "x" }]),
    create("s"),
  ]);
  const [error] = errors(sent);
  assert.equal(error.surfaceId, "nope");
  assert.match(String(error.message), /Surface not found/);
  assert.ok(client.processor.getSurface("s"), "the message after the bad one was handled");
});

test("a component the catalog does not have is reported as a validation error, and the surface survives", async () => {
  const { client, sent } = newClient();
  await client.receive([
    create("s"),
    components("s", [{ id: "root", component: "Text", text: "ok" }]),
    components("s", [{ id: "x", component: "Hologram" }]),
  ]);
  const [error] = errors(sent);
  assert.equal(error.code, "VALIDATION_FAILED");
  assert.equal(error.surfaceId, "s");
  assert.equal(typeof error.path, "string");
  assert.ok(client.processor.getSurface("s")!.componentsModel.get("root"));
});

test("a message with extra properties is reported, never thrown", async () => {
  const { client, sent } = newClient();
  await client.receive([create("s"), components("s", [{ id: "root", component: "Text", text: "x", colour: "red" }])]);
  assert.equal(errors(sent).length, 1);
});

test("the agent calling a function the renderer must not run gets an error response with the same call id", async () => {
  const { client, sent } = newClient();
  await client.receive([
    create("s"),
    { version: "v1.0", callRendererFunction: { functionCallId: "c1", callFunction: { catalogId: CATALOG, call: "formatNumber", args: { value: 1 } } } },
    { version: "v1.0", callRendererFunction: { functionCallId: "c2", callFunction: { catalogId: "https://example.com/other.json", call: "x", args: {} } } },
    { version: "v1.0", callRendererFunction: { functionCallId: "c3", callFunction: { catalogId: CATALOG, call: "doesNotExist", args: {} } } },
  ]);
  const responses = sent.filter((m) => m.rendererFunctionResponse).map((m) => m.rendererFunctionResponse as { functionCallId: string; error?: { code: string } });
  assert.deepEqual(responses.map((r) => r.functionCallId), ["c1", "c2", "c3"]);
  for (const response of responses) assert.equal(response.error?.code, "INVALID_FUNCTION_CALL");
});

test("callAgentFunction sends the call, and the agent's answer resolves it", async () => {
  const { client, sent } = newClient();
  await client.receive([create("s")]);
  const result = client.callAgentFunction("s", { call: "listWorkflows", args: {} } as never);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const call = sent.find((m) => m.callAgentFunction)!.callAgentFunction as { surfaceId: string; functionCallId: string; callFunction: { call: string } };
  assert.equal(call.surfaceId, "s");
  assert.equal(call.callFunction.call, "listWorkflows");
  await client.receive([{ version: "v1.0", agentFunctionResponse: { functionCallId: call.functionCallId, value: ["hello", "ui-demo"] } }]);
  assert.deepEqual(await result, ["hello", "ui-demo"]);
  assert.deepEqual(errors(sent), []);
});

test("callAgentFunction rejects when the agent answers with an error", async () => {
  const { client, sent } = newClient();
  await client.receive([create("s")]);
  const result = client.callAgentFunction("s", { call: "nope", args: {} } as never);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const id = (sent.find((m) => m.callAgentFunction)!.callAgentFunction as { functionCallId: string }).functionCallId;
  await client.receive([{ version: "v1.0", agentFunctionResponse: { functionCallId: id, error: { code: "FUNCTION_NOT_FOUND", message: "no such function" } } }]);
  await assert.rejects(result, /no such function/);
});

test("callAgentFunction rejects after the time limit", async () => {
  const { client } = newClient(30);
  await client.receive([create("s")]);
  await assert.rejects(client.callAgentFunction("s", { call: "slow", args: {} } as never), /time|timed out|timeout/i);
});

test("an answer to a call nobody made is ignored and reported", async () => {
  const { client, sent } = newClient();
  await client.receive([create("s"), { version: "v1.0", agentFunctionResponse: { functionCallId: "ghost", value: 1 } }]);
  const [error] = errors(sent);
  assert.equal(error.code, "UNKNOWN_FUNCTION_CALL");
  assert.equal(error.functionCallId, "ghost");
});

test("a data model update after the agent's messages still reaches the surface", async () => {
  const { client } = newClient();
  await client.receive([create("s"), data("s", "/", { a: 1 })]);
  assert.equal((client.processor.getSurface("s") as unknown as { dataModel: { get(p: string): unknown } }).dataModel.get("/a"), 1);
});

test("an error a surface reports (a failing call in a binding) goes to the agent; a missing path is only a warning", async () => {
  const { client, sent } = newClient();
  const warnings: string[] = [];
  const original = console.warn;
  console.warn = (m: string) => void warnings.push(m);
  try {
    await client.receive([
      create("s"),
      components("s", [
        { id: "root", component: "Column", children: ["missing", "broken"] },
        { id: "missing", component: "Text", text: { "@path": "/not/there" } },
        { id: "broken", component: "Text", text: { "@call": "regex", args: { value: "a", pattern: "[invalid(" } } },
      ]),
    ]);
    const { mount } = await import("../testenv.ts");
    const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
    el.surface = client.processor.getSurface("s") as never;
    await settle(el);
  } finally {
    console.warn = original;
  }
  assert.ok(warnings.some((w) => w.includes("/not/there")), "the missing path was warned about");
  assert.ok(!errors(sent).some((e) => String(e.message).includes("/not/there")), "and not sent to the agent");
  assert.ok(errors(sent).some((e) => e.surfaceId === "s"), "the failing call was reported to the agent");
});
