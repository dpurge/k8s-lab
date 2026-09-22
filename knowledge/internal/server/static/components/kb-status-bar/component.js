// kb-status-bar: the footer's job-status + message area, as one component
// with a fixed height regardless of content — the previous plain <span>s
// let the footer visibly grow/shrink between empty and populated, since
// nothing gave it a stable minimum size.
(function () {
  const STYLE_ID = "kb-status-bar-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-status-bar/component.css";
    document.head.appendChild(link);
  }

  class KbStatusBar extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._built) return;
      this._built = true;

      const job = document.createElement("span");
      job.className = "kb-status-job";
      const message = document.createElement("span");
      message.className = "kb-status-message";

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

  customElements.define("kb-status-bar", KbStatusBar);
})();
