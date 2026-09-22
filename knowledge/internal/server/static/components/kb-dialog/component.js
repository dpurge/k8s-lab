// kb-dialog: an in-app confirmation modal, replacing native confirm() (which
// looks like a browser chrome popup, not part of the app). One instance
// lives in the page; call .confirm(message) to show it, returning a
// Promise<boolean> resolved by OK/Cancel, Escape (false), or clicking the
// overlay outside the dialog box (false).
(function () {
  const STYLE_ID = "kb-dialog-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-dialog/component.css";
    document.head.appendChild(link);
  }

  class KbDialog extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._built) return;
      this._built = true;

      const box = document.createElement("div");
      box.className = "kb-dialog-box";

      const message = document.createElement("p");
      message.className = "kb-dialog-message";

      const actions = document.createElement("div");
      actions.className = "kb-dialog-actions";

      const cancelButton = document.createElement("kb-button");
      cancelButton.setAttribute("variant", "secondary");
      cancelButton.textContent = "Cancel";
      cancelButton.addEventListener("click", () => this._close(false));

      const okButton = document.createElement("kb-button");
      okButton.setAttribute("variant", "primary");
      okButton.textContent = "OK";
      okButton.addEventListener("click", () => this._close(true));

      actions.append(cancelButton, okButton);
      box.append(message, actions);
      this.append(box);

      this._box = box;
      this._message = message;
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
      this._okButton.querySelector("button").textContent = options.okLabel || "OK";
      this._okButton.classList.toggle("kb-dialog-danger", !!options.danger);
      // kb-button forwards variant via CSS attribute selectors; danger
      // confirmations (delete/discard) get the danger look on OK.
      this._okButton.setAttribute("variant", options.danger ? "danger" : "primary");
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

  customElements.define("kb-dialog", KbDialog);
})();
