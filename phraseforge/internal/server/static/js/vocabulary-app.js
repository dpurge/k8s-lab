// vocabulary-app.js: client-side rendering for the Vocabulary section of
// the unified SPA shell (app.html). Mirrors texts-app.js/dialogs-app.js's
// conventions (single-URL model, language-filter-from-query-string fix
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

  // Keyset pagination state — see texts-app.js's showList for the full
  // rationale (phraseforge-spa-pagination).
  let pageCursors = [null];
  let pageIndex = 0;
  let latestNextCursor = null;

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
      await showList(tagFilter);
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

  async function showList(tagFilter, pageDirection) {
    statusBar.setMessage("");
    if (pageDirection === "next") {
      pageCursors[pageIndex + 1] = latestNextCursor;
      pageIndex++;
    } else if (pageDirection === "prev") {
      pageIndex--;
    } else {
      pageCursors = [null];
      pageIndex = 0;
    }
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
    if (clearLink) clearLink.addEventListener("click", (e) => { e.preventDefault(); showList(); });

    const query = new URLSearchParams();
    if (languageFilter) query.set("language", languageFilter);
    if (tagFilter) query.set("tag", tagFilter);
    const cursor = pageCursors[pageIndex];
    if (cursor) query.set("cursor", cursor);
    let items;
    try {
      const data = await apiFetch("/api/v1/vocabulary?" + query.toString());
      items = data.items;
      latestNextCursor = data.nextCursor || null;
    } catch (e) {
      statusBar.setMessage(e.message, true);
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
        showView(Number(a.dataset.open));
      });
    });
    grid.querySelectorAll("[data-tag-filter]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        showList(a.dataset.tagFilter);
      });
    });

    if (pageIndex > 0 || latestNextCursor) {
      const pager = document.createElement("div");
      pager.style.cssText = "display:flex; justify-content:center; gap:0.75rem; margin-top:1.5rem;";
      if (pageIndex > 0) {
        const prevBtn = document.createElement("pf-button");
        prevBtn.setAttribute("variant", "secondary");
        prevBtn.textContent = T("texts.pagination_previous");
        prevBtn.addEventListener("click", () => showList(tagFilter, "prev"));
        pager.appendChild(prevBtn);
      }
      if (latestNextCursor) {
        const nextBtn = document.createElement("pf-button");
        nextBtn.setAttribute("variant", "secondary");
        nextBtn.textContent = T("texts.pagination_next");
        nextBtn.addEventListener("click", () => showList(tagFilter, "next"));
        pager.appendChild(nextBtn);
      }
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
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(); });
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
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <div>
          <h1 style="margin-bottom:0.25rem;">${esc(list.title)}</h1>
          <span class="badge">${esc(list.language)} / ${esc(list.script)}</span>
          ${tags}
        </div>
        <div class="resource-actions">
          <pf-button variant="secondary" type="button" id="copyBtn">Copy</pf-button>
          ${list.canEdit ? `<pf-button variant="secondary" type="button" id="generateMissingTranslationsBtn">${esc(T("vocabulary.generate_missing_translations"))}</pf-button>` : ""}
          ${list.canEdit ? `<pf-button variant="primary" type="button" id="editBtn">${esc(T("texts.edit"))}</pf-button><pf-button variant="danger" type="button" id="deleteBtn">${esc(T("texts.delete"))}</pf-button>` : ""}
        </div>
      </div>
      <textarea id="copy-vocabulary-markdown" class="copy-source" readonly>${esc(list.markdown)}</textarea>
      ${renderItemsTable(list, false)}
    `;
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(); });
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
          showList();
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    }
    document.querySelectorAll("[data-tag-filter]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        showList(a.dataset.tagFilter);
      });
    });
  }

  // withControls: View's table has no Edit/Delete column; Manage's does.
  function renderItemsTable(list, withControls) {
    if (!list.items || list.items.length === 0) {
      return `<p class="empty-state" style="margin-top:1.5rem;">${esc(T("vocabulary.empty"))}</p>`;
    }
    const rows = list.items
      .map(
        (it) => `
      <tr>
        <td class="${list.scriptEnlarged ? "prose-enlarged" : ""}" dir="${esc(list.scriptDirection)}" style="border-bottom:1px solid var(--border); padding:0.5rem;">${esc(it.phrase)}</td>
        <td style="border-bottom:1px solid var(--border); padding:0.5rem; color:var(--muted);">${esc(it.grammar)}</td>
        <td dir="ltr" style="border-bottom:1px solid var(--border); padding:0.5rem;">${esc(it.transcription)}</td>
        <td style="border-bottom:1px solid var(--border); padding:0.5rem;">${it.translation ? esc(it.translation) : "—"}</td>
        <td style="border-bottom:1px solid var(--border); padding:0.5rem; color:var(--muted);">${esc(it.notes)}</td>
        ${
          withControls
            ? `<td style="border-bottom:1px solid var(--border); padding:0.5rem; white-space:nowrap;">
                <a href="#" data-edit-position="${it.position}">${esc(T("texts.edit"))}</a>
                <button class="link-btn" type="button" data-delete-position="${it.position}" style="margin-left:0.5rem;">${esc(T("texts.delete"))}</button>
              </td>`
            : ""
        }
      </tr>`,
      )
      .join("");
    return `
      <div style="overflow-x:auto; margin-top:1.5rem;">
      <table class="vocab-table" style="width:100%; border-collapse:collapse;">
        <thead>
          <tr>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_phrase"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_grammar"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_transcription"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_translation"))}</th>
            <th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_notes"))}</th>
            ${withControls ? `<th style="text-align:left; border-bottom:1px solid var(--border); padding:0.5rem;">${esc(T("vocabulary.col_actions"))}</th>` : ""}
          </tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>
      </div>
    `;
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
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T(isEdit ? "vocabulary.save_item" : "vocabulary.add_item"))}</pf-button>
            ${isEdit && !editingItem.transcription ? `<pf-button variant="secondary" class="transcribe-action" type="button" id="transcribeBtn">${esc(T("llm.transcribe"))}</pf-button>` : ""}
            ${isEdit && !editingItem.translation ? `<pf-button variant="secondary" type="button" id="translateBtn">${esc(T("llm.translate"))}</pf-button>` : ""}
          </div>
          ${isEdit ? `<a href="#" id="cancelEditLink" class="back-link">${esc(T("vocabulary.cancel_edit"))}</a>` : ""}
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
      a.addEventListener("click", (e) => {
        e.preventDefault();
        showManage(id, Number(a.dataset.editPosition));
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
    // opts.viewId (dialog-vocabulary-models-generation's linked-list
    // navigation) opens straight to that list's view instead of the list —
    // every existing bare show() call is unaffected (opts is undefined).
    show(opts) {
      if (opts && opts.viewId != null) return showView(opts.viewId);
      return showList();
    },
  };
})();
