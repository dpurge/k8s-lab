import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import "./testenv.ts";

let run = 0;
// The script runs when it is loaded, so each case loads it afresh, with
// document.currentScript standing in for its own <script> tag.
async function load(key?: string) {
  const script = document.createElement("script");
  if (key) script.dataset.key = key;
  Object.defineProperty(document, "currentScript", { value: script, configurable: true });
  await import(`./theme-init.ts?run=${++run}`);
}

const theme = () => document.documentElement.getAttribute("data-theme");

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("sets the remembered theme under the key from data-key", async () => {
  localStorage.setItem("myapp-theme", "dark");
  await load("myapp");
  assert.equal(theme(), "dark");
});

test("uses the dp key when there is no data-key", async () => {
  localStorage.setItem("dp-theme", "dark");
  await load();
  assert.equal(theme(), "dark");
});

test("with nothing remembered it follows the system preference", async () => {
  (window as unknown as { matchMedia: unknown }).matchMedia = () => ({ matches: true });
  await load("a");
  assert.equal(theme(), "dark");
  document.documentElement.removeAttribute("data-theme");
  (window as unknown as { matchMedia: unknown }).matchMedia = () => ({ matches: false });
  await load("a");
  assert.equal(theme(), "light");
});

test("a remembered choice beats the system preference", async () => {
  localStorage.setItem("a-theme", "light");
  (window as unknown as { matchMedia: unknown }).matchMedia = () => ({ matches: true });
  await load("a");
  assert.equal(theme(), "light");
});

test("with nothing remembered and no matchMedia it uses the light theme", async () => {
  await load("a");
  assert.equal(theme(), "light");
});

test("a browser that refuses storage follows the system preference, and nothing throws", async () => {
  const original = Storage.prototype.getItem;
  Storage.prototype.getItem = () => {
    throw new Error("denied");
  };
  (window as unknown as { matchMedia: unknown }).matchMedia = () => ({ matches: true });
  try {
    await load("a");
    assert.equal(theme(), "dark");
  } finally {
    Storage.prototype.getItem = original;
  }
});
