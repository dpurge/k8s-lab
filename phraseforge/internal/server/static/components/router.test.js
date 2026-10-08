import { test } from "node:test";
import assert from "node:assert/strict";

await import("../js/router.js");
const { parse, build } = globalThis.pfRouter;

test("parses section, id, page and tag", () => {
  assert.deepEqual(parse("#/texts/12?page=3&tag=x"), { section: "texts", id: 12, page: 3, tag: "x" });
  assert.deepEqual(parse("#/texts?page=2"), { section: "texts", id: null, page: 2, tag: "" });
  assert.deepEqual(parse("#/models"), { section: "models", id: null, page: 1, tag: "" });
});

test("empty or odd hashes parse to an empty section with defaults", () => {
  for (const hash of ["", "#", "#/", undefined]) {
    assert.deepEqual(parse(hash), { section: "", id: null, page: 1, tag: "" });
  }
});

test("ignores a non-numeric id and invalid page values", () => {
  assert.equal(parse("#/texts/abc").id, null);
  assert.equal(parse("#/texts?page=0").page, 1);
  assert.equal(parse("#/texts?page=-4").page, 1);
  assert.equal(parse("#/texts?page=x").page, 1);
  assert.equal(parse("#/texts?page=2.5").page, 1);
});

test("build omits default page and empty tag", () => {
  assert.equal(build({ section: "texts", id: null, page: 1, tag: "" }), "#/texts");
  assert.equal(build({ section: "texts", id: 12, page: 3, tag: "x" }), "#/texts/12?page=3&tag=x");
  assert.equal(build({ section: "texts", page: 2 }), "#/texts?page=2");
});

test("build/parse round-trips, including a tag needing URL encoding", () => {
  const route = { section: "vocabulary", id: 7, page: 4, tag: "a b&c/ü" };
  assert.deepEqual(parse(build(route)), route);
});
