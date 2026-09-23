// Standalone jsdom test for the Ingest tab's Drafts/Jobs sub-workspace
// wiring added to app.js (specs/features/knowledge-workspace-redesign.md,
// Approach step 3). app.js is a single monolithic script with real
// top-level side effects (network calls, setInterval, etc.) and no
// exports, so it can't be imported wholesale in a test without dragging
// in the entire app. Instead, this test extracts the exact source text of
// the wiring block and the showIngestView/showIngestSection/showTab
// function definitions straight out of app.js by string markers, and
// executes that real source against a minimal harness DOM plus the real
// kb-nav custom element — so the assertions below exercise the actual
// app.js logic, not a hand-retyped reimplementation of it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { JSDOM } from "jsdom";

const here = path.dirname(fileURLToPath(import.meta.url));
// This file lives in components/ (so `node --test` run from there, as
// task test-knowledge-frontend does, discovers it alongside the other
// component tests) but app.js it extracts real logic from lives one
// directory up, in static/.
const staticDir = path.join(here, "..");

function extractBetween(source, startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const endMarkerIndex = source.indexOf(endMarker, start);
  if (start === -1 || endMarkerIndex === -1) {
    throw new Error("app.js no longer matches the markers this test extracts from it: " + startMarker);
  }
  return source.slice(start, endMarkerIndex + endMarker.length);
}

const appJsSource = readFileSync(path.join(staticDir, "app.js"), "utf8");

// The mainNav/ingestSubNav wiring block (const TAB_IDS ... through
// ingestSubNav's kb-nav-select listener) — deliberately stops short of the
// #list/#drafts/#chats/#jobs delegated listeners below it, which this step
// didn't touch and don't need a live element for this test.
const wiringSource = extractBetween(
  appJsSource,
  'const TAB_IDS = ["knowledgeTab", "chatTab", "ingestTab"];',
  'ingestSubNav.addEventListener("kb-nav-select", (event) => showIngestSection(event.detail.id));',
);

// showIngestView/showIngestSection/showTab, verbatim and contiguous in app.js.
const functionsSource = extractBetween(
  appJsSource,
  "function showIngestView(view) {",
  'if (tab === "ingest") run(async () => { await loadDrafts(); await loadJobs(); });\n}',
);

// updateIngestPillCount (Approach step 4, live pill counts), verbatim.
// References `ingestSubNav` as a free variable, same as app.js itself, so
// it's built against a real kb-nav instance the same way wiringSource is
// below, rather than reimplementing the label logic by hand.
const pillCountsSource = extractBetween(appJsSource, "const ingestPillCounts = { drafts: null, jobs: null };", "  ];\n}");

const buildPillCounts = new Function("ingestSubNav", `${pillCountsSource}\nreturn { updateIngestPillCount };`);

// Builds the real wiring in the caller's realm/globals, returning the
// bindings the test needs to drive and inspect. `document`/`run`/
// `loadChats`/`loadDrafts`/`loadJobs`/`showKnowledgeView` are the only
// free variables the extracted source references that this step didn't
// itself define (showKnowledgeView belongs to the Knowledge tab, out of
// scope here — stubbed so showTab("knowledge") doesn't throw).
const buildWiring = new Function(
  "document",
  "run",
  "loadChats",
  "loadDrafts",
  "loadJobs",
  "showKnowledgeView",
  `
  const $ = (id) => document.getElementById(id);
  ${functionsSource}
  ${wiringSource}
  return { mainNav, ingestSubNav, showTab, showIngestSection, showIngestView, TAB_IDS };
  `,
);

function harnessHTML() {
  return `
    <header><kb-nav id="mainNav"></kb-nav></header>
    <section id="knowledgeTab" class="tab active"></section>
    <section id="chatTab" class="tab"></section>
    <section id="ingestTab" class="tab">
      <main>
        <div id="ingestListView" class="view active">
          <kb-nav id="ingestSubNav"></kb-nav>
          <div class="subview active" id="ingestDraftsSection"><div id="drafts"></div></div>
          <div class="subview" id="ingestJobsSection"><div id="jobs"></div></div>
        </div>
        <div id="ingestDetailView" class="view"></div>
      </main>
    </section>
  `;
}

function settle() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// Set up jsdom and import the real kb-nav component exactly once at module
// scope (module imports are cached, so customElements.define only ever
// runs against the very first jsdom window it sees) — mirrors index.html's
// real load order, where kb-nav's <script> tag runs once, ahead of any
// <kb-nav> markup.
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

