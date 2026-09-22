// kb-field: replaces the repeated <label>Text<input/textarea></label>
// pattern. Deliberately does NOT wrap or move its input child the way
// kb-button wraps its content — that requires waiting for the light-DOM
// child to already be parsed, which is exactly the timing bug kb-button hit
// and fixed with a MutationObserver (connectedCallback fires on the
// *opening* tag, before the browser has parsed what's between the tags).
// kb-field avoids the whole problem: it only ever *prepends* a brand-new
// <label> node, which never depends on whatever else is or isn't there yet.
(function () {
  const STYLE_ID = "kb-field-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-field/component.css";
    document.head.appendChild(link);
  }

  class KbField extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      this._insertLabel();
      // Defensive: observed in jsdom (a synchronous insertBefore() during
      // connectedCallback for an element written in static HTML got
      // silently reverted sometime after connectedCallback returned, for
      // reasons not fully understood and not confirmed to also happen in a
      // real browser). Re-insert the label if it's ever no longer present,
      // rather than trust that one synchronous insert is the end of it.
      if (!this._observer) {
        this._observer = new MutationObserver(() => {
          if (this._label && !this.contains(this._label)) {
            this._label = null;
            this._insertLabel();
          }
        });
        this._observer.observe(this, { childList: true });
      }
    }

    _insertLabel() {
      if (this._label) return;
      const label = document.createElement("label");
      label.className = "kb-field-label";
      label.textContent = this.getAttribute("label") || "";
      const forAttr = this.getAttribute("for");
      if (forAttr) label.setAttribute("for", forAttr);
      this.insertBefore(label, this.firstChild);
      this._label = label;
    }
  }

  customElements.define("kb-field", KbField);
})();
