// dp-shell: the frame of a page: a top bar, an optional sidebar that can be
// collapsed, the main area, and an optional footer. The page's own elements go
// in slots and stay in the page's DOM, so its scripts and styles reach them as
// anywhere else.
//
//   <dp-shell storage-key="myapp" theme-toggle theme-label="Switch theme">
//     <a slot="brand" href="/">My app</a>
//     <nav slot="header">...</nav>
//     <div slot="actions">user, log out</div>
//     <div slot="sidebar">navigation</div>
//     ...the page...
//     <dp-status-bar slot="footer"></dp-status-bar>
//   </dp-shell>
//
// Slots
//   brand    the left of the top bar          header   after the brand
//   actions  the right of the top bar         sidebar  the sidebar; with nothing
//   (default) the main area                            in it there is no sidebar
//   footer   below the main area; nothing in it, no footer
//
// Attributes
//   storage-key        names what is remembered in localStorage: `<key>-sidebar`
//                      ("collapsed" or "expanded") and `<key>-theme` ("light" or
//                      "dark"); default "dp"
//   theme-toggle       show a theme button; the theme is the data-theme attribute
//                      on <html>, which tokens.css reads
//   theme-label        the theme button's accessible name and tooltip
//   sidebar-label      the sidebar button's accessible name and tooltip
//   sidebar-collapsed  the sidebar is collapsed (also remembered; reflected)
//   sidebar-open       on a narrow screen, the drawer is open (not remembered)
//   centered           the main area centres its content: a login page
//
// Methods: toggleSidebar(), toggleTheme()
//
// Events (bubble, composed)
//   dp-shell-sidebar-toggle  detail { collapsed }: whether the sidebar is now out
//                            of sight, collapsed on a wide screen or a closed drawer
//   dp-shell-theme-change    detail { theme }
//
// Parts: header, sidebar-toggle, theme-toggle, actions, sidebar, scrim, main, footer
//
// CSS custom properties: --dp-shell-height (100vh), --dp-shell-header-height
//   (3.5rem), --dp-shell-sidebar-width (15rem), --dp-shell-main-padding
//   (2rem 3rem), --dp-shell-main-padding-narrow (1.5rem).
//
// Below 760px wide the sidebar is a drawer that slides over the page: the
// sidebar button opens and closes it, a click on the page behind it, Escape, or
// choosing something in it (a link, a button, a tab) closes it. Opening it does
// not touch the collapsed choice that wide screens remember, and a closed
// drawer is out of the tab order. Until this element is defined its
// content would show unstyled: hide it with
//   dp-shell:not(:defined) { opacity: 0; }
// (not visibility: hidden, which makes a field inside unfocusable, so an
// autofocus attribute on a login form would be dropped), and load
// /ui/theme-init.js (a classic script) in <head> so the theme is set before the
// first paint.
import { LitElement, html, nothing, unsafeCSS } from "lit";
import "../dp-button/component.ts";
import styles from "./component.css";

export class DpShell extends LitElement {
  static properties = {
    storageKey: { attribute: "storage-key" },
    themeToggle: { type: Boolean, attribute: "theme-toggle" },
    themeLabel: { attribute: "theme-label" },
    sidebarLabel: { attribute: "sidebar-label" },
    sidebarCollapsed: { type: Boolean, attribute: "sidebar-collapsed", reflect: true },
    sidebarOpen: { type: Boolean, attribute: "sidebar-open", reflect: true },
    centered: { type: Boolean, reflect: true },
    _narrow: { state: true },
    _hasSidebar: { state: true },
    _hasFooter: { state: true },
    _hasActions: { state: true },
    _theme: { state: true },
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare storageKey: string;
  declare themeToggle: boolean;
  declare themeLabel: string;
  declare sidebarLabel: string;
  declare sidebarCollapsed: boolean;
  declare sidebarOpen: boolean;
  declare _narrow: boolean;
  declare centered: boolean;
  declare _hasSidebar: boolean;
  declare _hasFooter: boolean;
  declare _hasActions: boolean;
  declare _theme: string;

  #narrowQuery: MediaQueryList | null = null;
  #childObserver = new MutationObserver(() => this.#syncSlots());
  #themeObserver = new MutationObserver(() => this.#readTheme());

  constructor() {
    super();
    this.storageKey = "dp";
    this.themeToggle = false;
    this.themeLabel = "";
    this.sidebarLabel = "";
    this.sidebarCollapsed = false;
    this.sidebarOpen = false;
    this._narrow = false;
    this.centered = false;
    this._hasSidebar = false;
    this._hasFooter = false;
    this._hasActions = false;
    this._theme = "light";
  }

  connectedCallback() {
    super.connectedCallback();
    if (!this.hasAttribute("sidebar-collapsed") && this.#read("sidebar") === "collapsed") this.sidebarCollapsed = true;
    this.#syncSlots();
    this.#childObserver.observe(this, { childList: true });
    this.#readTheme();
    this.#themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    this.#narrowQuery = window.matchMedia?.("(max-width: 760px)") ?? null;
    this._narrow = this.#narrowQuery?.matches ?? false;
    this.#narrowQuery?.addEventListener("change", this.#onNarrowChange);
    this.addEventListener("keydown", this.#onKeydown);
  }

  disconnectedCallback() {
    this.#childObserver.disconnect();
    this.#themeObserver.disconnect();
    this.#narrowQuery?.removeEventListener("change", this.#onNarrowChange);
    this.removeEventListener("keydown", this.#onKeydown);
    super.disconnectedCallback();
  }

  willUpdate(changed: Map<string, unknown>) {
    if (changed.has("themeToggle") && this.themeToggle) this.#applyInitialTheme();
  }

  toggleSidebar() {
    if (this._narrow) {
      this.sidebarOpen = !this.sidebarOpen;
    } else {
      this.sidebarCollapsed = !this.sidebarCollapsed;
      this.#write("sidebar", this.sidebarCollapsed ? "collapsed" : "expanded");
    }
    this.#announceSidebar();
  }

  #announceSidebar() {
    const collapsed = this._narrow ? !this.sidebarOpen : this.sidebarCollapsed;
    this.dispatchEvent(new CustomEvent("dp-shell-sidebar-toggle", { detail: { collapsed }, bubbles: true, composed: true }));
  }

  #closeDrawer() {
    if (!this._narrow || !this.sidebarOpen) return;
    this.sidebarOpen = false;
    this.#announceSidebar();
  }

  // A drawer left open on a screen that then gets wide would come back as a
  // stale open state.
  #onNarrowChange = () => {
    this._narrow = this.#narrowQuery?.matches ?? false;
    if (!this._narrow) this.sidebarOpen = false;
  };

  #onKeydown = (event: KeyboardEvent) => {
    if (event.key !== "Escape" || !this._narrow || !this.sidebarOpen) return;
    this.#closeDrawer();
    this.renderRoot.querySelector<HTMLElement>(".sidebar-toggle")?.focus();
  };

