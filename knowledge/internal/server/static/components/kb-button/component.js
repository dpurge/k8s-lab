// kb-button: wraps a real <button> so it keeps native keyboard/focus/ARIA
// semantics, while letting the host page style it via the `variant`
// attribute (primary/secondary/danger/icon — see component.css) instead of
// getting only the browser's default, unstyled button look.
(function () {
  const STYLE_ID = "kb-button-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-button/component.css";
    document.head.appendChild(link);
  }

  class KbButton extends HTMLElement {
    static get observedAttributes() {
      return ["disabled", "type"];
    }

    connectedCallback() {
      ensureStyle();
      if (this._button) return;
      if (this.firstChild) {
        this._wrap();
        return;
      }
      // connectedCallback fires the instant the browser inserts <kb-button>
      // into the tree — for an element present in the initial HTML, that's
      // when its OPENING tag is parsed, before the parser has appended the
      // light-DOM content between the tags (e.g. "🌓"). A fixed microtask
      // delay was tried and did not reliably win that race in real Chrome
      // (confirmed live: still produced an empty <button> plus the text as a
      // stray sibling). Reacting to the actual mutation, whenever it happens,
      // is the robust fix — no timing assumption at all.
      if (this._observer) return;
      this._observer = new MutationObserver(() => {
        if (!this.firstChild) return;
        this._observer.disconnect();
        this._observer = null;
        this._wrap();
      });
      this._observer.observe(this, { childList: true });
    }

    _wrap() {
      if (this._button) return;
      const button = document.createElement("button");
      button.type = this.getAttribute("type") || "button";
      if (this.hasAttribute("disabled")) button.disabled = true;
      // Move whatever the author put inside <kb-button> (text, an icon
      // span, ...) into the real button, rather than requiring a separate
      // slot syntax for the common case.
      while (this.firstChild) button.appendChild(this.firstChild);
      this.appendChild(button);
      this._button = button;
    }

    attributeChangedCallback(name, _oldValue, value) {
      if (!this._button) return;
      if (name === "disabled") this._button.disabled = value !== null;
      if (name === "type") this._button.type = value || "button";
    }
  }

  customElements.define("kb-button", KbButton);
})();
