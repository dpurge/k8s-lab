// pf-tabs: a data-driven, click-switched tab bar — items in, an active id
// attribute, a bubbling pf-tabs-select event out. Never calls any host-page
// function itself, so it stays testable and reusable independent of how the
// host actually switches views (see component.test.js).
//
// Ported from knowledge's kb-nav, which already solves exactly this
// problem, but as its own component rather than a reuse of phraseforge's
// existing pf-nav: pf-nav wraps real server-rendered <a> links for
// phraseforge's multi-page site navigation (see its own header comment) —
// a client-only tab switcher with no server-known "active" state is exactly
// the case that comment says calls for a kb-nav-style component instead.
// Built with document.createElement, matching every other pf-* component's
// synchronous-render style, rather than kb-nav's fetched-component.html
// approach.
(function () {
  const STYLE_ID = "pf-tabs-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-tabs/component.css";
    document.head.appendChild(link);
  }

  class PfTabs extends HTMLElement {
    static get observedAttributes() {
      return ["active"];
    }

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
      ensureStyle();
      if (!this._nav) {
        this._nav = document.createElement("div");
        this._nav.className = "pf-tabs-items";
        this.appendChild(this._nav);
      }
      this._render();
    }

    attributeChangedCallback(name) {
      if (name === "active") this._updateActiveState();
    }

    _render() {
      if (!this._nav) return;
      this._nav.textContent = "";
      for (const item of this._items) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "pf-tabs-item";
        button.textContent = item.label;
        button.dataset.id = item.id;
        button.addEventListener("click", () => {
          this.active = item.id;
          this.dispatchEvent(new CustomEvent("pf-tabs-select", { detail: { id: item.id }, bubbles: true }));
        });
        this._nav.appendChild(button);
      }
      this._updateActiveState();
    }

    _updateActiveState() {
      if (!this._nav) return;
      const current = this.active;
      this._nav.querySelectorAll(".pf-tabs-item").forEach((button) => {
        button.classList.toggle("active", button.dataset.id === current);
      });
    }
  }

  customElements.define("pf-tabs", PfTabs);
})();
