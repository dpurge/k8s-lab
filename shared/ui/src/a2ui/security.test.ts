// What an untrusted interface must not be able to do: markup, script, dangerous
// links, oversized messages, and opening addresses without the user.
import { test } from "node:test";
import assert from "node:assert/strict";
import { CATALOG, components, create, draw, settle } from "./testkit.ts";
import { createClient, type ClientMessage } from "./index.ts";
import { mount } from "../testenv.ts";
import type { DpA2uiSurface } from "./index.ts";

const textOf = async (text: string) => {
  const { el } = await draw([create("s"), components("s", [{ id: "root", component: "Text", text }])], "s");
  await settle(el);
  return el;
};

// ── markdown ─────────────────────────────────────────────────────────────────

test("markdown: headings, emphasis, code, lists, links and paragraph breaks", async () => {
  const el = await textOf("# Title\n\nSome **bold**, *italic*, __also bold__, _also italic_ and `code`.\nSecond line.\n\n- one\n- two\n\n1. first\n2. second\n\n[docs](https://example.com/a?b=1)\n\n```\nlet x = 1 < 2;\n```");
  assert.equal(el.querySelector("h1")!.textContent, "Title");
  assert.equal(el.querySelectorAll("strong").length, 2);
  assert.equal(el.querySelectorAll("em").length, 2);
  assert.equal(el.querySelector("p code")!.textContent, "code");
  assert.ok(el.querySelector("p br"), "a single newline is a line break");
  assert.deepEqual([...el.querySelectorAll("ul li")].map((n) => n.textContent), ["one", "two"]);
  assert.deepEqual([...el.querySelectorAll("ol li")].map((n) => n.textContent), ["first", "second"]);
  const a = el.querySelector("a")!;
  assert.equal(a.getAttribute("href"), "https://example.com/a?b=1");
  assert.equal(a.getAttribute("target"), "_blank");
  assert.equal(a.getAttribute("rel"), "noopener noreferrer");
  assert.equal(el.querySelector("pre code")!.textContent, "let x = 1 < 2;");
});

test("markdown: all six heading levels, and a # without a space is just text", async () => {
  const el = await textOf("# a\n## b\n### c\n#### d\n##### e\n###### f\n#nospace");
  assert.deepEqual([...el.querySelectorAll("h1,h2,h3,h4,h5,h6")].map((h) => h.tagName), ["H1", "H2", "H3", "H4", "H5", "H6"]);
  assert.match(el.textContent!, /#nospace/);
});

test("markdown: snake_case and a lone * are not emphasis", async () => {
  const el = await textOf("use snake_case_names and 2 * 3 * 4");
  assert.equal(el.querySelector("em"), null);
  assert.match(el.textContent!, /snake_case_names and 2 \* 3 \* 4/);
});

test("markdown: raw HTML is plain characters, in text, headings, links, lists and code", async () => {
  const el = await textOf(
    '<script>alert(1)</script> <img src=x onerror="alert(1)"> <b>x</b>\n\n# <iframe src=//evil></iframe>\n\n- <svg onload=alert(1)>\n\n[<img src=x onerror=alert(1)>](https://example.com)\n\n`<u>`',
  );
  for (const tag of ["script", "img", "b", "iframe", "svg", "u"]) assert.equal(el.querySelector(tag), null, tag);
  assert.match(el.textContent!, /<script>alert\(1\)<\/script>/);
  assert.equal(el.querySelector("a")!.getAttribute("href"), "https://example.com/");
});

test("markdown: only http and https links are links", async () => {
  const el = await textOf(
    "[a](javascript:alert(1)) [b](JaVaScRiPt:alert(1)) [c](data:text/html,<script>alert(1)</script>) [d](//evil.example/x) [e](/relative) [f](mailto:x@y.z) [g](vbscript:x) [h](file:///etc/passwd) [ok](http://example.com)",
  );
  const links = [...el.querySelectorAll("a")];
  assert.deepEqual(links.map((a) => a.getAttribute("href")), ["http://example.com/"]);
  for (const label of ["a", "b", "c", "d", "e", "f", "g", "h"]) assert.match(el.textContent!, new RegExp(`\\b${label}\\b`));
});

test("markdown: an image is its alt text, never an img", async () => {
  const el = await textOf("![a cat](https://tracker.example/cat.png)");
  assert.equal(el.querySelector("img"), null);
  assert.match(el.textContent!, /a cat/);
});

test("markdown: pathological input is handled quickly", async () => {
  const started = Date.now();
  for (const text of [
    "# " + "a ".repeat(40_000) + " ",
    "*".repeat(50_000),
    "_".repeat(50_000),
    "[".repeat(30_000),
    "[a](".repeat(10_000),
    "**a ".repeat(10_000),
    "`".repeat(50_000),
    "- ".repeat(20_000),
    "a\n".repeat(30_000),
  ]) await textOf(text);
  assert.ok(Date.now() - started < 8000, `took ${Date.now() - started} ms`);
});

// ── openUrl ──────────────────────────────────────────────────────────────────

const withOpen = async (action: Record<string, unknown>, run: (opened: unknown[][], sent: ClientMessage[]) => void | Promise<void>) => {
  const opened: unknown[][] = [];
  const original = window.open;
  window.open = ((...args: unknown[]) => void opened.push(args)) as never;
  try {
    const sent: ClientMessage[] = [];
    const client = createClient({ send: (m) => void sent.push(m) });
    await client.receive([
      create("s"),
      components("s", [
        { id: "root", component: "Button", child: "t", action },
        { id: "t", component: "Text", text: "Open" },
      ]),
    ]);
    const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
    el.surface = client.processor.getSurface("s") as never;
    await settle(el);
    el.querySelector<HTMLElement>("dp-button")!.click();
    await settle(el);
    await run(opened, sent);
  } finally {
    window.open = original;
  }
};

test("openUrl from a button opens an http(s) address in a new tab with no opener and no referrer", async () => {
  await withOpen({ functionCall: { call: "openUrl", args: { url: "https://example.com/page" } } }, (opened) => {
    assert.deepEqual(opened, [["https://example.com/page", "_blank", "noopener,noreferrer"]]);
  });
});

test("openUrl refuses every scheme but http and https, and reports it", async () => {
  for (const url of ["javascript:alert(1)", "mailto:a@b.c", "tel:123", "file:///etc/passwd", "data:text/html,x", "blob:x", "ftp://x/y"]) {
    await withOpen({ functionCall: { call: "openUrl", args: { url } } }, (opened, sent) => {
      assert.deepEqual(opened, [], url);
      assert.ok(sent.some((m) => m.error), `${url} was reported`);
    });
  }
});

test("openUrl without a user gesture does nothing: a binding cannot open a tab", async () => {
  const opened: unknown[][] = [];
  const original = window.open;
  window.open = ((...args: unknown[]) => void opened.push(args)) as never;
  try {
    const { el } = await draw(
      [create("s"), components("s", [{ id: "root", component: "Text", text: { "@call": "openUrl", args: { url: "https://example.com" } } }])],
      "s",
    );
    await settle(el);
    assert.deepEqual(opened, []);
  } finally {
    window.open = original;
  }
});

test("the agent cannot call openUrl through callRendererFunction", async () => {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m) });
  const opened: unknown[][] = [];
  const original = window.open;
  window.open = ((...args: unknown[]) => void opened.push(args)) as never;
  try {
    await client.receive([
      create("s"),
      { version: "v1.0", callRendererFunction: { functionCallId: "o1", callFunction: { catalogId: CATALOG, call: "openUrl", args: { url: "https://example.com" } } } },
    ]);
  } finally {
    window.open = original;
  }
  assert.deepEqual(opened, []);
  const response = sent.find((m) => m.rendererFunctionResponse)!.rendererFunctionResponse as { error?: { code: string } };
  assert.equal(response.error?.code, "INVALID_FUNCTION_CALL");
});

