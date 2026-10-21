import { test } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpStatusBar } from "./component.ts";

const status = (el: DpStatusBar) => el.shadowRoot!.querySelector<HTMLElement>(".status")!;
const message = (el: DpStatusBar) => el.shadowRoot!.querySelector<HTMLElement>(".message")!;

test("starts with two empty parts", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  assert.equal(status(el).textContent, "");
  assert.equal(message(el).textContent, "");
  assert.equal(status(el).getAttribute("part"), "status");
  assert.equal(message(el).getAttribute("part"), "message");
  assert.equal(el.hasAttribute("error"), false);
});

test("setJobStatus shows the status text, and an empty value clears it", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setJobStatus("Updated 10:42");
  await el.updateComplete;
  assert.equal(status(el).textContent, "Updated 10:42");
  el.setJobStatus("");
  await el.updateComplete;
  assert.equal(status(el).textContent, "");
  el.setJobStatus("again");
  el.setJobStatus(null);
  await el.updateComplete;
  assert.equal(status(el).textContent, "");
});

test("setMessage shows the message as a polite status, not an error", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setMessage("Saved");
  await el.updateComplete;
  assert.equal(message(el).textContent, "Saved");
  assert.equal(message(el).getAttribute("role"), "status");
  assert.equal(el.hasAttribute("error"), false);
});

test("setMessage with isError marks the bar as an error and the message as an alert", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setMessage("Could not save", true);
  await el.updateComplete;
  assert.equal(message(el).textContent, "Could not save");
  assert.equal(message(el).getAttribute("role"), "alert");
  assert.equal(el.hasAttribute("error"), true);
});

test("a later plain message clears the error state", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setMessage("bad", true);
  el.setMessage("fine");
  await el.updateComplete;
  assert.equal(el.hasAttribute("error"), false);
  assert.equal(message(el).getAttribute("role"), "status");
  el.setMessage("bad again", true);
  el.setMessage("");
  await el.updateComplete;
  assert.equal(message(el).textContent, "");
  assert.equal(el.hasAttribute("error"), false);
});

test("the status and the message are independent", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setJobStatus("job");
  el.setMessage("msg", true);
  el.setMessage("");
  await el.updateComplete;
  assert.equal(status(el).textContent, "job");
});

test("attributes work as well as the methods", async () => {
  const el = await mount<DpStatusBar>('<dp-status-bar status="Idle" message="Oops" error></dp-status-bar>');
  assert.equal(status(el).textContent, "Idle");
  assert.equal(message(el).textContent, "Oops");
  assert.equal(message(el).getAttribute("role"), "alert");
});

test("text is shown as text, never as markup", async () => {
  const el = await mount<DpStatusBar>("<dp-status-bar></dp-status-bar>");
  el.setMessage("<img src=x onerror=alert(1)>");
  el.setJobStatus("<b>bold</b>");
  await el.updateComplete;
  assert.equal(el.shadowRoot!.querySelector("img"), null);
  assert.equal(el.shadowRoot!.querySelector("b"), null);
  assert.equal(message(el).textContent, "<img src=x onerror=alert(1)>");
});

test("methods called before the first render are kept", async () => {
  document.body.innerHTML = "<dp-status-bar></dp-status-bar>";
  const el = document.querySelector("dp-status-bar") as DpStatusBar;
  el.setMessage("early", true);
  await el.updateComplete;
  assert.equal(message(el).textContent, "early");
});

test("keeps a fixed minimum height and can be hidden", async () => {
  const css = (DpStatusBar.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /min-height:\s*var\(--dp-status-bar-min-height,\s*2\.4rem\)/);
  assert.match(css, /:host\(\[hidden\]\)\s*\{\s*display:\s*none/);
});
