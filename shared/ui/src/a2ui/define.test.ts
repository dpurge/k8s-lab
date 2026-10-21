import { test } from "node:test";
import assert from "node:assert/strict";
import { components, create, settle, text } from "./testkit.ts";
import { mount } from "../testenv.ts";
import { createClient, defineCatalog, type ClientMessage, type DpA2uiSurface } from "./index.ts";

const ID = "https://example.test/a2ui/catalog.json";

function rig(extra: Parameters<typeof defineCatalog>[0]["functions"]) {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m), catalogs: [defineCatalog({ id: ID, functions: extra })] });
  return { client, sent };
}

const responses = (sent: ClientMessage[]) => sent.filter((m) => m.rendererFunctionResponse).map((m) => m.rendererFunctionResponse as { functionCallId: string; value?: unknown; error?: { code: string; message: string } });

const call = (id: string, fn: string, args: Record<string, unknown> = {}) => ({
  version: "v1.0",
  callRendererFunction: { functionCallId: id, callFunction: { catalogId: ID, "@call": fn, args } },
});

test("an agent may call a function whose catalog says rendererOrAgent, and gets its value", async () => {
  const { client, sent } = rig([{ name: "viewport", returnType: "object", allowedCallers: "rendererOrAgent", execute: () => ({ width: 1280, height: 900 }) }]);
  await client.receive([call("c1", "viewport")]);
  assert.deepEqual(responses(sent), [{ functionCallId: "c1", value: { width: 1280, height: 900 } }]);
});

test("an async function's value is awaited", async () => {
  const { client, sent } = rig([{ name: "later", returnType: "string", allowedCallers: "rendererOrAgent", execute: async () => "done" }]);
  await client.receive([call("c1", "later")]);
  assert.equal(responses(sent)[0].value, "done");
});

test("a function with the default callers is the renderer's own: an agent cannot call it", async () => {
  const { client, sent } = rig([{ name: "mine", returnType: "string", execute: () => "x" }]);
  await client.receive([call("c1", "mine")]);
  assert.equal(responses(sent)[0].error?.code, "INVALID_FUNCTION_CALL");
  assert.match(responses(sent)[0].error!.message, /rendererOnly/);
});

test("arguments are checked against the declared ones before the function runs", async () => {
  let ran = 0;
  const { client, sent } = rig([{ name: "greet", returnType: "string", allowedCallers: "rendererOrAgent", args: { name: "string", times: "number" }, execute: () => (ran++, "hi") }]);
  await client.receive([call("a", "greet", { name: "Ada", times: 2 })]);
  await client.receive([call("b", "greet", { name: "Ada" })]);
  await client.receive([call("c", "greet", { name: 5, times: 2 })]);
  await client.receive([call("d", "greet", { name: "Ada", times: 2, extra: true })]);
  const byId = Object.fromEntries(responses(sent).map((r) => [r.functionCallId, r]));
  assert.equal(byId.a.value, "hi");
  for (const id of ["b", "c", "d"]) assert.equal(byId[id].error?.code, "INVALID_FUNCTION_CALL", id);
  assert.equal(ran, 1, "only the valid call ran");
});

test("a function of the catalog can be used in a binding, and an async one shows its value when it arrives", async () => {
  let resolve!: (v: string) => void;
  const waiting = new Promise<string>((r) => (resolve = r));
  const { client } = rig([{ name: "names", returnType: "string", execute: () => waiting }]);
  await client.receive([
    create("s"),
    components("s", [{ id: "root", component: "Text", text: { "@call": "names", catalogId: ID, args: {} } }]),
  ]);
  const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
  el.surface = client.processor.getSurface("s") as never;
  await settle(el);
  assert.deepEqual(text(el), [""], "nothing yet");
  resolve("hello, ui-demo");
  await settle(el);
  assert.deepEqual(text(el), ["hello, ui-demo"]);
});

test("a catalog function that asks the agent: the answer comes back through receive", async () => {
  const sent: ClientMessage[] = [];
  let surfaceId = "s";
  const client = createClient({
    send: (m) => void sent.push(m),
    catalogs: [
      defineCatalog({
        id: ID,
        functions: [{ name: "listWorkflows", returnType: "string", execute: () => client.callAgentFunction(surfaceId, { "@call": "listWorkflows", catalogId: ID, args: {} } as never) as Promise<string> }],
      }),
    ],
  });
  await client.receive([create(surfaceId), components(surfaceId, [{ id: "root", component: "Text", text: { "@call": "listWorkflows", catalogId: ID, args: {} } }])]);
  const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
  el.surface = client.processor.getSurface(surfaceId) as never;
  await settle(el);
  const request = sent.find((m) => m.callAgentFunction)!.callAgentFunction as { functionCallId: string; surfaceId: string };
  assert.equal(request.surfaceId, surfaceId);
  await client.receive([{ version: "v1.0", agentFunctionResponse: { functionCallId: request.functionCallId, value: "hello: Asks for a name." } }]);
  await settle(el);
  assert.deepEqual(text(el), ["hello: Asks for a name."]);
});
