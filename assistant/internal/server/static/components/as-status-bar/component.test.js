import { test } from "node:test";
import assert from "node:assert/strict";
import { load, reset } from "../testenv.js";

await load("components/as-status-bar/component.js");

function mount() {
  reset();
  const el = document.createElement("as-status-bar");
  document.body.appendChild(el);
  return el;
}

test("starts with an empty job status and message", () => {
  const el = mount();
  assert.equal(el.querySelector(".as-status-job").textContent, "");
  assert.equal(el.querySelector(".as-status-message").textContent, "");
});

test("setJobStatus and setMessage show their text", () => {
  const el = mount();
  el.setJobStatus("Updated 12:00:00");
  el.setMessage("Could not load tasks: boom", true);
  assert.equal(el.querySelector(".as-status-job").textContent, "Updated 12:00:00");
  const message = el.querySelector(".as-status-message");
  assert.equal(message.textContent, "Could not load tasks: boom");
  assert.equal(message.classList.contains("error"), true);
});

test("a normal message clears the error styling, and empty text clears the message", () => {
  const el = mount();
  el.setMessage("bad", true);
  el.setMessage("fine", false);
  assert.equal(el.querySelector(".as-status-message").classList.contains("error"), false);
  el.setMessage("", false);
  el.setJobStatus(undefined);
  assert.equal(el.querySelector(".as-status-message").textContent, "");
  assert.equal(el.querySelector(".as-status-job").textContent, "");
});

test("the message is announced politely", () => {
  assert.equal(mount().querySelector(".as-status-message").getAttribute("role"), "status");
});

test("text is never markup", () => {
  const el = mount();
  el.setMessage("<b>x</b>", false);
  assert.equal(el.querySelector("b"), null);
});

test("reconnecting does not duplicate the parts", () => {
  const el = mount();
  document.body.removeChild(el);
  document.body.appendChild(el);
  assert.equal(el.querySelectorAll(".as-status-job").length, 1);
});

test("calling the setters before the element is connected does not throw", () => {
  const el = document.createElement("as-status-bar");
  assert.doesNotThrow(() => {
    el.setJobStatus("x");
    el.setMessage("y", true);
  });
});
