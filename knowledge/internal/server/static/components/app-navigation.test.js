// Standalone jsdom test for the navigation wiring in app.js: the menu (Documents,
// Chat, Tasks), the sidebar's Documents list (Documents, Drafts), the counts in
// their labels, and showTab. app.js is a single script with real top-level side
// effects (network calls, setInterval) and no exports, so it cannot be imported
// in a test. Instead this extracts the exact source text of the wiring and of
// the functions from app.js by string markers and runs that real source against
// a minimal DOM plus a stand-in for the shared dp-tabs element (the Lit
// component itself is tested in shared/ui; this jsdom does not load it), so the
// assertions exercise the actual app.js logic, not a retyped copy of it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { JSDOM } from "jsdom";

const here = path.dirname(fileURLToPath(import.meta.url));
// This file lives in components/ (so `node --test` run from there, as
// task test-knowledge-frontend does, finds it with the other component tests);
// app.js is one directory up, in static/.
const appJsSource = readFileSync(path.join(path.dirname(here), "app.js"), "utf8");

function extractBetween(source, startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start);
  if (start === -1 || end === -1) {
    throw new Error("app.js no longer matches the markers this test extracts from it: " + startMarker);
  }
  return source.slice(start, end + endMarker.length);
}

// The nav wiring: TAB_IDS through the documentsNav listener.
const wiringSource = extractBetween(
  appJsSource,
  'const TAB_IDS = ["knowledgeTab", "chatTab", "ingestTab", "tasksTab"];',
  'documentsNav.addEventListener("dp-tabs-select", (event) => showTab(event.detail.id));',
);
// showIngestView and showTab, verbatim and contiguous (showKnowledgeView is stubbed).
const functionsSource = extractBetween(appJsSource, "function showIngestView(view) {", 'if (tab === "tasks") run(loadJobs);\n}');
// The counts: navCounts, updateNavCount and its first call.
const countsSource = extractBetween(appJsSource, "const navCounts = { drafts: null, tasks: null };", 'updateNavCount("drafts", null);');

