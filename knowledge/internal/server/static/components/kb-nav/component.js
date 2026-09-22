// kb-nav: renders a horizontal/inline navigation control (see component.css
// for the visual treatment) from an `items` list ({id, label}), highlights
// the `active` one, and dispatches a `kb-nav-select` event on click. It
// never calls the host page's tab-switching function itself — the host
// listens for the event — so it stays testable and reusable independent of
// how the page actually switches views.
(function () {
  const STYLE_ID = "kb-nav-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-nav/component.css";
    document.head.appendChild(link);
  }

  let templatesPromise = null;

  function loadTemplates() {
    if (!templatesPromise) {
      templatesPromise = fetch("/components/kb-nav/component.html")
        .then((res) => res.text())
        .then((html) => {
          const holder = document.createElement("template");
          holder.innerHTML = html;
          return {
            shell: holder.content.getElementById("kb-nav-shell"),
            item: holder.content.getElementById("kb-nav-item"),
          };
        });
    }
    return templatesPromise;
  }

  class KbNav extends HTMLElement {
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

    async connectedCallback() {
      ensureStyle();
      const templates = await loadTemplates();
      if (!this._nav) {
        this.appendChild(templates.shell.content.cloneNode(true));
        this._nav = this.querySelector(".kb-nav-items");
        this._itemTemplate = templates.item;
      }
      this._render();
    }

    attributeChangedCallback(name) {
      if (name === "active") this._updateActiveState();
    }

    _render() {
      if (!this._nav || !this._itemTemplate) return;
      this._nav.textContent = "";
      for (const item of this._items) {
        const node = this._itemTemplate.content.cloneNode(true);
        const button = node.querySelector(".kb-nav-item");
        button.textContent = item.label;
        button.dataset.id = item.id;
        button.addEventListener("click", () => {
          this.active = item.id;
          this.dispatchEvent(new CustomEvent("kb-nav-select", { detail: { id: item.id }, bubbles: true }));
        });
        this._nav.appendChild(node);
      }
      this._updateActiveState();
    }

    _updateActiveState() {
      if (!this._nav) return;
      const current = this.active;
      this._nav.querySelectorAll(".kb-nav-item").forEach((button) => {
        button.classList.toggle("active", button.dataset.id === current);
      });
    }
  }

  customElements.define("kb-nav", KbNav);
})();