const kbNavHere = path.join(here, "kb-nav");
const kbNavTemplateHTML = readFileSync(path.join(kbNavHere, "component.html"), "utf8");
global.fetch = async (url) => {
  if (url === "/components/kb-nav/component.html") return { text: async () => kbNavTemplateHTML };
  throw new Error("unexpected fetch: " + url);
};

await import("./kb-nav/component.js");

async function setUpHarness() {
  // Fresh markup per test, reusing the one already-defined customElements
  // registry above — each <kb-nav> parsed here upgrades immediately.
  document.body.innerHTML = harnessHTML();
  await settle();

  const stubCalls = { loadDrafts: 0, loadJobs: 0, loadChats: 0 };
  const wiring = buildWiring(
    document,
    (fn) => fn(),
    async () => {
      stubCalls.loadChats++;
    },
    async () => {
      stubCalls.loadDrafts++;
    },
    async () => {
      stubCalls.loadJobs++;
    },
    () => {}, // showKnowledgeView — Knowledge tab is out of scope for this step
  );

  return { wiring, stubCalls };
}

test("clicking the Jobs pill shows the Jobs subview and hides Drafts, and vice versa", async () => {
  const { wiring } = await setUpHarness();

  assert.equal(document.getElementById("ingestDraftsSection").classList.contains("active"), true);
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), false);
  assert.equal(wiring.ingestSubNav.active, "drafts");

  wiring.ingestSubNav.querySelector('[data-id="jobs"]').click();

  assert.equal(document.getElementById("ingestDraftsSection").classList.contains("active"), false);
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), true);
  assert.equal(wiring.ingestSubNav.active, "jobs");

  wiring.ingestSubNav.querySelector('[data-id="drafts"]').click();

  assert.equal(document.getElementById("ingestDraftsSection").classList.contains("active"), true);
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), false);
  assert.equal(wiring.ingestSubNav.active, "drafts");
});

test("a kb-nav-select from ingestSubNav does not reach mainNav or change the active tab", async () => {
  const { wiring } = await setUpHarness();

  wiring.showTab("knowledge");
  const tabStateBefore = wiring.TAB_IDS.map((id) => document.getElementById(id).classList.contains("active"));
  const mainNavActiveBefore = wiring.mainNav.active;

  wiring.ingestSubNav.querySelector('[data-id="jobs"]').click();

  const tabStateAfter = wiring.TAB_IDS.map((id) => document.getElementById(id).classList.contains("active"));
  assert.deepEqual(tabStateAfter, tabStateBefore);
  assert.equal(wiring.mainNav.active, mainNavActiveBefore);
  // The sub-nav click did have its own, separate effect — proving the
  // listener fired at all, just not on the main nav's handler.
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), true);
});

test("showTab('ingest') resets the sub-workspace to drafts even if Jobs was active", async () => {
  const { wiring, stubCalls } = await setUpHarness();

  wiring.ingestSubNav.querySelector('[data-id="jobs"]').click();
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), true);

  wiring.showTab("ingest");

  assert.equal(document.getElementById("ingestDraftsSection").classList.contains("active"), true);
  assert.equal(document.getElementById("ingestJobsSection").classList.contains("active"), false);
  assert.equal(wiring.ingestSubNav.active, "drafts");
  // loadDrafts()/loadJobs() run inside an async IIFE passed to run(); the
  // second await's continuation lands in a microtask, so give it one tick.
  await settle();
  assert.equal(stubCalls.loadDrafts, 1);
  assert.equal(stubCalls.loadJobs, 1);
});

test("updateIngestPillCount sets each pill's label without disturbing the active pill", async () => {
  const { wiring } = await setUpHarness();
  const { updateIngestPillCount } = buildPillCounts(wiring.ingestSubNav);

  // Switch to the Jobs pill first so the assertions below can confirm a
  // count update doesn't reset which pill is active — re-setting `items`
  // is how kb-nav receives label data at all (see kb-nav/component.js), so
  // clobbering `active` in the process would be an easy regression to miss.
  wiring.ingestSubNav.querySelector('[data-id="jobs"]').click();
  assert.equal(wiring.ingestSubNav.active, "jobs");

  updateIngestPillCount("drafts", 3);
  assert.equal(wiring.ingestSubNav.items[0].label, "Drafts (3)");
  assert.equal(wiring.ingestSubNav.items[1].label, "Jobs"); // jobs count not loaded yet
  assert.equal(wiring.ingestSubNav.active, "jobs");

  updateIngestPillCount("jobs", 0);
  assert.equal(wiring.ingestSubNav.items[1].label, "Jobs (0)");
  assert.equal(wiring.ingestSubNav.active, "jobs");
});
