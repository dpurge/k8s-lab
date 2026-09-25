// pf-field: replaces the repeated <label class="field-label">Text</label>
// + sibling <input>/<select>/<textarea> pattern. Deliberately does NOT wrap
// or move its input child the way pf-button wraps its content — that
// requires waiting for the light-DOM child to already be parsed, which is
// exactly the timing bug pf-button hit and fixed with a MutationObserver
// (connectedCallback fires on the *opening* tag, before the browser has
// parsed what's between the tags). pf-field avoids the whole problem: it
// only ever *prepends* a brand-new <label> node, which never depends on
// whatever else is or isn't there yet — so editor.js's direct
// getElementById/className/dir/onkeydown manipulation of the actual input
// (never the label or a wrapper) survives untouched.
//
// Mirrors knowledge's kb-field exactly (deliberate per-app duplication —
// see specs/tech-stack.md's Frontend components convention), including its
// defensive MutationObserver re-insertion (specs/memory.md,
// 2026-09-22T18:50:40Z [gotcha]: observed in jsdom only, kept anyway since
// it costs nothing if a real browser never needs it).
(function () {
  const STYLE_ID = "pf-field-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-field/component.css";
    document.head.appendChild(link);
  }

  class PfField extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      this._insertLabel();
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
      label.className = "pf-field-label";
      label.textContent = this.getAttribute("label") || "";
      const forAttr = this.getAttribute("for");
      if (forAttr) label.setAttribute("for", forAttr);
      this.insertBefore(label, this.firstChild);
      this._label = label;
    }
  }

  customElements.define("pf-field", PfField);
})();
