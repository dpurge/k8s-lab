// vocabulary-app.js: client-side rendering for the Vocabulary section of
// the unified SPA shell (app.html). Mirrors texts-app.js/dialogs-app.js's
// conventions (routed list/view URLs, language-filter-from-query-string fix
// applied from the start this time) with one structural addition: a list
// has items, so "Manage" combines the old vocab-edit.html's three pieces —
// the list-meta form, an item add/edit form, and the items table — as one
// client view. The old `?edit=<position>` query param becomes pure in-page
// state (`editingPosition`, reset to null after every mutation, never
// patched locally — item positions shift on delete, see
// specs/features/phraseforge-spa-vocabulary.md). Registers itself on
// window.pfSections.vocabulary for shell.js to call into — see
// specs/features/phraseforge-spa-unified-shell.md.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.vocabulary;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("vocabulary-app");
  const statusBar = document.getElementById("statusBar");

  // listContext is the list page/tag the current list or view belongs to;
  // forms and the view's back link return to it. Set by show(route). See
  // texts-app.js for the routing model (specs/features/phraseforge-hash-
  // navigation.md).
  let listContext = { page: 1, tag: "" };

  // listRequest numbers the list renders: a list response whose render has since
  // been replaced (by a newer list render or a detail view) must not touch the
  // page, where its container is gone or belongs to the newer render.
  let listRequest = 0;

  // goTo changes the URL (adding a history entry), which renders the route;
  // the render functions below never touch the URL themselves.
  function goTo(route) {
    window.pfRouter.navigate({ section: "vocabulary", ...route });
  }

  function esc(s) {
    return String(s == null ? "" : s).replace(
      /[&<>"']/g,
      (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
    );
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

  function languageOptions(selected) {
    return BOOT.languages
      .map((l) => `<option value="${esc(l.Code)}"${l.Code === selected ? " selected" : ""}>${esc(l.Name)}</option>`)
      .join("");
  }
  function scriptOptions(selected) {
    return BOOT.scripts
      .map((s) => `<option value="${esc(s.Code)}"${s.Code === selected ? " selected" : ""}>${esc(s.Name)}</option>`)
      .join("");
  }

  function renderCard(item) {
    const tags = (item.tags || [])
      .map((t) => `<a class="badge tag-link" href="#" data-tag-filter="${esc(t)}">${esc(t)}</a>`)
      .join("");
    const card = document.createElement("pf-card");
    card.innerHTML = `
      <h2><a href="#" data-open="${item.id}">${esc(item.title)}</a></h2>
      <div class="meta">
        <span class="badge">${esc(item.language)} / ${esc(item.script)}</span>
        ${tags}
      </div>
      <div class="meta" style="margin-top:0.4rem;">${item.itemCount} · ${esc(item.createdAt)}</div>
    `;
    return card;
  }

  // buildExportURL/handleImportFile/showImportResult mirror texts-app.js's
  // own copies exactly — see that file for the inline reasoning comments
  // this codebase's "deliberate duplication, not a shared abstraction"
  // convention (specs/tech-stack.md) keeps out of a shared module.
  function buildExportURL(endpoint, language, script, tagsInput) {
    const q = new URLSearchParams({ language, script });
    const tags = (tagsInput || "").trim();
    if (tags) q.set("tags", tags);
    return `${endpoint}?${q.toString()}`;
  }

  // showExportDialog mirrors texts-app.js's own copy exactly — see that
  // file for the inline reasoning comments.
  async function showExportDialog(endpoint, languageFilter) {
    const confirmDialog = document.getElementById("confirmDialog");
    const container = document.createElement("div");
    container.innerHTML = `
      <pf-field label="${esc(T("texts.field_language"))}" for="dlgExportLanguage">
        <select id="dlgExportLanguage">${languageOptions(languageFilter)}</select>
      </pf-field>
      <pf-field label="${esc(T("texts.field_script"))}" for="dlgExportScript">
        <select id="dlgExportScript">${scriptOptions()}</select>
      </pf-field>
      <pf-field label="${esc(T("texts.field_tags"))}" for="dlgExportTags">
        <input type="text" id="dlgExportTags" placeholder="${esc(T("texts.field_tags_hint"))}">
      </pf-field>
    `;
    const languageSelect = container.querySelector("#dlgExportLanguage");
    const scriptSelect = container.querySelector("#dlgExportScript");
    const tagsInput = container.querySelector("#dlgExportTags");
    const ok = await confirmDialog.showContent(container, { okLabel: T("texts.export"), wide: true });
    if (!ok) return;
    window.location.href = buildExportURL(endpoint, languageSelect.value, scriptSelect.value, tagsInput.value);
  }

  function handleImportFile(input, endpoint, tagFilter) {
    const file = input.files[0];
    input.value = "";
    if (!file) return;
    const reader = new FileReader();
    reader.onload = async () => {
      let res;
      try {
        res = await apiFetch(endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/yaml" },
          body: reader.result,
        });
      } catch (err) {
        statusBar.setMessage(err.message, true);
        return;
      }
      await showList(tagFilter, listContext.page);
      showImportResult(res);
    };
    reader.onerror = () => {
      statusBar.setMessage(T("texts.err_import_file_read"), true);
    };
    reader.readAsText(file);
  }

  function showImportResult(res) {
    const summary = `${T("texts.import_result_imported")}: ${res.imported}, ${T("texts.import_result_deleted")}: ${res.deleted}, ${T("texts.import_result_unchanged")}: ${res.unchanged}`;
    const hasErrors = !!(res.errors && res.errors.length > 0);
    statusBar.setMessage(summary, hasErrors);
    if (hasErrors) {
      const lines = res.errors.map((e) => `#${e.index}${e.id ? ` (id ${e.id})` : ""}: ${e.message}`);
      const confirmDialog = document.getElementById("confirmDialog");
      confirmDialog.confirm(`${T("texts.import_result_errors")}:\n${lines.join("\n")}`, { okLabel: T("texts.import_errors_close") });
    }
  }

  async function showList(tagFilter, page = 1) {
    const request = ++listRequest;
    statusBar.setMessage("");
    listContext = { page, tag: tagFilter || "" };
    const languageFilter = window.pfGetLanguageFilter();
    root.innerHTML = `
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <h1 style="margin-bottom:0;">${esc(T("vocabulary.title"))}</h1>
        ${
          BOOT.navFlags.canCreateAny
            ? `<div style="display:flex; gap:0.5rem; flex-wrap:wrap;">
                <pf-button variant="primary" id="newListBtn">${esc(T("texts.new"))}</pf-button>
                <pf-button variant="secondary" id="exportVocabularyBtn">${esc(T("texts.export"))}</pf-button>
                <pf-button variant="secondary" id="importVocabularyBtn">${esc(T("texts.import"))}</pf-button>
                <input type="file" id="importVocabularyFile" accept=".yaml,.yml" style="display:none;">
              </div>`
            : ""
        }
      </div>
      ${
        tagFilter
          ? `<p class="meta" style="margin-top:0.75rem;">${esc(T("texts.tag_filter"))} <span class="badge">${esc(tagFilter)}</span> <a href="#" id="clearTagFilter">${esc(T("texts.tag_filter_clear"))}</a></p>`
          : ""
      }
      <div id="listContainer"></div>
    `;
    const newBtn = document.getElementById("newListBtn");
    if (newBtn) newBtn.addEventListener("click", () => showNew());
    const exportBtn = document.getElementById("exportVocabularyBtn");
    if (exportBtn) {
      exportBtn.addEventListener("click", () => showExportDialog("/api/v1/vocabulary/export", languageFilter));
    }
    const importBtn = document.getElementById("importVocabularyBtn");
    const importFile = document.getElementById("importVocabularyFile");
    if (importBtn && importFile) {
      importBtn.addEventListener("click", () => importFile.click());
      importFile.addEventListener("change", () => handleImportFile(importFile, "/api/v1/vocabulary/import", tagFilter));
    }
    const clearLink = document.getElementById("clearTagFilter");
    if (clearLink) clearLink.addEventListener("click", (e) => { e.preventDefault(); goTo({}); });

    const query = new URLSearchParams();
    if (languageFilter) query.set("language", languageFilter);
    if (tagFilter) query.set("tag", tagFilter);
    query.set("page", String(page));
    let items;
    let pageCount;
    const isCurrent = () => request === listRequest && document.getElementById("listContainer") !== null;
    try {
      const data = await apiFetch("/api/v1/vocabulary?" + query.toString());
      items = data.items;
      pageCount = data.pageCount;
    } catch (e) {
      if (isCurrent()) statusBar.setMessage(e.message, true);
      return;
    }
    if (!isCurrent()) return;
    // A bookmarked page that no longer exists (items deleted since) lands on
    // the last page instead of an empty one.
    if (page > pageCount) {
      window.pfRouter.replace({ section: "vocabulary", page: pageCount, tag: tagFilter });
      return;
    }

    const container = document.getElementById("listContainer");
    if (items.length === 0) {
      const hint = BOOT.navFlags.canCreateAny ? "" : ` ${esc(T("texts.no_access"))}`;
      container.innerHTML = `<p class="empty-state" style="margin-top:1.5rem;">${esc(T("vocabulary.empty"))}${hint}</p>`;
      return;
    }
    const grid = document.createElement("div");
    grid.className = "card-grid";
    grid.style.marginTop = "1.5rem";
    for (const item of items) grid.appendChild(renderCard(item));
    container.appendChild(grid);

    grid.querySelectorAll("[data-open]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        goTo({ id: Number(a.dataset.open), page, tag: tagFilter });
      });
    });
    grid.querySelectorAll("[data-tag-filter]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        goTo({ tag: a.dataset.tagFilter });
      });
    });

    if (pageCount > 1) {
      const pager = document.createElement("pf-pager");
      pager.style.marginTop = "1.5rem";
      pager.setAttribute("page", String(page));
      pager.setAttribute("page-count", String(pageCount));
      pager.setAttribute("prev-label", T("texts.pagination_previous"));
      pager.setAttribute("next-label", T("texts.pagination_next"));
      pager.addEventListener("pf-pager-select", (e) => goTo({ page: e.detail.page, tag: tagFilter }));
      container.appendChild(pager);
    }
  }

  function showNew() {
    statusBar.setMessage("");
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <h1>${esc(T("vocabulary.new_title"))}</h1>
      <div class="form-card">
        <form id="newListForm">
          <input type="text" id="title" placeholder="${esc(T("texts.field_title"))}" autofocus required>
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="language">
              <select id="language" required>${languageOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("texts.field_script"))}" for="script">
              <select id="script" required>${scriptOptions("latn")}</select>
            </pf-field>
          </div>
          <pf-field label="${esc(T("texts.field_tags"))}" for="tags">
            <input type="text" id="tags" placeholder="${esc(T("texts.field_tags_hint"))}">
          </pf-field>
          <pf-button variant="primary" type="submit">${esc(T("texts.save"))}</pf-button>
        </form>
      </div>
      <p class="meta" style="margin-top:1rem;">${esc(T("vocabulary.new_hint"))}</p>
    `;
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(listContext.tag, listContext.page); });
    document.getElementById("newListForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        title: document.getElementById("title").value,
        language: document.getElementById("language").value,
        script: document.getElementById("script").value,
        tags: document.getElementById("tags").value,
      };
      try {
        const result = await apiFetch("/api/v1/vocabulary", { method: "POST", body: JSON.stringify(payload) });
        // A new list starts empty — land on Manage to start adding items,
        // matching handleVocabCreate's original redirect (not View).
        window.pfRouter.setUrl({ section: "vocabulary", id: result.id, ...listContext });
        showManage(result.id, null);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }

  async function showView(id) {
    statusBar.setMessage("");
    let list;
    try {
      list = await apiFetch(`/api/v1/vocabulary/${id}`);
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    const tags = (list.tags || [])
      .map((t) => `<a class="badge tag-link" href="#" data-tag-filter="${esc(t)}">${esc(t)}</a>`)
      .join("");
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <div class="resource-header">
        <div class="resource-actions">
          <pf-button variant="secondary" type="button" id="copyBtn">Copy</pf-button>
          ${list.canEdit ? `<pf-button variant="secondary" type="button" id="generateMissingTranslationsBtn">${esc(T("vocabulary.generate_missing_translations"))}</pf-button>` : ""}
          ${list.canEdit ? `<pf-button variant="secondary" type="button" id="reviewBtn">${esc(T("vocabulary.review"))}</pf-button>` : ""}
          ${list.canEdit ? `<pf-button variant="primary" type="button" id="editBtn">${esc(T("texts.edit"))}</pf-button><pf-button variant="danger" type="button" id="deleteBtn">${esc(T("texts.delete"))}</pf-button>` : ""}
        </div>
        <div>
          <h1 style="margin-bottom:0.25rem;">${esc(list.title)}</h1>
          <span class="badge">${esc(list.language)} / ${esc(list.script)}</span>
          ${tags}
        </div>
      </div>
      <textarea id="copy-vocabulary-markdown" class="copy-source" readonly>${esc(list.markdown)}</textarea>
      ${renderItemsTable(list, false, true)}
      <div id="hiddenToggle" class="vocab-hidden-toggle"></div>
    `;
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); goTo({ page: listContext.page, tag: listContext.tag }); });
    document.getElementById("copyBtn").addEventListener("click", (e) => {
      phraseforgeCopyMarkdown("copy-vocabulary-markdown", e.currentTarget.querySelector("button"));
    });
    const generateMissingTranslationsBtn = document.getElementById("generateMissingTranslationsBtn");
    if (generateMissingTranslationsBtn) {
      generateMissingTranslationsBtn.addEventListener("click", async () => {
        try {
          const result = await apiFetch(`/api/v1/vocabulary/${id}/generate-missing-translations`, { method: "POST" });
          statusBar.setMessage(result.enqueued > 0 ? T("vocabulary.generate_missing_translations_started") : T("vocabulary.generate_missing_translations_none"));
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    }
    wireHide(id, list);
    if (list.canEdit) wireReview(id, list);
    const editBtn = document.getElementById("editBtn");
    if (editBtn) editBtn.addEventListener("click", () => showManage(id, null));
    const deleteBtn = document.getElementById("deleteBtn");
    if (deleteBtn) {
      deleteBtn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("vocabulary.delete_confirm"), { danger: true });
        if (!ok) return;
        try {
          await apiFetch(`/api/v1/vocabulary/${id}`, { method: "DELETE" });
          statusBar.setMessage("Deleted.");
          goTo({ page: listContext.page, tag: listContext.tag });
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    }
    document.querySelectorAll("[data-tag-filter]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        goTo({ tag: a.dataset.tagFilter });
      });
    });
  }

  // withControls: View's table has no Edit/Delete column; Manage's does.
  // Examples sit in the notes cell below the notes text, in the notes text's
  // own size and color. They are source-language text, so they keep the
  // script's direction (RTL lines read right to left); the block itself shrinks
  // to its longest line (see .vocab-examples) and so starts where the notes do.
  function renderExamples(list, it) {
    if (!it.examples || it.examples.length === 0) return "";
    const lines = it.examples.map((e) => `<div class="vocab-example">${esc(e)}</div>`).join("");
    return `<div class="vocab-examples" dir="${esc(list.scriptDirection)}">${lines}</div>`;
  }

  // withActions adds the View page's Actions column: Hide for everyone (filled
  // in by wireHide) and, for editors, the review controls (wireReview).
  function renderItemsTable(list, withControls, withActions) {
    if (!list.items || list.items.length === 0) {
      return `<p class="empty-state" style="margin-top:1.5rem;">${esc(T("vocabulary.empty"))}</p>`;
    }
    const rows = list.items
      .map(
        (it) => `
      <tr${it.hidden ? ' class="vocab-hidden"' : ""}>
        <td class="${list.scriptEnlarged ? "prose-enlarged" : ""}" dir="${esc(list.scriptDirection)}" style="border-bottom:1px solid var(--border); padding:0.5rem;">${esc(it.phrase)}</td>
        <td style="border-bottom:1px solid var(--border); padding:0.5rem; color:var(--muted);">${esc(it.grammar)}</td>
        ${list.needsTranscription ? `<td dir="ltr" style="border-bottom:1px solid var(--border); padding:0.5rem;">${esc(it.transcription)}</td>` : ""}
        <td style="border-bottom:1px solid var(--border); padding:0.5rem;">${it.translation ? esc(it.translation) : "—"}</td>
        <td style="border-bottom:1px solid var(--border); padding:0.5rem; color:var(--muted);">${esc(it.notes)}${renderExamples(list, it)}</td>
        ${
          withActions
            ? `<td class="actions-cell">${list.canEdit ? `<span data-review-position="${it.position}"></span>` : ""}<span data-hide-position="${it.position}"></span></td>`
            : ""
        }
        ${
          withControls
            ? `<td style="border-bottom:1px solid var(--border); padding:0.5rem; white-space:nowrap;">
                <pf-button data-edit-position="${it.position}">${esc(T("texts.edit"))}</pf-button>
                <pf-button variant="danger" data-delete-position="${it.position}">${esc(T("texts.delete"))}</pf-button>
              </td>`
            : ""
        }
      </tr>`,
      )
      .join("");
    return `
      <div style="overflow-x:auto; margin-top:1.5rem;">
      <table class="vocab-table vocab-items-table" style="width:100%; border-collapse:collapse;">
        <thead>
          <tr>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_phrase"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_grammar"))}</th>
            ${list.needsTranscription ? `<th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_transcription"))}</th>` : ""}
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_translation"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_notes"))}</th>
            ${withActions ? `<th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_actions"))}</th>` : ""}
            ${withControls ? `<th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_actions"))}</th>` : ""}
          </tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>
      </div>
    `;
  }

  // --- Review (specs/features/phraseforge-vocabulary-review-job.md) ---
  //
  // A model reviews each item in the background; its proposals are stored and
  // shown here, and nothing changes until the editor applies one. The proposals
  // are model output, so the dialog is built with the DOM API (textContent),
  // never from an HTML string.

  function fmt(key, values) {
    return T(key).replace(/\{(\w+)\}/g, (_m, name) => (name in values ? values[name] : `{${name}}`));
  }

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

  // wireHide fills the Actions column's Hide / Unhide buttons and the "Show
  // hidden (N)" toggle under the table. Hidden rows stay in the table (the
  // server lists them with hidden: true) and are only displayed while the toggle
  // is on, so Hide and Unhide need no reload.
  function wireHide(id, list) {
    const table = root.querySelector(".vocab-items-table");
    const toggleSlot = document.getElementById("hiddenToggle");
    if (!table || !toggleSlot) return;
    const itemAt = (position) => list.items.find((it) => it.position === position);

    function refresh() {
      table.querySelectorAll("[data-hide-position]").forEach((slot) => {
        const position = Number(slot.dataset.hidePosition);
        const item = itemAt(position);
        slot.closest("tr").classList.toggle("vocab-hidden", item.hidden);
        slot.replaceChildren(button(T(item.hidden ? "vocabulary.unhide" : "vocabulary.hide"), "secondary", () => setHidden(position, !item.hidden)));
      });
      const count = list.items.filter((it) => it.hidden).length;
      const showing = table.classList.contains("show-hidden");
      if (count === 0) table.classList.remove("show-hidden");
      toggleSlot.replaceChildren(
        ...(count === 0 ? [] : [button(fmt(showing ? "vocabulary.hide_hidden" : "vocabulary.show_hidden", { n: count }), "secondary", () => {
          table.classList.toggle("show-hidden");
          refresh();
        })]),
      );
    }

    async function setHidden(position, hidden) {
      try {
        await apiFetch(`/api/v1/vocabulary/${id}/items/${position}/hidden`, { method: hidden ? "PUT" : "DELETE" });
        itemAt(position).hidden = hidden;
      } catch (e) {
        statusBar.setMessage(e.message, true);
      }
      refresh();
    }

    refresh();
  }

  // wireReview wires the View page's Review button and the review controls in the Actions column.
  // The column keeps itself up to date through pfPoll (poll.js) while a review
  // waits in the job queue.
  function wireReview(id, list) {
    const table = root.querySelector(".vocab-items-table");

    // load fetches the reviews, renders the column and says whether any item
    // still waits for its review job.
    async function load() {
      let data;
      try {
        data = await apiFetch(`/api/v1/vocabulary/${id}/reviews`);
      } catch (_e) {
        return false; // the column stays as it was; the next action retries
      }
      const byPosition = new Map(data.reviews.map((r) => [r.position, r]));
      const pending = new Set(data.pending);
      table.querySelectorAll("[data-review-position]").forEach((cell) => {
        const position = Number(cell.dataset.reviewPosition);
        cell.replaceChildren(...reviewCellContent(position, byPosition.get(position), pending.has(position)));
      });
      return pending.size > 0;
    }
    // An empty list has no table, hence no column to keep up to date.
    const poll = table ? window.pfPoll({ run: load, anchor: table }) : { kick() {} };
    const refresh = () => poll.kick();

    function reviewCellContent(position, review, isPending) {
      const queue = () => queueReview(`/api/v1/vocabulary/${id}/items/${position}/review`);
      if (isPending) return [el("span", { class: "meta" }, T("vocabulary.review_pending"))];
      if (!review) return [button(T("vocabulary.review"), "secondary", queue)];
      const open = () => openReviewDialog(id, position, list, refresh, showView);
      const label = review.stale ? T("vocabulary.review_stale") : review.open > 0 ? fmt("vocabulary.review_open", { n: review.open }) : T("vocabulary.review_reviewed");
      const out = [button(label, "secondary", open)];
      if (review.stale) out.push(button(T("vocabulary.review"), "secondary", queue));
      return out;
    }

    async function queueReview(path) {
      try {
        const result = await apiFetch(path, { method: "POST" });
        statusBar.setMessage(result.enqueued > 0 ? fmt("vocabulary.review_started", { n: result.enqueued }) : T("vocabulary.review_none"));
      } catch (e) {
        statusBar.setMessage(e.message, true);
      }
      refresh();
    }

    const reviewBtn = document.getElementById("reviewBtn");
    if (reviewBtn) reviewBtn.addEventListener("click", () => queueReview(`/api/v1/vocabulary/${id}/review`));
    refresh();
  }

  // openReviewDialog shows one item's proposals with Apply and Dismiss for each
  // and for all, inside pf-dialog's showContent. The dialog stays open while
  // proposals are acted on; on close the View page reloads if anything was
  // applied, else only the Review column refreshes.
  async function openReviewDialog(listId, position, list, refreshColumn, reloadView) {
    const confirmDialog = document.getElementById("confirmDialog");
    const base = `/api/v1/vocabulary/${listId}/items/${position}/review`;
    const container = el("div", { class: "review-dialog" });
    let changed = false;
    let message = "";

    async function act(action, indexes) {
      try {
        const res = await apiFetch(`${base}/${action}`, { method: "POST", body: JSON.stringify({ indexes }) });
        if (action === "apply") {
          changed = changed || res.results.some((r) => r.outcome === "applied");
          message = res.results.some((r) => r.outcome === "conflict") ? T("vocabulary.review_conflict") : "";
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
        await apiFetch(base, { method: "POST" });
        message = fmt("vocabulary.review_started", { n: 1 });
      } catch (e) {
        message = e.message;
      }
      render(currentView);
    }

    function values(rows) {
      const dl = el("dl", { class: "review-values" });
      for (const { label, text, dir } of rows) {
        dl.append(el("dt", {}, label), el("dd", { dir }, text));
      }
      return dl;
    }

    // The proposal's own rows: what is stored now and what the model suggests.
    function rowsOf(f) {
      const sourceDir = list.scriptDirection || "ltr";
      const sourceText = f.field === "phrase" || f.field === "example";
      const dir = sourceText ? sourceDir : "auto";
      const rows = [];
      if (f.current) rows.push({ label: T("vocabulary.review_current"), text: f.current, dir });
      if (f.action === "remove") rows.push({ label: T("vocabulary.review_suggested"), text: T("vocabulary.review_remove"), dir: "auto" });
      if (f.action === "replace") rows.push({ label: T("vocabulary.review_suggested"), text: f.suggestion, dir });
      if (f.field === "phrase" && f.action === "replace") {
        if (f.grammar) rows.push({ label: T("vocabulary.col_grammar"), text: f.grammar, dir: "ltr" });
        if (f.transcription) rows.push({ label: T("vocabulary.col_transcription"), text: f.transcription, dir: "ltr" });
      }
      return rows;
    }

    const fieldLabel = {
      phrase: "vocabulary.col_phrase", grammar: "vocabulary.col_grammar", transcription: "vocabulary.col_transcription",
      translation: "vocabulary.col_translation", notes: "vocabulary.col_notes", example: "vocabulary.review_field_example",
    };

    function proposal(f, n) {
      const isOpen = f.status === "open";
      const statusText = f.status === "applied" ? T("vocabulary.review_applied") : f.status === "dismissed" ? T("vocabulary.review_dismissed") : "";
      const head = el("div", { class: "review-item-head" },
        el("strong", {}, f.action === "comment" ? T("vocabulary.review_remark") : T(fieldLabel[f.field] || "vocabulary.review")),
        f.locale ? el("span", { class: "badge" }, f.locale) : null,
        f.action === "comment" ? el("span", { class: "meta" }, T(fieldLabel[f.field] || "vocabulary.review")) : null,
        statusText ? el("span", { class: "review-status" }, statusText) : null,
      );
      const body = el("div", { class: `review-item${isOpen ? "" : " is-done"}` }, head, values(rowsOf(f)));
      if (f.reason) body.append(el("div", { class: "review-reason" }, f.reason));
      if (isOpen) {
        const actions = el("div", { class: "review-actions" });
        if (f.action !== "comment" && !currentView.stale) actions.append(button(T("vocabulary.review_apply"), "primary", () => act("apply", [n])));
        actions.append(button(T("vocabulary.review_dismiss"), "secondary", () => act("dismiss", [n])));
        body.append(actions);
      }
      return body;
    }

    let currentView = { findings: [], stale: false };
    function render(view) {
      currentView = view;
      const open = view.findings.filter((f) => f.status === "open");
      const applicable = open.filter((f) => f.action !== "comment");
      const head = el("div", { class: "review-head" },
        el("h2", { dir: list.scriptDirection || "ltr" }, view.phrase),
        el("span", { class: "meta" }, `${T("vocabulary.review_reviewed_at")} ${new Date(view.createdAt).toLocaleString()}`),
      );
      const parts = [head];
      if (view.stale) parts.push(el("div", { class: "review-warning" }, T("vocabulary.review_stale_warning")));
      if (message) parts.push(el("div", { class: "review-message" }, message));
      if (view.findings.length === 0) parts.push(el("p", { class: "review-message" }, T("vocabulary.review_nothing")));
      view.findings.forEach((f, n) => parts.push(proposal(f, n)));
      const footer = el("div", { class: "review-footer" });
      if (applicable.length > 0 && !view.stale) footer.append(button(T("vocabulary.review_apply_all"), "primary", () => act("apply", [])));
      if (open.length > 0) footer.append(button(T("vocabulary.review_dismiss_all"), "secondary", () => act("dismiss", [])));
      footer.append(button(T("vocabulary.review_again"), "secondary", requeue));
      if (view.findings.length > 0 && open.length === 0) parts.push(el("p", { class: "review-message" }, T("vocabulary.review_none_open")));
      parts.push(footer);
      container.replaceChildren(...parts);
    }

    async function load() {
      try {
        render(await apiFetch(base));
      } catch (e) {
        statusBar.setMessage(e.message, true);
      }
    }

    await load();
    if (currentView.findings.length === 0 && !currentView.phrase) return;
    await confirmDialog.showContent(container, { okLabel: T("vocabulary.review_close"), wide: true, hideCancel: true });
    if (changed) reloadView(listId);
    else refreshColumn();
  }

  // showManage is the combined list-meta + item-entry + items-table view.
  // editingPosition is null (add mode) or the position of the item being
  // edited — pure in-page state, replacing the old ?edit=<position> query
  // param. Always re-fetches (never trusts a caller-supplied item list) —
  // positions shift on delete.
  async function showManage(id, editingPosition) {
    statusBar.setMessage("");
    let list;
    try {
      list = await apiFetch(`/api/v1/vocabulary/${id}`);
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    const editingItem = editingPosition != null ? list.items.find((it) => it.position === editingPosition) : null;
    // The position named by a stale Edit click (e.g. another tab deleted
    // it first) may no longer exist — fall back to add mode rather than
    // rendering a form for an item that's gone.
    const isEdit = !!editingItem;

    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <h1>${esc(T("vocabulary.edit_title"))}</h1>

      <div class="form-card">
        <form id="listMetaForm">
          <input type="text" id="listTitle" value="${esc(list.title)}" required>
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="language">
              <select id="language" required>${languageOptions(list.language)}</select>
            </pf-field>
            <pf-field label="${esc(T("texts.field_script"))}" for="script">
              <select id="script" required>${scriptOptions(list.script)}</select>
            </pf-field>
          </div>
          <pf-field label="${esc(T("texts.field_tags"))}" for="tags">
            <input type="text" id="tags" value="${esc((list.tags || []).join(", "))}" placeholder="${esc(T("texts.field_tags_hint"))}">
          </pf-field>
          <pf-button variant="primary" type="submit">${esc(T("texts.save_changes"))}</pf-button>
        </form>
      </div>

      <h2 style="margin-top:2rem;">${esc(T(isEdit ? "vocabulary.edit_item" : "vocabulary.add_item"))}</h2>
      <div class="form-card" style="max-width: none;">
        <form id="itemForm">
          <pf-field label="${esc(T("vocabulary.col_phrase"))}" for="field-source">
            <input type="text" id="field-source" value="${esc(editingItem?.phrase)}" autofocus required>
          </pf-field>
          <pf-field label="${esc(T("vocabulary.col_grammar"))}" for="grammar">
            <input type="text" id="grammar" value="${esc(editingItem?.grammar)}">
          </pf-field>
          <pf-field id="transcription-group" label="${esc(T("vocabulary.col_transcription"))}" for="field-transcription">
            <input type="text" id="field-transcription" value="${esc(editingItem?.transcription)}">
          </pf-field>
          <pf-field label="${esc(T("vocabulary.col_translation"))}" for="translation">
            <input type="text" id="translation" value="${esc(editingItem?.translation)}">
          </pf-field>
          <pf-field label="${esc(T("vocabulary.col_notes"))}" for="notes">
            <input type="text" id="notes" value="${esc(editingItem?.notes)}">
          </pf-field>
          <pf-field label="${esc(T("vocabulary.col_examples"))}" for="field-examples">
            <textarea id="field-examples" rows="3">${esc((editingItem?.examples || []).join("\n"))}</textarea>
            <div class="vocab-hint">${esc(T("vocabulary.examples_hint"))}</div>
          </pf-field>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T(isEdit ? "vocabulary.save_item" : "vocabulary.add_item"))}</pf-button>
            ${isEdit && !editingItem.transcription ? `<pf-button variant="secondary" class="transcribe-action" type="button" id="transcribeBtn">${esc(T("llm.transcribe"))}</pf-button>` : ""}
            ${isEdit && !editingItem.translation ? `<pf-button variant="secondary" type="button" id="translateBtn">${esc(T("llm.translate"))}</pf-button>` : ""}
            ${isEdit ? `<pf-button id="cancelEditLink">${esc(T("vocabulary.cancel_edit"))}</pf-button>` : ""}
          </div>
        </form>
      </div>

      ${renderItemsTable(list, true)}
    `;

    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showView(id); });

    // List-meta form: updates title/language/script/tags only, items untouched.
    document.getElementById("listMetaForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        title: document.getElementById("listTitle").value,
        language: document.getElementById("language").value,
        script: document.getElementById("script").value,
        tags: document.getElementById("tags").value,
      };
      try {
        await apiFetch(`/api/v1/vocabulary/${id}`, { method: "PUT", body: JSON.stringify(payload) });
        showManage(id, editingPosition);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    // background-generate-title-transcription-translation: background jobs
    // instead of phraseforgeGenerate's blocking call — a "started"
    // status-bar acknowledgment, matching generateFromText's own shape, no
    // live field update (the job's writeback fills field-transcription/
    // translation once it completes; reopening this item's edit form
    // afterward shows it). Translate targets only the viewer's own site
    // locale (BOOT.locale), same decision as the Text/Dialog View page.
    const transcribeBtn = document.getElementById("transcribeBtn");
    if (transcribeBtn) {
      transcribeBtn.addEventListener("click", async () => {
        try {
          await apiFetch(`/api/v1/vocabulary/${id}/items/${editingPosition}/generate-transcription`, { method: "POST" });
          statusBar.setMessage(T("texts.generate_transcription_started"));
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    }
    const translateBtn = document.getElementById("translateBtn");
    if (translateBtn) {
      translateBtn.addEventListener("click", async () => {
        try {
          await apiFetch(`/api/v1/vocabulary/${id}/items/${editingPosition}/generate-translation`, {
            method: "POST",
            body: JSON.stringify({ locale: BOOT.locale }),
          });
          statusBar.setMessage(T("texts.generate_translation_started"));
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    }
    pfInitEditor();

    const cancelLink = document.getElementById("cancelEditLink");
    if (cancelLink) cancelLink.addEventListener("click", (e) => { e.preventDefault(); showManage(id, null); });

    document.getElementById("itemForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        phrase: document.getElementById("field-source").value,
        grammar: document.getElementById("grammar").value,
        transcription: document.getElementById("field-transcription").value,
        translation: document.getElementById("translation").value,
        notes: document.getElementById("notes").value,
        // One example per line; blank lines are dropped by the server, and an
        // empty box clears the examples.
        examples: document.getElementById("field-examples").value.split(/\r?\n/),
      };
      try {
        if (isEdit) {
          await apiFetch(`/api/v1/vocabulary/${id}/items/${editingPosition}`, { method: "PUT", body: JSON.stringify(payload) });
        } else {
          await apiFetch(`/api/v1/vocabulary/${id}/items`, { method: "POST", body: JSON.stringify(payload) });
        }
        // Always back to add mode with a fresh fetch — positions may have
        // shifted (they don't on add/update, but re-fetching uniformly
        // keeps this path identical to delete's, rather than a special case).
        showManage(id, null);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    root.querySelectorAll("[data-edit-position]").forEach((a) => {
      a.addEventListener("click", async () => {
        await showManage(id, Number(a.dataset.editPosition));
        // The form is above the table: bring its heading into view and put the
        // cursor in the phrase, so it is clear which item is being edited.
        document.getElementById("itemForm").closest(".form-card").previousElementSibling.scrollIntoView({ behavior: "smooth", block: "start" });
        document.getElementById("field-source").focus({ preventScroll: true });
      });
    });
    root.querySelectorAll("[data-delete-position]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const position = Number(btn.dataset.deletePosition);
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("vocabulary.delete_item_confirm"), { danger: true });
        if (!ok) return;
        try {
          await apiFetch(`/api/v1/vocabulary/${id}/items/${position}`, { method: "DELETE" });
          // Positions after the deleted one just shifted down — always
          // return to add mode rather than trying to track where the
          // currently-being-edited item (if any) moved to.
          showManage(id, null);
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    });
  }

  window.pfSections.vocabulary = {
    setBootstrap(b) { BOOT = b; },
    // show renders a route: a view when it has an id (this also serves
    // dialog-vocabulary-models-generation's linked-list navigation), else a
    // list page. The route's page/tag become the context forms and Back
    // return to.
    show(route) {
      const { id = null, page = 1, tag = "" } = route || {};
      if (id != null) {
        listContext = { page, tag };
        return showView(id);
      }
      return showList(tag, page);
    },
  };
})();
