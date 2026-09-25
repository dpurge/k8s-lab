// jobs-app.js: client-side rendering for the Jobs section of the unified SPA
// shell (app.html) — only present in the page at all when the current user
// is an admin (see app.html/handleApp), same gating as admin-app.js. See
// specs/features/phraseforge-jobs-menu.md.
//
// Own bootstrap/i18n comes from window.__PF_APP_BOOTSTRAP__.jobs, built by
// app.go's buildAppBootstrap (jobsAppI18n) the same way every other
// section's own section-specific strings are — see admin-app.js's BOOT/T
// pattern, which this mirrors exactly. Registers itself on
// window.pfSections.jobs for shell.js to call into — see
// specs/features/phraseforge-spa-unified-shell.md.
//
// No auto-polling — a manual Refresh button only, matching this app's
// existing convention (the admin dashboard only re-fetches after an
// explicit mutation; nothing in phraseforge polls today). See this
// feature's spec for the rationale.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.jobs;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("jobs-app");
  const statusBar = document.getElementById("statusBar");

  function esc(s) {
    return String(s == null ? "" : s).replace(
      /[&<>"']/g,
      (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
    );
  }

  // truncate shortens a long error message for the table column — the View
  // dialog always shows the full job (payload/result/step/error) untruncated.
  function truncate(s, max) {
    if (!s || s.length <= max) return s || "";
    return s.slice(0, max) + "…";
  }

  // field appends one dt/dd pair to dl, using textContent throughout — this
  // is what makes the View dialog's showContent() call safe even though
  // job.payload/job.result can contain arbitrary user-derived text (see
  // pf-dialog's showContent doc comment).
  function field(dl, label, value) {
    const dt = document.createElement("dt");
    dt.textContent = label;
    const dd = document.createElement("dd");
    dd.textContent = value;
    dl.append(dt, dd);
  }

  function jsonBlock(heading, value) {
    const h3 = document.createElement("h3");
    h3.textContent = heading;
    const pre = document.createElement("pre");
    pre.textContent = value === null || value === undefined ? "—" : JSON.stringify(value, null, 2);
    const wrap = document.createElement("div");
    wrap.append(h3, pre);
    return wrap;
  }

  // buildJobView renders job's full detail as structured content for
  // pf-dialog's showContent() — metadata fields, then Payload/Result each
  // in their own pretty-printed block, with Step/Error included only when
  // the job actually has one. Returns a DocumentFragment (not a wrapping
  // <div>) so its pieces land as .pf-dialog-content's own direct
  // children — that's what component.css's "> * + *" spacing rule
  // between blocks actually targets; nested one level deeper inside an
  // extra wrapper div, it would never match.
  function buildJobView(job) {
    const root = document.createDocumentFragment();
    const dl = document.createElement("dl");
    field(dl, T("jobs.view_id"), job.id);
    field(dl, T("jobs.col_kind"), job.kind);
    field(dl, T("jobs.col_priority"), job.priority);
    field(dl, T("jobs.col_status"), job.status);
    field(dl, T("jobs.col_created"), job.created_at);
    field(dl, T("jobs.view_updated"), job.updated_at);
    if (job.step) field(dl, T("jobs.view_step"), job.step);
    if (job.error) field(dl, T("jobs.col_error"), job.error);
    root.append(dl);
    root.append(jsonBlock(T("jobs.view_payload"), job.payload));
    root.append(jsonBlock(T("jobs.view_result"), job.result));
    return root;
  }

  async function apiFetch(path, options) {
    const res = await fetch(path, options);
    if (!res.ok) {
      let message = res.statusText;
      try {
        const body = await res.json();
        if (body && body.error && body.error.message) message = body.error.message;
      } catch (_e) {
        // response wasn't JSON (e.g. a 302 to /login on session expiry) — fall through to statusText
      }
      const err = new Error(message);
      err.status = res.status;
      throw err;
    }
    if (res.status === 204) return null;
    return res.json();
  }

  async function loadAndRender() {
    statusBar.setMessage("");
    let jobList;
    try {
      jobList = await apiFetch("/api/v1/admin/jobs");
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    render(jobList);
  }

  // Each action gets its own fixed table column so a row's buttons never
  // shift position depending on which other actions apply — in particular
  // Cancel and Delete share one column (jobActionCells's cancelOrDeleteCell
  // return value): they're mutually exclusive by status (a job is
  // cancellable — pending/running — before it's ever deletable —
  // done/failed/cancelled — never both at once), so reusing one column for
  // both reads as one consistent "the terminal action available right now"
  // slot instead of two buttons awkwardly swapping columns.
  function jobActionCells(j) {
    const btn = (dataAttr, label) =>
      `<button class="link-btn" type="button" data-${dataAttr}-id="${esc(j.id)}">${esc(label)}</button>`;
    const retryCell = j.status === "failed" || j.status === "cancelled" ? btn("retry", T("jobs.retry")) : "";
    const cancelOrDeleteCell =
      j.status === "pending" || j.status === "running" ? btn("cancel", T("jobs.cancel")) : btn("delete", T("texts.delete"));
    return { retryCell, cancelOrDeleteCell };
  }

  function render(jobList) {
    const rows =
      jobList
        .map((j) => {
          const { retryCell, cancelOrDeleteCell } = jobActionCells(j);
          return `
      <tr>
        <td>${esc(j.kind)}</td>
        <td>${esc(j.priority)}</td>
        <td>${esc(j.status)}</td>
        <td>${esc(j.createdAt)}</td>
        <td>${esc(truncate(j.error, 80))}</td>
        <td><a href="#" data-view-id="${esc(j.id)}">${esc(T("jobs.view"))}</a></td>
        <td>${retryCell}</td>
        <td>${cancelOrDeleteCell}</td>
      </tr>`;
        })
        .join("") || `<tr><td colspan="8"><em>${esc(T("jobs.empty"))}</em></td></tr>`;

    root.innerHTML = `
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <h1 style="margin-bottom:0;">${esc(T("jobs.title"))}</h1>
        <div style="display:flex; gap:0.5rem;">
          <pf-button variant="secondary" id="refreshJobsBtn">${esc(T("jobs.refresh"))}</pf-button>
          <pf-button variant="secondary" id="clearJobsBtn">${esc(T("jobs.clear"))}</pf-button>
        </div>
      </div>
      <div class="card" style="margin-top:1.5rem;">
        <div style="overflow-x:auto;">
          <table class="admin-table">
            <thead><tr><th>${esc(T("jobs.col_kind"))}</th><th>${esc(T("jobs.col_priority"))}</th><th>${esc(T("jobs.col_status"))}</th><th>${esc(T("jobs.col_created"))}</th><th>${esc(T("jobs.col_error"))}</th><th></th><th></th><th></th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      </div>
    `;

    document.getElementById("refreshJobsBtn").addEventListener("click", () => loadAndRender());

    document.getElementById("clearJobsBtn").addEventListener("click", async () => {
      const confirmDialog = document.getElementById("confirmDialog");
      const ok = await confirmDialog.confirm(T("jobs.clear_confirm"), { danger: true });
      if (!ok) return;
      try {
        await apiFetch("/api/v1/admin/jobs/clear", { method: "POST" });
        statusBar.setMessage(T("jobs.clear_done"));
        await loadAndRender();
      } catch (e) {
        statusBar.setMessage(e.message, true);
      }
    });

    root.querySelectorAll("[data-view-id]").forEach((a) => {
      a.addEventListener("click", async (e) => {
        e.preventDefault();
        const confirmDialog = document.getElementById("confirmDialog");
        try {
          const job = await apiFetch(`/api/v1/admin/jobs/${a.dataset.viewId}`);
          await confirmDialog.showContent(buildJobView(job), { okLabel: T("jobs.view_close"), wide: true, hideCancel: true });
        } catch (err) {
          statusBar.setMessage(err.message, true);
        }
      });
    });

    root.querySelectorAll("[data-cancel-id]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("jobs.cancel_confirm"), { danger: true });
        if (!ok) return;
        // Same immediate-disable pattern as Retry/Delete (see their own
        // handlers below) — jobActionCells never renders Retry/Delete
        // alongside Cancel on the same row, so there is nothing else on
        // this row to disable.
        btn.disabled = true;
        try {
          await apiFetch(`/api/v1/admin/jobs/${btn.dataset.cancelId}/cancel`, { method: "POST" });
          await loadAndRender();
          statusBar.setMessage("Job cancelled.");
        } catch (e) {
          statusBar.setMessage(e.message, true);
          btn.disabled = false;
        }
      });
    });

    root.querySelectorAll("[data-retry-id]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("jobs.retry_confirm"));
        if (!ok) return;
        // Disable immediately (before the await), not just after the
        // request settles: otherwise a double-click, or two admins retrying
        // the same failed/cancelled job at once, both pass this button's own
        // enabled state and each enqueue a real duplicate job. Also disables
        // Delete on the same row for consistency, matching
        // phraseforgeGenerate's disable-before-await/re-enable-in-catch
        // pattern (see layout.html) — jobActionCells always renders Retry
        // and Delete together (failed/cancelled), so deleteBtn is always
        // present here.
        const row = btn.closest("tr");
        const deleteBtn = row ? row.querySelector("[data-delete-id]") : null;
        btn.disabled = true;
        if (deleteBtn) deleteBtn.disabled = true;
        try {
          await apiFetch(`/api/v1/admin/jobs/${btn.dataset.retryId}/retry`, { method: "POST" });
          // Set the success message after the refresh, not before: like
          // admin-app.js's loadAndRender, this one also resets the status
          // bar to "" as its first step, which would otherwise immediately
          // wipe out a message set beforehand.
          await loadAndRender();
          statusBar.setMessage("Job requeued.");
        } catch (e) {
          statusBar.setMessage(e.message, true);
          btn.disabled = false;
          if (deleteBtn) deleteBtn.disabled = false;
        }
      });
    });

    root.querySelectorAll("[data-delete-id]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("jobs.delete_confirm"), { danger: true });
        if (!ok) return;
        // Same immediate-disable pattern as Retry above. Delete can be
        // enabled while Retry is disabled (status "done"), so Retry's own
        // prior disabled state is captured and restored rather than assumed.
        const row = btn.closest("tr");
        const retryBtn = row ? row.querySelector("[data-retry-id]") : null;
        const wasRetryDisabled = retryBtn ? retryBtn.disabled : true;
        btn.disabled = true;
        if (retryBtn) retryBtn.disabled = true;
        try {
          await apiFetch(`/api/v1/admin/jobs/${btn.dataset.deleteId}`, { method: "DELETE" });
          await loadAndRender();
          statusBar.setMessage("Job deleted.");
        } catch (e) {
          statusBar.setMessage(e.message, true);
          btn.disabled = false;
          if (retryBtn) retryBtn.disabled = wasRetryDisabled;
        }
      });
    });
  }

  window.pfSections.jobs = {
    setBootstrap(b) { BOOT = b; },
    show: loadAndRender,
  };
})();
