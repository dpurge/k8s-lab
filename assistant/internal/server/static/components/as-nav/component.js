// as-nav: the header's navigation. The items attribute is a JSON list of
// {key, href, label}; the active attribute names the key of the current one.
// It only renders links: the hash router in app.js does the navigating.
(function () {
  const STYLE_ID = "as-nav-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-nav/component.css";
    document.head.appendChild(link);
  }

  function parseItems(raw) {
    try {
      const items = JSON.parse(raw || "[]");
      return Array.isArray(items) ? items.filter((i) => i && typeof i.href === "string" && typeof i.label === "string") : [];
    } catch (e) {
      return [];
    }
  }

  class AsNav extends HTMLElement {
    static get observedAttributes() {
      return ["items", "active"];
    }

    connectedCallback() {
      ensureStyle();
      this._render();
    }

    attributeChangedCallback() {
      this._render();
    }

    _render() {
      if (!this.isConnected) return;
      this.textContent = "";
      const active = this.getAttribute("active");
      const nav = document.createElement("nav");
      nav.className = "as-nav-items";
      for (const item of parseItems(this.getAttribute("items"))) {
        const a = document.createElement("a");
        a.className = "as-nav-item";
        a.href = item.href;
        a.textContent = item.label;
        if (item.key !== undefined && item.key === active) {
          a.classList.add("active");
          a.setAttribute("aria-current", "page");
        }
        nav.appendChild(a);
      }
      this.appendChild(nav);
    }
  }

  customElements.define("as-nav", AsNav);
})();
