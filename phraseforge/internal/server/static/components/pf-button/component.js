// pf-button: wraps a real <button> so it keeps native keyboard/focus/ARIA
// semantics, while letting the host page style it via the `variant`
// attribute (primary/secondary/danger/icon — see component.css) instead of
// getting only the browser's default, unstyled button look.
//
// Mirrors knowledge's kb-button exactly (deliberate per-app duplication,
// not a shared file — see specs/tech-stack.md's Frontend components
// convention). Includes kb-button's MutationObserver-based fix for static
// light-DOM content from the start: connectedCallback fires the instant the
// browser inserts <pf-button>'s OPENING tag during HTML parsing, before the
// parser has appended content written between the tags (e.g. "☰") — a fixed
// microtask delay was tried for kb-button and did not reliably win that race
// in real Chrome, so this reacts to the actual mutation instead of assuming
// a fixed number of ticks (see specs/memory.md, 2026-09-22T17:39:25Z).
(function () {
  const STYLE_ID = "pf-button-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-button/component.css";
    document.head.appendChild(link);
  }

  class PfButton extends HTMLElement {
    static get observedAttributes() {
      // aria-label is forwarded (unlike title, which the browser's native
      // tooltip lookup already walks up to an ancestor for) because a
      // screen reader computes a <button>'s accessible name from the
      // button itself, not from a non-interactive custom-element ancestor.
      return ["disabled", "type", "aria-label"];
    }

    connectedCallback() {
      ensureStyle();
      if (this._button) return;
      if (this.firstChild) {
        this._wrap();
        return;
      }
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
      const ariaLabel = this.getAttribute("aria-label");
      if (ariaLabel !== null) button.setAttribute("aria-label", ariaLabel);
      // Move whatever the author put inside <pf-button> (text, an icon
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
      if (name === "aria-label") {
        if (value === null) this._button.removeAttribute("aria-label");
        else this._button.setAttribute("aria-label", value);
      }
    }
  }

  customElements.define("pf-button", PfButton);
})();
