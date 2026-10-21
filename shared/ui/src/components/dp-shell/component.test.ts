import { test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { mount } from "../../testenv.ts";
import { DpShell } from "./component.ts";

const root = (el: DpShell) => el.shadowRoot!;
const sidebarToggle = (el: DpShell) => root(el).querySelector<HTMLElement>(".sidebar-toggle");
const themeToggle = (el: DpShell) => root(el).querySelector<HTMLElement>(".theme-toggle");
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

async function shell(attrs = "", children = "<p>page</p>") {
  const el = await mount<DpShell>(`<dp-shell ${attrs}>${children}</dp-shell>`);
  await tick();
  await el.updateComplete;
  return el;
}

test("draws a header, a main area and a footer, each with its slot", async () => {
  const el = await shell("", '<a slot="brand">App</a><div slot="actions">me</div><dp-x slot="footer"></dp-x><p>page</p>');
  for (const part of ["header", "main", "footer"]) assert.ok(root(el).querySelector(`[part~="${part}"]`), part);
  const slots = Array.from(root(el).querySelectorAll("slot")).map((s) => s.getAttribute("name"));
  assert.deepEqual(slots, ["brand", "header", "actions", "sidebar", null, "footer"]);
  assert.equal(root(el).querySelector("main slot")!.getAttribute("name"), null);
});

test("with nothing in the sidebar slot there is no sidebar and no sidebar button", async () => {
  const el = await shell();
  assert.equal(sidebarToggle(el), null);
  assert.equal(root(el).querySelector("aside")!.hasAttribute("hidden"), true);
  assert.equal(root(el).querySelector(".body")!.classList.contains("no-sidebar"), true);
});

test("a sidebar gives a sidebar and a button for it", async () => {
  const el = await shell("", '<div slot="sidebar">nav</div><p>page</p>');
  assert.ok(sidebarToggle(el));
  assert.equal(root(el).querySelector("aside")!.hasAttribute("hidden"), false);
  assert.equal(root(el).querySelector(".body")!.classList.contains("no-sidebar"), false);
});

test("a sidebar added after the shell appears, as when a script builds it", async () => {
  const el = await shell();
  const bar = document.createElement("div");
  bar.slot = "sidebar";
  el.append(bar);
  await tick();
  await el.updateComplete;
  assert.ok(sidebarToggle(el));
});

test("the sidebar button collapses and reopens it, remembers the choice and says so", async () => {
  const el = await shell('storage-key="app"', '<div slot="sidebar">nav</div>');
  const events: boolean[] = [];
  el.addEventListener("dp-shell-sidebar-toggle", (e) => events.push((e as CustomEvent).detail.collapsed));
  assert.equal(sidebarToggle(el)!.getAttribute("aria-expanded"), "true");
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-collapsed"), true);
  assert.equal(localStorage.getItem("app-sidebar"), "collapsed");
  assert.equal(sidebarToggle(el)!.getAttribute("aria-expanded"), "false");
  assert.equal(root(el).querySelector("aside")!.hasAttribute("inert"), true, "a collapsed sidebar is out of the tab order");
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-collapsed"), false);
  assert.equal(localStorage.getItem("app-sidebar"), "expanded");
  assert.equal(root(el).querySelector("aside")!.hasAttribute("inert"), false);
  assert.deepEqual(events, [true, false]);
});

test("a remembered collapsed sidebar is collapsed from the start", async () => {
  localStorage.setItem("app-sidebar", "collapsed");
  const el = await shell('storage-key="app"', '<div slot="sidebar">nav</div>');
  assert.equal(el.hasAttribute("sidebar-collapsed"), true);
  localStorage.setItem("other-sidebar", "expanded");
  const open = await shell('storage-key="other"', '<div slot="sidebar">nav</div>');
  assert.equal(open.hasAttribute("sidebar-collapsed"), false);
});

test("the default storage key is dp", async () => {
  const el = await shell("", '<div slot="sidebar">nav</div>');
  sidebarToggle(el)!.click();
  assert.equal(localStorage.getItem("dp-sidebar"), "collapsed");
});

test("labels name the buttons for assistive technology and as tooltips", async () => {
  const el = await shell('theme-toggle theme-label="Switch theme" sidebar-label="Toggle sidebar"', '<div slot="sidebar">nav</div>');
  assert.equal(sidebarToggle(el)!.getAttribute("aria-label"), "Toggle sidebar");
  assert.equal(sidebarToggle(el)!.getAttribute("title"), "Toggle sidebar");
  assert.equal(themeToggle(el)!.getAttribute("aria-label"), "Switch theme");
  assert.equal(themeToggle(el)!.getAttribute("title"), "Switch theme");
});

test("there is no theme button unless theme-toggle is set", async () => {
  assert.equal(themeToggle(await shell()), null);
  assert.ok(themeToggle(await shell("theme-toggle")));
});

test("the theme button flips the theme on <html>, remembers it, shows the icon and says so", async () => {
  document.documentElement.setAttribute("data-theme", "light");
  const el = await shell('theme-toggle storage-key="app"');
  const icon = () => root(el).querySelector(".theme-icon")!.textContent;
  const themes: string[] = [];
  el.addEventListener("dp-shell-theme-change", (e) => themes.push((e as CustomEvent).detail.theme));
  assert.equal(icon(), "☽");
  themeToggle(el)!.click();
  await el.updateComplete;
  assert.equal(document.documentElement.getAttribute("data-theme"), "dark");
  assert.equal(localStorage.getItem("app-theme"), "dark");
  assert.equal(icon(), "☀");
  themeToggle(el)!.click();
  await el.updateComplete;
  assert.equal(document.documentElement.getAttribute("data-theme"), "light");
  assert.equal(icon(), "☽");
  assert.deepEqual(themes, ["dark", "light"]);
});

test("without a theme on <html> the remembered one is used, else the system preference", async () => {
  localStorage.setItem("app-theme", "dark");
  await shell('theme-toggle storage-key="app"');
  assert.equal(document.documentElement.getAttribute("data-theme"), "dark");

  document.documentElement.removeAttribute("data-theme");
  localStorage.clear();
  (window as unknown as { matchMedia: unknown }).matchMedia = () => ({ matches: true });
  await shell("theme-toggle");
  assert.equal(document.documentElement.getAttribute("data-theme"), "dark");
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("a theme already on <html> is never overridden", async () => {
  localStorage.setItem("app-theme", "dark");
  document.documentElement.setAttribute("data-theme", "light");
  await shell('theme-toggle storage-key="app"');
  assert.equal(document.documentElement.getAttribute("data-theme"), "light");
});

test("a theme changed elsewhere updates the icon", async () => {
  document.documentElement.setAttribute("data-theme", "light");
  const el = await shell("theme-toggle");
  document.documentElement.setAttribute("data-theme", "dark");
  await tick();
  await el.updateComplete;
  assert.equal(root(el).querySelector(".theme-icon")!.textContent, "☀");
});

test("empty actions and footer take no room, and they appear when filled", async () => {
  const el = await shell();
  assert.equal(root(el).querySelector(".actions")!.hasAttribute("hidden"), true);
  assert.equal(root(el).querySelector("footer")!.hasAttribute("hidden"), true);
  const full = await shell("", '<div slot="actions">me</div><div slot="footer">status</div>');
  assert.equal(root(full).querySelector(".actions")!.hasAttribute("hidden"), false);
  assert.equal(root(full).querySelector("footer")!.hasAttribute("hidden"), false);
});

test("centered is reflected, and has its own style for the main area", async () => {
  const el = await shell("centered");
  assert.equal(el.hasAttribute("centered"), true);
  const css = (DpShell.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /:host\(\[centered\]\) main\s*\{[^}]*justify-content:\s*center/);
});

test("collapsing the sidebar leaves one column, and on a narrow screen it is a drawer", async () => {
  const css = (DpShell.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /@media \(max-width: 760px\)[\s\S]*position:\s*fixed[\s\S]*translateX\(-100%\)/);
  assert.match(css, /:host\(\[sidebar-collapsed\]\) \.body:not\(\.no-sidebar\)\s*\{\s*grid-template-columns:\s*0 minmax\(0, 1fr\)/);
});

test("a browser that refuses storage still toggles for the page", async () => {
  const el = await shell("theme-toggle", '<div slot="sidebar">nav</div>');
  const original = Storage.prototype.setItem;
  Storage.prototype.setItem = () => {
    throw new Error("denied");
  };
  try {
    sidebarToggle(el)!.click();
    themeToggle(el)!.click();
    await el.updateComplete;
    assert.equal(el.hasAttribute("sidebar-collapsed"), true);
    assert.ok(document.documentElement.getAttribute("data-theme"));
  } finally {
    Storage.prototype.setItem = original;
  }
});

test("the page's own content stays in the page's DOM", async () => {
  const el = await shell("", '<div slot="sidebar"><select id="lang"></select></div><input id="field">');
  assert.equal(document.getElementById("field")!.getRootNode(), document);
  assert.equal(document.getElementById("lang")!.getRootNode(), document);
  assert.equal(root(el).querySelector("input, select"), null);
});

// ── A narrow screen: the sidebar is a drawer ──────────────────────────────────

type Listener = () => void;
// A matchMedia whose answer the test controls, and that tells the shell when it changes.
function narrowScreen(matches: boolean) {
  const listeners = new Set<Listener>();
  const query = {
    matches,
    addEventListener: (_: string, l: Listener) => listeners.add(l),
    removeEventListener: (_: string, l: Listener) => listeners.delete(l),
  };
  (window as unknown as { matchMedia: unknown }).matchMedia = () => query;
  return {
    set(value: boolean) {
      query.matches = value;
      listeners.forEach((l) => l());
    },
    listeners: () => listeners.size,
  };
}
const aside = (el: DpShell) => root(el).querySelector("aside")!;
const scrim = (el: DpShell) => root(el).querySelector<HTMLElement>(".scrim")!;
const WITH_SIDEBAR = '<div slot="sidebar"><a id="link" href="#x">go</a><select id="sel"></select><span id="plain">text</span></div><p>page</p>';

test("on a narrow screen the drawer starts closed, and the button opens and closes it", async () => {
  narrowScreen(true);
  const el = await shell('storage-key="d"', WITH_SIDEBAR);
  assert.equal(el.hasAttribute("sidebar-open"), false);
  assert.equal(aside(el).hasAttribute("inert"), true, "a closed drawer is out of the tab order");
  assert.equal(scrim(el).hasAttribute("hidden"), true);
  assert.equal(sidebarToggle(el)!.getAttribute("aria-expanded"), "false");
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), true);
  assert.equal(aside(el).hasAttribute("inert"), false);
  assert.equal(scrim(el).hasAttribute("hidden"), false);
  assert.equal(sidebarToggle(el)!.getAttribute("aria-expanded"), "true");
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("opening the drawer does not touch the collapsed choice of wide screens or what is saved", async () => {
  narrowScreen(true);
  localStorage.setItem("d-sidebar", "expanded");
  const el = await shell('storage-key="d"', WITH_SIDEBAR);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-collapsed"), false);
  assert.equal(localStorage.getItem("d-sidebar"), "expanded");
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("a collapsed choice saved on a wide screen does not hide the drawer once it is opened", async () => {
  narrowScreen(true);
  localStorage.setItem("d-sidebar", "collapsed");
  const el = await shell('storage-key="d"', WITH_SIDEBAR);
  assert.equal(el.hasAttribute("sidebar-collapsed"), true);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(aside(el).hasAttribute("inert"), false);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("Escape and a click on the page behind close the drawer", async () => {
  narrowScreen(true);
  const el = await shell("", WITH_SIDEBAR);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  el.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  scrim(el).click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("Escape does nothing when the drawer is closed or the screen is wide", async () => {
  const wide = narrowScreen(false);
  const el = await shell("", WITH_SIDEBAR);
  const events: boolean[] = [];
  el.addEventListener("dp-shell-sidebar-toggle", (e) => events.push((e as CustomEvent).detail.collapsed));
  el.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  wide.set(true);
  await el.updateComplete;
  el.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  assert.deepEqual(events, []);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("choosing something in the drawer closes it; a field or a plain click does not", async () => {
  narrowScreen(true);
  const el = await shell("", WITH_SIDEBAR);
  const open = async () => {
    if (!el.hasAttribute("sidebar-open")) sidebarToggle(el)!.click();
    await el.updateComplete;
  };
  await open();
  document.getElementById("sel")!.dispatchEvent(new window.MouseEvent("click", { bubbles: true, composed: true }));
  document.getElementById("plain")!.dispatchEvent(new window.MouseEvent("click", { bubbles: true, composed: true }));
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), true, "a field and plain text keep it open");
  document.getElementById("link")!.addEventListener("click", (e) => e.preventDefault());
  document.getElementById("link")!.dispatchEvent(new window.MouseEvent("click", { bubbles: true, composed: true, cancelable: true }));
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false, "a link closes it");
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("a tab chosen in the drawer closes it", async () => {
  narrowScreen(true);
  const el = await shell("", '<div slot="sidebar"><dp-tabs id="t"></dp-tabs></div>');
  sidebarToggle(el)!.click();
  await el.updateComplete;
  document.getElementById("t")!.dispatchEvent(new window.MouseEvent("click", { bubbles: true, composed: true }));
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("an open drawer does not survive the screen becoming wide, and the wide state is kept", async () => {
  const screen = narrowScreen(true);
  localStorage.setItem("d-sidebar", "collapsed");
  const el = await shell('storage-key="d"', WITH_SIDEBAR);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  screen.set(false);
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  assert.equal(el.hasAttribute("sidebar-collapsed"), true);
  assert.equal(aside(el).hasAttribute("inert"), true, "back on a wide screen the saved collapsed state applies");
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("the event says whether the sidebar is out of sight, for the drawer too", async () => {
  narrowScreen(true);
  const el = await shell("", WITH_SIDEBAR);
  const seen: boolean[] = [];
  el.addEventListener("dp-shell-sidebar-toggle", (e) => seen.push((e as CustomEvent).detail.collapsed));
  sidebarToggle(el)!.click();
  await el.updateComplete;
  scrim(el).click();
  assert.deepEqual(seen, [false, true]);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("on a wide screen nothing about the drawer applies", async () => {
  narrowScreen(false);
  const el = await shell("", WITH_SIDEBAR);
  sidebarToggle(el)!.click();
  await el.updateComplete;
  assert.equal(el.hasAttribute("sidebar-open"), false);
  assert.equal(el.hasAttribute("sidebar-collapsed"), true);
  assert.equal(scrim(el).hasAttribute("hidden"), true);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("the shell stops listening to the screen size when it leaves the page", async () => {
  const screen = narrowScreen(true);
  const el = await shell("", WITH_SIDEBAR);
  assert.equal(screen.listeners(), 1);
  el.remove();
  assert.equal(screen.listeners(), 0);
  delete (window as unknown as { matchMedia?: unknown }).matchMedia;
});

test("a narrow screen has a tighter top bar so the brand, user and buttons fit", async () => {
  const css = (DpShell.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /@media \(max-width: 760px\)[\s\S]*header\s*\{\s*gap:\s*0\.75rem;\s*padding:\s*0 1rem/);
  assert.match(css, /\.actions\s*\{[^}]*min-width:\s*0/);
  assert.match(css, /::slotted\(\[slot="actions"\]\)\s*\{\s*min-width:\s*0/);
  assert.match(css, /::slotted\(\[slot="header"\]\)\s*\{\s*min-width:\s*0;\s*overflow-x:\s*auto/);
});

test("content wider than the page scrolls inside the main area, not the page", async () => {
  const css = (DpShell.styles as unknown as { cssText: string }).cssText;
  assert.match(css, /:host\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\)/, "the shell's own column too");
  assert.match(css, /\.body\s*\{[^}]*grid-template-columns:\s*var\(--dp-shell-sidebar-width, 15rem\) minmax\(0, 1fr\)/);
  assert.match(css, /\.body\.no-sidebar\s*\{\s*grid-template-columns:\s*minmax\(0, 1fr\)/);
  assert.match(css, /\nmain\s*\{[^}]*min-width:\s*0/);
});