// ── limits ───────────────────────────────────────────────────────────────────

const errorsOf = (sent: ClientMessage[]) => sent.filter((m) => m.error).map((m) => m.error as { code: string; message: string; surfaceId?: string });

test("a message over the size limit is rejected whole, with the limit named, and the surface is unchanged", async () => {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m), limits: { maxMessageBytes: 2000 } });
  await client.receive([
    create("s"),
    components("s", [{ id: "root", component: "Text", text: "small" }]),
    components("s", [{ id: "root", component: "Text", text: "x".repeat(5000) }]),
  ]);
  const [error] = errorsOf(sent);
  assert.equal(error.code, "LIMIT_EXCEEDED");
  assert.equal(error.surfaceId, "s");
  assert.match(error.message, /limit is 2000/);
  assert.equal((client.processor.getSurface("s")!.componentsModel.get("root")!.properties as { text: string }).text, "small");
});

test("too many components in one message, or in one surface, are rejected", async () => {
  const many = (n: number, prefix = "c") => Array.from({ length: n }, (_, i) => ({ id: `${prefix}${i}`, component: "Text", text: "x" }));
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m), limits: { maxComponentsPerMessage: 10, maxComponentsPerSurface: 15 } });
  await client.receive([create("s"), components("s", many(11))]);
  assert.match(errorsOf(sent)[0].message, /11 components; the limit is 10/);
  await client.receive([components("s", many(10))]);
  assert.equal(errorsOf(sent).length, 1, "ten fit");
  await client.receive([components("s", many(10, "d"))]);
  assert.match(errorsOf(sent)[1].message, /20 components; the limit is 15/);
  await client.receive([components("s", many(10))]);
  assert.equal(errorsOf(sent).length, 2, "replacing existing components does not count twice");
});

test("a tree nested deeper than the limit is rejected", async () => {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m) });
  const chain = Array.from({ length: 60 }, (_, i) => ({ id: i === 0 ? "root" : `n${i}`, component: "Column", children: i === 59 ? [] : [`n${i + 1}`] }));
  await client.receive([create("s"), components("s", chain)]);
  assert.deepEqual(errorsOf(sent).map((e) => e.code), ["RECURSION_ERROR"]);
  assert.match(errorsOf(sent)[0].message, /depth > 20/);
  assert.equal(client.processor.getSurface("s")!.componentsModel.size, 0, "nothing of the rejected tree was kept");
  const shallow = Array.from({ length: 10 }, (_, i) => ({ id: i === 0 ? "root" : `n${i}`, component: "Column", children: i === 9 ? [] : [`n${i + 1}`] }));
  const ok: ClientMessage[] = [];
  const client2 = createClient({ send: (m) => void ok.push(m) });
  await client2.receive([create("t"), components("t", shallow)]);
  assert.deepEqual(errorsOf(ok), []);
});

test("a reference cycle is reported and nothing of it is kept", async () => {
  const sent: ClientMessage[] = [];
  const client = createClient({ send: (m) => void sent.push(m) });
  await client.receive([
    create("s"),
    components("s", [
      { id: "root", component: "Column", children: ["a"] },
      { id: "a", component: "Column", children: ["root"] },
    ]),
  ]);
  assert.deepEqual(errorsOf(sent).map((e) => e.code), ["RECURSION_ERROR"]);
  assert.match(errorsOf(sent)[0].message, /Circular reference/);
  assert.equal(client.processor.getSurface("s")!.componentsModel.size, 0);
});
