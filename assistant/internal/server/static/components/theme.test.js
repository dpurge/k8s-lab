import { test } from "node:test";
import assert from "node:assert/strict";
import { runScript } from "./testenv.js";

let dark = false;
let storageBroken = false;
window.matchMedia = () => ({ matches: dark });
global.matchMedia = window.matchMedia;

const proto = Object.getPrototypeOf(window.localStorage);
const originalGet = proto.getItem;
const originalSet = proto.setItem;
proto.getItem = function (k) {
  if (storageBroken) throw new Error("storage blocked");
  return originalGet.call(this, k);
};
proto.setItem = function (k, v) {
  if (storageBroken) throw new Error("storage blocked");
  return originalSet.call(this, k, v);
};

const theme = () => document.documentElement.getAttribute("data-theme");

async function fresh(options = {}) {
  dark = !!options.systemDark;
  storageBroken = !!options.storageBroken;
  window.localStorage.clear();
  if (options.saved) window.localStorage.setItem("as-theme", options.saved);
  document.body.innerHTML = '<span id="theme-icon"></span>';
  document.documentElement.removeAttribute("data-theme");
  runScript("theme.js");
}

test("follows the system when nothing is saved", async () => {
  await fresh({ systemDark: false });
  assert.equal(theme(), "light");
  await fresh({ systemDark: true });
  assert.equal(theme(), "dark");
});

test("a saved choice wins over the system", async () => {
  await fresh({ systemDark: true, saved: "light" });
  assert.equal(theme(), "light");
  await fresh({ systemDark: false, saved: "dark" });
  assert.equal(theme(), "dark");
});

test("a garbage saved value is ignored", async () => {
  await fresh({ systemDark: true, saved: "purple" });
  assert.equal(theme(), "dark");
});

test("the toggle switches, remembers and updates the icon", async () => {
  await fresh({ systemDark: false });
  assert.equal(document.getElementById("theme-icon").textContent, "☽");
  window.toggleTheme();
  assert.equal(theme(), "dark");
  assert.equal(window.localStorage.getItem("as-theme"), "dark");
  assert.equal(document.getElementById("theme-icon").textContent, "☀");
  window.toggleTheme();
  assert.equal(theme(), "light");
  assert.equal(window.localStorage.getItem("as-theme"), "light");
});

test("blocked storage does not break the page or the toggle", async () => {
  await fresh({ systemDark: true, storageBroken: true });
  assert.equal(theme(), "dark");
  assert.doesNotThrow(() => window.toggleTheme());
  assert.equal(theme(), "light");
});
