// as-card: a light-DOM wrapper that contributes CSS chrome (background,
// border, radius, padding) around whatever real content the page put inside
// it. The optional heading attribute is shown as a title above that content.
// It never reads, moves or rewrites its children.
(function () {
  const STYLE_ID = "as-card-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-card/component.css";
    document.head.appendChild(link);
  }

  class AsCard extends HTMLElement {
    static get observedAttributes() {
      return ["heading"];
    }

    connectedCallback() {
      ensureStyle();
      this._renderHeading();
    }

    attributeChangedCallback() {
      this._renderHeading();
    }

    _renderHeading() {
      if (!this.isConnected) return;
      const text = this.getAttribute("heading");
      let h = this.querySelector(":scope > .as-card-heading");
      if (!text) {
        if (h) h.remove();
        return;
      }
      if (!h) {
        h = document.createElement("h2");
        h.className = "as-card-heading";
        this.prepend(h);
      }
      h.textContent = text;
    }
  }

  customElements.define("as-card", AsCard);
})();
