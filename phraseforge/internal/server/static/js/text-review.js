// text-review.js: the Review buttons, status links and proposals dialog of the
// Text and Dialog view pages (specs/features/phraseforge-text-dialog-review-job.md).
// The transcription and the translations are reviewed separately, each with its
// own button, background job and stored proposals. Nothing changes until an
// editor applies a proposal. The proposals are model output, so the dialog is
// built with the DOM API (textContent), never from an HTML string. Shared by
// texts-app.js and dialogs-app.js through window.pfTextReview.attach.
(function () {
  const PARTS = ["transcription", "translation"];

  function el(tag, props, ...children) {
    const node = document.createElement(tag);
    for (const [name, value] of Object.entries(props || {})) {
      if (value == null || value === false) continue;
      if (name === "class") node.className = value;
      else node.setAttribute(name, value === true ? "" : value);
    }
    for (const child of children.flat()) {
      if (child != null && child !== false) node.append(child);
    }
    return node;
  }

  function button(label, variant, onClick) {
    const b = el("pf-button", { variant, type: "button" }, label);
    b.addEventListener("click", onClick);
    return b;
  }

  // attach({ root, base, T, apiFetch, statusBar, parts, reload }):
  //   root       the view page's root element
  //   base       "/api/v1/texts/<id>" or "/api/v1/dialogs/<id>"
  //   parts      { transcription: bool, translation: bool } which parts exist
  //   reload     re-renders the view page (after proposals were applied)
  function attach({ root, base, T, apiFetch, statusBar, parts, reload }) {
    const fmt = (key, values) => T(key).replace(/\{(\w+)\}/g, (_m, n) => (n in values ? values[n] : `{${n}}`));
    const actions = root.querySelector(".resource-actions");
    if (!actions) return;
    const row = el("div", { class: "review-row meta" });
    root.querySelector(".resource-header").after(row);
    const state = {}; // part -> { pending, review }

    // load fetches each part's review state, renders the row and says whether
    // any part still waits for its review job. pfPoll (poll.js) keeps the row up
    // to date while one does.
    async function load() {
      let anyPending = false;
      for (const part of PARTS.filter((p) => parts[p])) {
        try {
          state[part] = await apiFetch(`${base}/review/${part}`);
        } catch (_e) {
          continue;
        }
        anyPending = anyPending || state[part].pending;
      }
      renderRow();
      return anyPending;
    }
    const poll = window.pfPoll({ run: load, anchor: row });
    const refresh = () => poll.kick();

    function renderRow() {
      row.replaceChildren();
      for (const part of PARTS.filter((p) => parts[p])) {
        const { pending, review } = state[part] || {};
        const label = T(`texts.review_field_${part}`);
        if (pending) {
          row.append(el("span", {}, `${label}: ${T("texts.review_pending")}`));
        } else if (review) {
          const open = review.findings.filter((f) => f.status === "open").length;
          const text = review.stale ? T("texts.review_stale") : open > 0 ? fmt("texts.review_open", { n: open }) : T("texts.review_reviewed");
          const a = el("a", { href: "#" }, `${label}: ${text}`);
          a.addEventListener("click", (e) => { e.preventDefault(); openDialog(part); });
          row.append(a);
        }
      }
    }

    async function queue(part) {
      try {
        const result = await apiFetch(`${base}/review/${part}`, { method: "POST" });
        statusBar.setMessage(result.enqueued > 0 ? fmt("texts.review_started", { n: result.enqueued }) : T("texts.review_none"));
      } catch (e) {
        statusBar.setMessage(e.message, true);
      }
      refresh();
    }

    // The queue buttons sit with the other actions, before Edit.
    const editBtn = document.getElementById("editBtn");
    for (const part of PARTS.filter((p) => parts[p])) {
      const b = button(T(`texts.review_btn_${part}`), "secondary", () => queue(part));
      actions.insertBefore(b, editBtn || null);
    }

    async function openDialog(part) {
      const confirmDialog = document.getElementById("confirmDialog");
      const container = el("div", { class: "review-dialog" });
      let changed = false;
      let message = "";
      let view = null;

      async function act(action, indexes) {
        try {
          const res = await apiFetch(`${base}/review/${part}/${action}`, { method: "POST", body: JSON.stringify({ indexes }) });
          if (action === "apply") {
            changed = changed || res.results.some((r) => r.outcome === "applied");
            message = res.results.some((r) => r.outcome === "conflict") ? T("texts.review_conflict") : "";
          } else {
            message = "";
          }
          render(res.review);
        } catch (e) {
          message = e.message;
          await load();
        }
      }

      async function requeue() {
        try {
          await apiFetch(`${base}/review/${part}`, { method: "POST" });
          message = fmt("texts.review_started", { n: 1 });
        } catch (e) {
          message = e.message;
        }
        render(view);
      }

      function proposal(f, n) {
        const isOpen = f.status === "open";
        const statusText = f.status === "applied" ? T("texts.review_applied") : f.status === "dismissed" ? T("texts.review_dismissed") : "";
        const head = el("div", { class: "review-item-head" },
          el("strong", {}, f.action === "comment" ? T("texts.review_remark") : T(`texts.review_field_${f.field}`)),
          f.locale ? el("span", { class: "badge" }, f.locale) : null,
          statusText ? el("span", { class: "review-status" }, statusText) : null,
        );
        const values = el("dl", { class: "review-values" },
          f.current ? [el("dt", {}, T("texts.review_current")), el("dd", { class: "review-long", dir: "auto" }, f.current)] : null,
          f.action === "replace" ? [el("dt", {}, T("texts.review_suggested")), el("dd", { class: "review-long", dir: "auto" }, f.suggestion)] : null,
        );
        const body = el("div", { class: `review-item${isOpen ? "" : " is-done"}` }, head, values);
        if (f.reason) body.append(el("div", { class: "review-reason" }, f.reason));
        if (isOpen) {
          const buttons = el("div", { class: "review-actions" });
          if (f.action !== "comment" && !view.stale) buttons.append(button(T("texts.review_apply"), "primary", () => act("apply", [n])));
          buttons.append(button(T("texts.review_dismiss"), "secondary", () => act("dismiss", [n])));
          body.append(buttons);
        }
        return body;
      }

      function render(next) {
        view = next;
        const open = view.findings.filter((f) => f.status === "open");
        const applicable = open.filter((f) => f.action !== "comment");
        const parts_ = [el("div", { class: "review-head" },
          el("h2", {}, T(`texts.review_btn_${part}`)),
          el("span", { class: "meta" }, `${T("texts.review_reviewed_at")} ${new Date(view.createdAt).toLocaleString()}`),
        )];
        if (view.stale) parts_.push(el("div", { class: "review-warning" }, T("texts.review_stale_warning")));
        if (message) parts_.push(el("div", { class: "review-message" }, message));
        if (view.findings.length === 0) parts_.push(el("p", { class: "review-message" }, T("texts.review_nothing")));
        view.findings.forEach((f, n) => parts_.push(proposal(f, n)));
        if (view.findings.length > 0 && open.length === 0) parts_.push(el("p", { class: "review-message" }, T("texts.review_none_open")));
        const footer = el("div", { class: "review-footer" });
        if (applicable.length > 0 && !view.stale) footer.append(button(T("texts.review_apply_all"), "primary", () => act("apply", [])));
        if (open.length > 0) footer.append(button(T("texts.review_dismiss_all"), "secondary", () => act("dismiss", [])));
        footer.append(button(T("texts.review_again"), "secondary", requeue));
        parts_.push(footer);
        container.replaceChildren(...parts_);
      }

      async function load() {
        try {
          const res = await apiFetch(`${base}/review/${part}`);
          if (res.review) render(res.review);
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      }

      await load();
      if (!view) return;
      await confirmDialog.showContent(container, { okLabel: T("texts.review_close"), wide: true, hideCancel: true });
      if (changed) reload();
      else refresh();
    }

    refresh();
  }

  window.pfTextReview = { attach };
})();
