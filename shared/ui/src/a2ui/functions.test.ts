// The reference's conformance cases for the basic catalog functions, run
// against dpCatalog's implementations (web_core's) the way the reference's own
// TypeScript harness runs them: the function's `execute` in a data context.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { DataContext, DataModel, SurfaceModel, getValue, isSignal } from "@a2ui/web_core/v1_0";
import { dpCatalog } from "./catalog.ts";
import "../testenv.ts";

type Case = { name: string; protocolVersion?: string; function: string; args?: Record<string, unknown>; dataModel?: unknown; expect?: unknown; expectError?: unknown };
const cases = (JSON.parse(readFileSync(new URL("./fixtures/conformance/functions.json", import.meta.url), "utf8")) as Case[]).filter((c) => !c.protocolVersion || c.protocolVersion === "v1.0"); // v0.9-only cases do not apply

const VALIDATION = new Set(["required", "regex", "email", "length", "numeric"]);

const evaluate = (c: Case) => {
  const surface = new SurfaceModel("main", dpCatalog as never, undefined, undefined, false, new DataModel((c.dataModel ?? {}) as never));
  const fn = dpCatalog.functions.get(c.function);
  assert.ok(fn, `${c.function} is in the catalog`);
  const result = fn.execute(c.args ?? {}, new DataContext(surface, "/"));
  return isSignal(result) ? getValue(result) : result;
};

test("the catalog has the 14 functions of the basic catalog, and the built-in ", () => {
  assert.deepEqual(
    [...dpCatalog.functions.keys()].sort(),
    ["@index", "and", "email", "formatCurrency", "formatDate", "formatNumber", "formatString", "length", "not", "numeric", "openUrl", "or", "pluralize", "regex", "required"],
  );
});

for (const c of cases) {
  test(`conformance ${c.name}`, () => {
    if (c.expectError) {
      assert.throws(() => evaluate(c));
      return;
    }
    let result = evaluate(c);
    // The cases that name no protocol version were written for v0.9, where the
    // validation functions return a boolean; in v1.0 they return a
    // ValidationResult, so what the case expects is its `valid` field.
    if (VALIDATION.has(c.function) && typeof c.expect === "boolean") result = (result as { valid: boolean }).valid;
    assert.deepEqual(result === undefined ? null : JSON.parse(JSON.stringify(result)), c.expect);
  });
}