const build = new Function(
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
  ${countsSource}
  return { mainNav, documentsNav, showTab, updateNavCount, TAB_IDS };
  `,
);

function harnessHTML() {
  return `
    <header><dp-tabs id="mainNav"></dp-tabs></header>
    <dp-tabs id="documentsNav" class="panel active"></dp-tabs>
    <aside id="knowledgeAside" class="panel active"></aside>
    <aside id="chatAside" class="panel"></aside>
    <aside id="ingestAside" class="panel"></aside>
    <aside id="tasksAside" class="panel"></aside>
    <section id="knowledgeTab" class="tab active"></section>
    <section id="chatTab" class="tab"></section>
    <section id="ingestTab" class="tab">
      <div id="ingestListView" class="view active"><div id="drafts"></div></div>
      <div id="ingestDetailView" class="view"></div>
    </section>
    <section id="tasksTab" class="tab"><div id="jobs"></div></section>
  `;
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

// The dp-tabs stand-in has the parts of the shared component's API that app.js
// uses: an `items` property that draws one button per item (with data-id), an
// `active` property, and a dp-tabs-select event on a click, which also makes
// that item active.
const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
global.window = dom.window;
global.document = dom.window.document;
global.customElements = dom.window.customElements;
global.HTMLElement = dom.window.HTMLElement;
global.CustomEvent = dom.window.CustomEvent;

class StandInTabs extends dom.window.HTMLElement {
  constructor() {
    super();
    this._items = [];
  }
  get items() {
    return this._items;
  }
  set items(value) {
    this._items = Array.isArray(value) ? value : [];
    this._render();
  }
  get active() {
    return this.getAttribute("active") || "";
  }
  set active(value) {
    this.setAttribute("active", value || "");
  }
  connectedCallback() {
    this._render();
  }
  _render() {
    this.textContent = "";
    for (const item of this._items) {
      const button = document.createElement("button");
      button.dataset.id = item.id;
      button.textContent = item.label;
      button.addEventListener("click", () => {
        this.active = item.id;
        this.dispatchEvent(new CustomEvent("dp-tabs-select", { detail: { id: item.id }, bubbles: true }));
      });
      this.appendChild(button);
    }
  }
}
customElements.define("dp-tabs", StandInTabs);

async function setUp() {
  document.body.innerHTML = harnessHTML();
  await settle();
  const calls = { loadDrafts: 0, loadJobs: 0, loadChats: 0 };
  const wiring = build(
    document,
    (fn) => fn(),
    async () => void calls.loadChats++,
    async () => void calls.loadDrafts++,
    async () => void calls.loadJobs++,
    () => {}, // showKnowledgeView: the documents list is out of scope here
  );
  return { wiring, calls };
}

const labels = (tabs) => tabs.items.map((i) => i.label);
const ids = (tabs) => tabs.items.map((i) => i.id);
const isActive = (id) => document.getElementById(id).classList.contains("active");

test("the menu is Documents, Chat, Tasks; the Documents list is Documents, Drafts", async () => {
  const { wiring } = await setUp();
  assert.deepEqual(ids(wiring.mainNav), ["documents", "chat", "tasks"]);
  assert.deepEqual(labels(wiring.mainNav), ["Documents", "Chat", "Tasks"]);
  assert.deepEqual(ids(wiring.documentsNav), ["knowledge", "ingest"]);
  assert.deepEqual(labels(wiring.documentsNav), ["Documents", "Drafts"]);
  assert.equal(wiring.mainNav.active, "documents");
  assert.equal(wiring.documentsNav.active, "knowledge");
});

test("showTab shows the tab's workspace and panel, the Documents list only with a documents tab, and marks the menu", async () => {
  const { wiring } = await setUp();
  const state = () => ({
    workspaces: wiring.TAB_IDS.map(isActive),
    panels: ["knowledgeAside", "chatAside", "ingestAside", "tasksAside"].map(isActive),
    list: isActive("documentsNav"),
    nav: wiring.mainNav.active,
    page: wiring.documentsNav.active,
  });
  wiring.showTab("chat");
  assert.deepEqual(state(), { workspaces: [false, true, false, false], panels: [false, true, false, false], list: false, nav: "chat", page: "knowledge" });
  wiring.showTab("ingest");
  assert.deepEqual(state(), { workspaces: [false, false, true, false], panels: [false, false, true, false], list: true, nav: "documents", page: "ingest" });
  wiring.showTab("tasks");
  assert.deepEqual(state(), { workspaces: [false, false, false, true], panels: [false, false, false, true], list: false, nav: "tasks", page: "ingest" });
  wiring.showTab("knowledge");
  assert.deepEqual(state(), { workspaces: [true, false, false, false], panels: [true, false, false, false], list: true, nav: "documents", page: "knowledge" });
});

test("choosing Documents in the menu opens the documents page; Drafts in the list opens the drafts and keeps the menu on Documents", async () => {
  const { wiring, calls } = await setUp();
  wiring.showTab("tasks");
  wiring.mainNav.querySelector('[data-id="documents"]').click();
  assert.equal(isActive("knowledgeTab"), true);
  assert.equal(wiring.mainNav.active, "documents");

  wiring.documentsNav.querySelector('[data-id="ingest"]').click();
  await settle();
  assert.equal(isActive("ingestTab"), true);
  assert.equal(isActive("ingestAside"), true);
  assert.equal(wiring.mainNav.active, "documents");
  assert.equal(wiring.documentsNav.active, "ingest");
  assert.equal(calls.loadDrafts, 1, "the drafts are loaded when their page opens");

  wiring.documentsNav.querySelector('[data-id="knowledge"]').click();
  assert.equal(isActive("knowledgeTab"), true);
  assert.equal(isActive("ingestTab"), false);
});

test("choosing Tasks opens the tasks and loads them; Chat loads the chats", async () => {
  const { wiring, calls } = await setUp();
  wiring.mainNav.querySelector('[data-id="tasks"]').click();
  await settle();
  assert.equal(isActive("tasksTab"), true);
  assert.equal(isActive("tasksAside"), true);
  assert.equal(calls.loadJobs, 1);
  assert.equal(calls.loadDrafts, 0, "the drafts are not part of the tasks");

  wiring.mainNav.querySelector('[data-id="chat"]').click();
  await settle();
  assert.equal(calls.loadChats, 1);
});

test("entering the drafts page again lands on its list view", async () => {
  const { wiring } = await setUp();
  document.getElementById("ingestListView").classList.remove("active");
  document.getElementById("ingestDetailView").classList.add("active");
  wiring.showTab("ingest");
  assert.equal(isActive("ingestListView"), true);
  assert.equal(isActive("ingestDetailView"), false);
});

test("the Drafts count is in the Documents list, the Tasks count in the menu, and neither disturbs the active item", async () => {
  const { wiring } = await setUp();
  wiring.showTab("ingest");
  wiring.updateNavCount("drafts", 3);
  assert.deepEqual(labels(wiring.documentsNav), ["Documents", "Drafts (3)"]);
  assert.deepEqual(labels(wiring.mainNav), ["Documents", "Chat", "Tasks"], "the tasks count is not loaded yet");
  assert.equal(wiring.documentsNav.active, "ingest");
  assert.equal(wiring.mainNav.active, "documents");

  wiring.updateNavCount("tasks", 0);
  assert.deepEqual(labels(wiring.mainNav), ["Documents", "Chat", "Tasks (0)"]);
  assert.deepEqual(labels(wiring.documentsNav), ["Documents", "Drafts (3)"], "one count does not clobber the other");
  assert.equal(wiring.mainNav.active, "documents");
});
