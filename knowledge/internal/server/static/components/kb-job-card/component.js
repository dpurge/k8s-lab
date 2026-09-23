// kb-job-card: replaces renderJob()'s string-built markup (formerly
// app.js's `.item` div with inline onclick="run(() => retryJob(...))"/
// deleteJob(...) handlers) for a single /api/v1/jobs entry. Takes a `job`
// property (setter-triggered render, matching kb-nav's `items` and
// kb-message's `message` pattern) rather than attributes, since a job's
// `error` field can be a multi-line string that's awkward and escape-prone
// as an HTML attribute.
//
// Deliberately NOT built on kb-card: kb-card binds a click listener to
// itself and fires kb-card-select on any click, including clicks on a
// nested action button, which would misroute a Retry/Delete click into
// whatever handles kb-card-select. kb-card also implies clickability
// (pointer cursor, hover state) that a job row doesn't have (no detail
// view). This component only ever emits kb-job-retry/kb-job-delete, and
// only from their own buttons — it must never bind a listener on itself.
(function () {
  const STYLE_ID = "kb-job-card-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-job-card/component.css";
    document.head.appendChild(link);
  }

  // jobStatusLabel distinguishes a job that's running but hasn't been
  // claimed by the queue's worker yet (no step set) — genuinely "pending",
  // not yet doing anything — from one actively being worked on, since
  // jobs.Status itself only has running/done/failed (see
  // specs/features/knowledge-background-queue.md). Moved here from
  // app.js's jobStatusLabel(), which had exactly one caller (renderJob).
  function jobStatusLabel(job) {
    if (job.status === "running") return job.step ? "active" : "pending";
    return job.status;
  }

  function makeActionButton(label, variant, eventName, job) {
    const button = document.createElement("kb-button");
    if (variant) button.setAttribute("variant", variant);
    button.textContent = label;
    button.addEventListener("click", () => {
      button.dispatchEvent(new CustomEvent(eventName, { detail: { id: job.id }, bubbles: true }));
    });
    return button;
  }

  class KbJobCard extends HTMLElement {
    connectedCallback() {
      ensureStyle();
    }

    get job() {
      return this._job;
    }

    set job(value) {
      this._job = value || {};
      this._render();
    }

    _render() {
      const job = this._job || {};

      // Clear any previously rendered content before re-rendering —
      // matches kb-nav/kb-message's own re-render pattern (textContent =
      // "" rather than innerHTML = "").
      this.textContent = "";

      const label = jobStatusLabel(job);

      const heading = document.createElement("b");
      heading.textContent = `${job.kind || ""} `;
      const badge = document.createElement("span");
      badge.className = `kb-job-card-status kb-job-card-status-${label}`;
      badge.textContent = label;
      heading.appendChild(badge);
      this.appendChild(heading);

      if (job.source_ref) {
        const source = document.createElement("div");
        source.className = "muted";
        source.textContent = `${job.source_kind}: ${job.source_ref}`;
        this.appendChild(source);
      }

      if (job.step) {
        const step = document.createElement("div");
        step.className = "muted";
        step.textContent = job.step;
        this.appendChild(step);
      }

      if (job.error) {
        // job.error is rendered as literal text only — it could contain
        // markup, and must never be parsed as HTML (this is the actual
        // security fix motivating this component, not just a refactor).
        const error = document.createElement("div");
        error.className = "error";
        error.textContent = job.error;
        this.appendChild(error);
      }

      const updated = document.createElement("div");
      updated.className = "muted";
      updated.textContent = job.updated_at ? `Updated ${new Date(job.updated_at).toLocaleString()}` : "";
      this.appendChild(updated);

      if (job.status === "failed") {
        const actions = document.createElement("div");
        actions.className = "actions";
        actions.appendChild(makeActionButton("Retry", null, "kb-job-retry", job));
        actions.appendChild(makeActionButton("Delete", "danger", "kb-job-delete", job));
        this.appendChild(actions);
      } else if (job.status !== "running") {
        const actions = document.createElement("div");
        actions.className = "actions";
        actions.appendChild(makeActionButton("Delete", "danger", "kb-job-delete", job));
        this.appendChild(actions);
      }
    }
  }

  customElements.define("kb-job-card", KbJobCard);
})();
