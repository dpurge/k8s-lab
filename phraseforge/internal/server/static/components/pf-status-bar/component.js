// pf-status-bar: a job-status + message area with a fixed height regardless
// of content, so the footer doesn't visibly jump between empty and
// populated states. Mirrors knowledge's kb-status-bar exactly (API and
// shape) — added globally in layout.html (present but unused on pages not
// yet part of the SPA, same pattern as pf-dialog), used by texts-app.js to
// replace the now-dead server-redirect flash message for fetch()-based
// save/delete/validation feedback.
(function () {
  const STYLE_ID = "pf-status-bar-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-status-bar/component.css";
    document.head.appendChild(link);
  }

  class PfStatusBar extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._built) return;
      this._built = true;

      const job = document.createElement("span");
      job.className = "pf-status-job";
      const message = document.createElement("span");
      message.className = "pf-status-message";

      this.append(job, message);
      this._job = job;
      this._message = message;
    }

    setJobStatus(text) {
      if (this._job) this._job.textContent = text || "";
    }

    setMessage(text, isError) {
      if (!this._message) return;
      this._message.textContent = text || "";
      this._message.classList.toggle("error", !!isError);
    }
  }

  customElements.define("pf-status-bar", PfStatusBar);
})();
