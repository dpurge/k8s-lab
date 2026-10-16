// as-status-bar: a job-status and message area of fixed height, so the footer
// does not jump between empty and populated states. Same API and shape as
// phraseforge's pf-status-bar: setJobStatus(text) and setMessage(text, isError).
(function () {
  const STYLE_ID = "as-status-bar-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-status-bar/component.css";
    document.head.appendChild(link);
  }

  class AsStatusBar extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._built) return;
      this._built = true;
      const job = document.createElement("span");
      job.className = "as-status-job";
      const message = document.createElement("span");
      message.className = "as-status-message";
      message.setAttribute("role", "status");
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

  customElements.define("as-status-bar", AsStatusBar);
})();
