import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

// curl can't test this feature's actual point: posting straight to a
// delete route always "succeeds," since there is no client-side gate over
// raw HTTP. This extracts phraseforgeWireConfirmForms() from the REAL
// layout.html template (read from disk, not a hand-copied approximation)
// and executes it in jsdom against a fixture data-confirm form, to prove
// the confirm/cancel decision itself — not just that the server accepts a
// submission — actually gates form.submit().
const layoutSource = readFileSync(
  new URL("../../templates/layout.html", import.meta.url),
  "utf8",
);

// layout.html has exactly two attribute-less <script> blocks (a pre-paint
// theme/sidebar IIFE near the top, and the shared functions block at the
// bottom); every other <script> tag in the file has a src= attribute and
// no body. The shared functions block — containing
// phraseforgeWireConfirmForms — is the last match.
const scriptBlocks = [...layoutSource.matchAll(/<script>([\s\S]*?)<\/script>/g)];
const sharedScript = scriptBlocks[scriptBlocks.length - 1][1];
assert.ok(
  sharedScript.includes("function phraseforgeWireConfirmForms"),
  "expected the extracted block to be the one defining phraseforgeWireConfirmForms",
);

function buildDom(formHtml) {
  const html = `<!doctype html><html><body>
    <span id="theme-icon"></span>
    <pf-dialog id="confirmDialog"></pf-dialog>
    ${formHtml}
    <script>${sharedScript}</script>
  </body></html>`;
  return new JSDOM(html, { url: "http://localhost/", runScripts: "dangerously" });
}

test("submitting a data-confirm form calls preventDefault and shows the dialog, without submitting yet", async () => {
  const dom = buildDom(
    '<form id="f" method="post" action="/vocabulary/1/delete" data-confirm="Delete this?" data-confirm-danger="true"><button type="submit">Delete</button></form>',
  );
  const { document } = dom.window;
  const form = document.getElementById("f");
  const dialog = document.getElementById("confirmDialog");

  let confirmArgs = null;
  let resolveConfirm;
  dialog.confirm = (message, options) => {
    confirmArgs = { message, options };
    return new Promise((resolve) => {
      resolveConfirm = resolve;
    });
  };
  let submitCalled = false;
  form.submit = () => {
    submitCalled = true;
  };

  const event = new dom.window.Event("submit", { cancelable: true });
  form.dispatchEvent(event);

  assert.equal(event.defaultPrevented, true, "the real form submission must be prevented");
  // Compared field-by-field, not via deepEqual on the whole object: the
  // extracted script executes inside jsdom's own vm realm (runScripts:
  // "dangerously"), so its object literals aren't prototype-identical to
  // ones built in this test's Node realm, even with identical shape.
  assert.equal(confirmArgs.message, "Delete this?");
  assert.equal(confirmArgs.options.danger, true);
  assert.equal(submitCalled, false, "must not submit before the user has answered");

  resolveConfirm(true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(submitCalled, true, "must submit once confirmed");
});

test("cancelling the dialog never submits the form", async () => {
  const dom = buildDom(
    '<form id="f" method="post" action="/vocabulary/1/delete" data-confirm="Delete this?"><button type="submit">Delete</button></form>',
  );
  const { document } = dom.window;
  const form = document.getElementById("f");
  const dialog = document.getElementById("confirmDialog");

  let resolveConfirm;
  dialog.confirm = () =>
    new Promise((resolve) => {
      resolveConfirm = resolve;
    });
  let submitCalled = false;
  form.submit = () => {
    submitCalled = true;
  };

  form.dispatchEvent(new dom.window.Event("submit", { cancelable: true }));
  resolveConfirm(false);
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.equal(submitCalled, false, "must never submit when the user cancels");
});

test("a form without data-confirm is never intercepted (no listener attached)", async () => {
  const dom = buildDom(
    '<form id="f" method="post" action="/vocabulary/1/edit"><button type="submit">Save</button></form>',
  );
  const { document } = dom.window;
  const form = document.getElementById("f");

  const event = new dom.window.Event("submit", { cancelable: true });
  form.dispatchEvent(event);

  assert.equal(event.defaultPrevented, false, "a plain form must submit normally, untouched by the wiring");
});