  // Choosing something in the drawer (a link, a button, a tab) closes it; a
  // field or a plain click on its background does not.
  #onSidebarClick = (event: MouseEvent) => {
    const chose = event.composedPath().some((n) => n instanceof Element && n !== event.currentTarget && n.matches("a[href], button, dp-button, dp-tabs, [data-dp-close]"));
    if (chose) this.#closeDrawer();
  };

  toggleTheme() {
    const next = this._theme === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    this.#write("theme", next);
    this._theme = next;
    this.dispatchEvent(new CustomEvent("dp-shell-theme-change", { detail: { theme: next }, bubbles: true, composed: true }));
  }

  // The slots are always drawn and only hidden when empty, so a sidebar or a
  // footer the page adds later, or while it is still being parsed, shows up.
  #syncSlots() {
    const has = (name: string) => Array.from(this.children).some((c) => c.getAttribute("slot") === name);
    this._hasSidebar = has("sidebar");
    this._hasFooter = has("footer");
    this._hasActions = has("actions");
  }

  #readTheme() {
    this._theme = document.documentElement.getAttribute("data-theme") === "dark" ? "dark" : "light";
  }

  // The page's own early script normally sets the theme before the first paint;
  // this covers a page that does not have one.
  #applyInitialTheme() {
    const root = document.documentElement;
    if (root.hasAttribute("data-theme")) return;
    const prefersDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
    root.setAttribute("data-theme", this.#read("theme") || (prefersDark ? "dark" : "light"));
  }

  #read(name: string): string | null {
    try {
      return localStorage.getItem(`${this.storageKey}-${name}`);
    } catch {
      return null;
    }
  }

  #write(name: string, value: string) {
    try {
      localStorage.setItem(`${this.storageKey}-${name}`, value);
    } catch {
      // A browser that refuses storage still gets the change for this page.
    }
  }

  render() {
    const drawerOpen = this._narrow && this.sidebarOpen;
    const sidebarShown = this._narrow ? this.sidebarOpen : !this.sidebarCollapsed;
    return html`
      <header part="header">
        ${this._hasSidebar
          ? html`<dp-button
              class="sidebar-toggle"
              part="sidebar-toggle"
              variant="icon"
              aria-label=${this.sidebarLabel || nothing}
              title=${this.sidebarLabel || nothing}
              aria-expanded=${sidebarShown ? "true" : "false"}
              @click=${this.toggleSidebar}
              >☰</dp-button
            >`
          : nothing}
        <slot name="brand"></slot>
        <slot name="header"></slot>
        <div class="actions" part="actions" ?hidden=${!this._hasActions}><slot name="actions"></slot></div>
        ${this.themeToggle
          ? html`<dp-button
              class="theme-toggle"
              part="theme-toggle"
              variant="icon"
              aria-label=${this.themeLabel || nothing}
              title=${this.themeLabel || nothing}
              @click=${this.toggleTheme}
              ><span class="theme-icon">${this._theme === "dark" ? "☀" : "☽"}</span></dp-button
            >`
          : nothing}
      </header>
      <div class=${this._hasSidebar ? "body" : "body no-sidebar"}>
        <aside part="sidebar" ?hidden=${!this._hasSidebar} ?inert=${!sidebarShown} @click=${this.#onSidebarClick}><slot name="sidebar"></slot></aside>
        <div class="scrim" part="scrim" ?hidden=${!drawerOpen} @click=${() => this.#closeDrawer()}></div>
        <main part="main"><slot></slot></main>
      </div>
      <footer part="footer" ?hidden=${!this._hasFooter}><slot name="footer"></slot></footer>
    `;
  }
}

customElements.define("dp-shell", DpShell);
