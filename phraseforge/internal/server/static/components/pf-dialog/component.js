// pf-dialog: an in-app confirmation modal, replacing native confirm() (which
// looks like a browser chrome popup, not part of the app). One instance
// lives in the page (added once in layout.html); call .confirm(message)
// to show it, returning a Promise<boolean> resolved by OK/Cancel, Escape
// (false), or clicking the overlay outside the dialog box (false).
//
// Mirrors knowledge's kb-dialog exactly (deliberate per-app duplication —
// see specs/tech-stack.md's Frontend components convention), built from
// two pf-button instances. The harder part of this feature is NOT this
// component — it's that phraseforge's delete forms use a synchronous
// onsubmit="return confirm(...)", which cannot await this component's
// async .confirm(). See phraseforgeWireConfirmForms() in layout.html for
// how a real <form> submission is actually gated by this dialog.
//
// showContent(node, options) is a second way to open the same dialog, for
// callers that need structured markup (e.g. the Jobs page's View action)
// instead of one plain-text message — see jobs-app.js. Callers build node
// themselves via the DOM API (createElement/textContent), never by parsing
// a string of HTML, so dynamic data (job payload/result content) can never
// be interpreted as markup. options.wide widens the dialog box (and makes
// it user-resizable via CSS `resize`) for this case; confirm() never sets
// it, so every other caller is unaffected. options.hideCancel drops the
// Cancel button for a pure "view, nothing to confirm" case, where a
// separate Cancel would just be a second way to do exactly what the OK/
// Close button already does.
(function () {
  const STYLE_ID = "pf-dialog-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-dialog/component.css";
    document.head.appendChild(link);
  }

  class PfDialog extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._built) return;
      this._built = true;

      const box = document.createElement("div");
      box.className = "pf-dialog-box";

      const message = document.createElement("p");
      message.className = "pf-dialog-message";

      const content = document.createElement("div");
      content.className = "pf-dialog-content";

      const actions = document.createElement("div");
      actions.className = "pf-dialog-actions";

      const cancelButton = document.createElement("pf-button");
      cancelButton.setAttribute("variant", "secondary");
      cancelButton.textContent = "Cancel";
      cancelButton.addEventListener("click", () => this._close(false));

      const okButton = document.createElement("pf-button");
      okButton.setAttribute("variant", "primary");
      okButton.textContent = "OK";
      okButton.addEventListener("click", () => this._close(true));

      actions.append(cancelButton, okButton);
      box.append(message, content, actions);
      this.append(box);

      this._box = box;
      this._message = message;
      this._content = content;
      this._cancelButton = cancelButton;
      this._okButton = okButton;

      this.addEventListener("click", (event) => {
        if (event.target === this) this._close(false);
      });
      this.addEventListener("keydown", (event) => {
        if (event.key === "Escape") this._close(false);
      });
    }

    confirm(message, options = {}) {
      this._message.textContent = message;
      this._message.style.display = "";
      this._content.replaceChildren();
      return this._open(options);
    }

    // showContent: see this file's top-of-file comment. node is appended
    // as-is (never parsed from a string), so this never opens an XSS path
    // even though message paragraphs are hidden while it's showing.
    showContent(node, options = {}) {
      this._message.textContent = "";
      this._message.style.display = "none";
      this._content.replaceChildren(node);
      return this._open(options);
    }

    _open(options) {
      this._okButton.querySelector("button").textContent = options.okLabel || "OK";
      this._okButton.setAttribute("variant", options.danger ? "danger" : "primary");
      this._cancelButton.style.display = options.hideCancel ? "none" : "";
      this._box.classList.toggle("wide", !!options.wide);
      this._box.style.width = "";
      this._box.style.height = "";
      this.classList.add("open");
      this.setAttribute("tabindex", "-1");
      this.focus();
      return new Promise((resolve) => {
        this._resolve = resolve;
      });
    }

    _close(result) {
      this.classList.remove("open");
      const resolve = this._resolve;
      this._resolve = null;
      resolve?.(result);
    }
  }

  customElements.define("pf-dialog", PfDialog);
})();
