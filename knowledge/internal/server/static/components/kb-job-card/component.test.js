import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

await import("../kb-button/component.js");
await import("./component.js");

function makeCard(job) {
  const card = document.createElement("kb-job-card");
  document.body.appendChild(card);
  card.job = job;
  return card;
}

function statusBadge(card) {
  return card.querySelector(".kb-job-card-status");
}

test("pending status: running with no step", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "running", step: "" });
  const badge = statusBadge(card);
  assert.equal(badge.textContent, "pending");
  assert.ok(badge.classList.contains("kb-job-card-status-pending"));
});

test("active status: running with a step", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "running", step: "fetching" });
  const badge = statusBadge(card);
  assert.equal(badge.textContent, "active");
  assert.ok(badge.classList.contains("kb-job-card-status-active"));
  assert.equal(card.querySelector(".muted").textContent, "fetching");
});

test("done status", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "done" });
  const badge = statusBadge(card);
  assert.equal(badge.textContent, "done");
  assert.ok(badge.classList.contains("kb-job-card-status-done"));
});

test("failed status", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "failed" });
  const badge = statusBadge(card);
  assert.equal(badge.textContent, "failed");
  assert.ok(badge.classList.contains("kb-job-card-status-failed"));
});

test("failed job renders Retry and Delete buttons", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "failed" });
  const buttons = card.querySelectorAll(".actions kb-button");
  assert.equal(buttons.length, 2);
  assert.equal(buttons[0].textContent, "Retry");
  assert.equal(buttons[0].hasAttribute("variant"), false);
  assert.equal(buttons[1].textContent, "Delete");
  assert.equal(buttons[1].getAttribute("variant"), "danger");
});

test("done (non-running, non-failed) job renders only a Delete button", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "done" });
  const buttons = card.querySelectorAll(".actions kb-button");
  assert.equal(buttons.length, 1);
  assert.equal(buttons[0].textContent, "Delete");
  assert.equal(buttons[0].getAttribute("variant"), "danger");
});

test("running job renders no action buttons", () => {
  const card = makeCard({ id: "1", kind: "ingest", status: "running", step: "fetching" });
  assert.equal(card.querySelectorAll(".actions").length, 0);
});

test("job.error renders as literal text, never parsed as HTML (XSS regression)", () => {
  const payload = "<img src=x onerror=alert(1)>";
  const card = makeCard({ id: "1", kind: "ingest", status: "failed", error: payload });

  // The payload must never be parsed into a real element.
  assert.equal(card.querySelector("img"), null);

  // It must still be visible, verbatim, as text.
  const errorEl = card.querySelector(".error");
  assert.ok(errorEl);
  assert.equal(errorEl.textContent, payload);
  assert.equal(errorEl.innerHTML.includes("<img"), false);
});

test("clicking Retry emits a bubbling kb-job-retry event with the job id", () => {
  const card = makeCard({ id: "job-42", kind: "ingest", status: "failed" });
  const seen = [];
  document.body.addEventListener("kb-job-retry", (event) => seen.push(event.detail.id));

  const retryButton = card.querySelectorAll(".actions kb-button")[0];
  retryButton.querySelector("button").click();

  assert.deepEqual(seen, ["job-42"]);
});

test("clicking Delete emits a bubbling kb-job-delete event with the job id", () => {
  const card = makeCard({ id: "job-42", kind: "ingest", status: "failed" });
  const seen = [];
  document.body.addEventListener("kb-job-delete", (event) => seen.push(event.detail.id));

  const deleteButton = card.querySelectorAll(".actions kb-button")[1];
  deleteButton.querySelector("button").click();

  assert.deepEqual(seen, ["job-42"]);
});

// kb-job-card exists specifically to avoid kb-card's pattern of binding a
// click listener on itself and firing a generic card-select-style event on
// every click, including on nested action buttons (which would misroute a
// Retry/Delete click). This test fails if that self-click-listener is ever
// added back.
test("clicking an action button never fires a generic card-select-style event", () => {
  const card = makeCard({ id: "job-42", kind: "ingest", status: "failed" });
  let genericSelectFired = false;
  card.addEventListener("kb-card-select", () => {
    genericSelectFired = true;
  });
  card.addEventListener("kb-job-card-select", () => {
    genericSelectFired = true;
  });

  card.querySelectorAll(".actions kb-button")[0].querySelector("button").click();
  card.querySelectorAll(".actions kb-button")[1].querySelector("button").click();

  assert.equal(genericSelectFired, false);
});

// The event-based test above only proves two specific event names never
// fire — a regression using any third, made-up event name would pass it
// undetected. This asserts the actual invariant component.js's own header
// comment states ("it must never bind a listener on itself"), mirroring
// the source-text check kb-message/component.test.js uses for its own
// "never assigns to .innerHTML" invariant.
test("the component source never binds a listener on the card element itself", () => {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const source = readFileSync(path.join(here, "component.js"), "utf8");
  assert.equal(
    /this\.addEventListener\(/.test(source),
    false,
    "kb-job-card must never bind a listener on itself — only its own action buttons may emit events",
  );
});
