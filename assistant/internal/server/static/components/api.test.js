import { test } from "node:test";
import assert from "node:assert/strict";
import { load, mockFetch, jsonResponse, textResponse } from "./testenv.js";

await load("api.js");
const api = window.asApi;

test("getJSON returns the parsed body and asks for JSON", async () => {
  let seen;
  mockFetch((url, options) => {
    seen = { url, options };
    return jsonResponse(200, { items: [1, 2] });
  });
  assert.deepEqual(await api.getJSON("/api/v1/tasks"), { items: [1, 2] });
  assert.equal(seen.url, "/api/v1/tasks");
  assert.equal(seen.options.headers.Accept, "application/json");
});

test("getJSON throws the server's own error message", async () => {
  mockFetch(() => jsonResponse(404, { error: { code: "not_found", message: "task not found" } }));
  await assert.rejects(api.getJSON("/x"), { message: "task not found" });
});

test("getJSON falls back to the status when the error body is not the API's", async () => {
  mockFetch(() => textResponse(502, "<html>Bad gateway</html>"));
  await assert.rejects(api.getJSON("/x"), { message: "HTTP 502" });
  mockFetch(() => jsonResponse(500, { oops: true }));
  await assert.rejects(api.getJSON("/x"), { message: "HTTP 500" });
  mockFetch(() => jsonResponse(500, { error: { message: 42 } }));
  await assert.rejects(api.getJSON("/x"), { message: "HTTP 500" });
});

test("getJSON lets a network failure through", async () => {
  mockFetch(() => {
    throw new TypeError("Failed to fetch");
  });
  await assert.rejects(api.getJSON("/x"), { message: "Failed to fetch" });
});

test("loadUsers maps ids to display names", async () => {
  mockFetch(() => jsonResponse(200, { items: [{ id: "a", display_name: "Ada" }, { id: "b", display_name: "Bob" }] }));
  const users = await api.loadUsers();
  assert.equal(users.get("a"), "Ada");
  assert.equal(users.get("b"), "Bob");
  assert.equal(users.size, 2);
});

test("loadUsers copes with an empty or missing list", async () => {
  mockFetch(() => jsonResponse(200, { items: [] }));
  assert.equal((await api.loadUsers()).size, 0);
  mockFetch(() => jsonResponse(200, {}));
  assert.equal((await api.loadUsers()).size, 0);
});

test("shortId keeps the first eight characters", () => {
  assert.equal(api.shortId("11111111-2222-4333-8444-555555555555"), "11111111");
  assert.equal(api.shortId("abc"), "abc");
  assert.equal(api.shortId(undefined), "undefine");
});
