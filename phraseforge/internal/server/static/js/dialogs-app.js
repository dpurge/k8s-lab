// dialogs-app.js: client-side rendering for the Dialogs section of the
// unified SPA shell (app.html). Mirrors texts-app.js exactly (single-URL
// model, no deep-linking, per phraseforge-spa-shell-texts's decision) with
// two dialog-specific differences: renderedBody/etc. can come back with a
// sibling *Error field instead (malformed turn syntax — a normal 200, not
// an HTTP error), shown as the same inline notice dialogs-view.html used;
// and the body field hint uses dialogs' own i18n key. Registers itself on
// window.pfSections.dialogs for shell.js to call into — see
// specs/features/phraseforge-spa-unified-shell.md.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.dialogs;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("dialogs-app");
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
      <div class="meta" style="margin-top:0.4rem;">${esc(item.createdAt)}</div>
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
        <h1 style="margin-bottom:0;">${esc(T("dialogs.title"))}</h1>
        ${
          BOOT.navFlags.canCreateAny
            ? `<div style="display:flex; gap:0.5rem; flex-wrap:wrap;">
                <pf-button variant="primary" id="newDialogBtn">${esc(T("texts.new"))}</pf-button>
                <pf-button variant="secondary" id="ingestDialogBtn">${esc(T("texts.ingest"))}</pf-button>
                <pf-button variant="secondary" id="exportDialogsBtn">${esc(T("texts.export"))}</pf-button>
                <pf-button variant="secondary" id="importDialogsBtn">${esc(T("texts.import"))}</pf-button>
                <input type="file" id="importDialogsFile" accept=".yaml,.yml" style="display:none;">
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
    const newBtn = document.getElementById("newDialogBtn");
    if (newBtn) newBtn.addEventListener("click", () => showNew());
    const ingestBtn = document.getElementById("ingestDialogBtn");
    if (ingestBtn) ingestBtn.addEventListener("click", () => showIngest());
    const exportBtn = document.getElementById("exportDialogsBtn");
    if (exportBtn) {
      exportBtn.addEventListener("click", () => showExportDialog("/api/v1/dialogs/export", languageFilter));
    }
    const importBtn = document.getElementById("importDialogsBtn");
    const importFile = document.getElementById("importDialogsFile");
    if (importBtn && importFile) {
      importBtn.addEventListener("click", () => importFile.click());
      importFile.addEventListener("change", () => handleImportFile(importFile, "/api/v1/dialogs/import", tagFilter));
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
      const data = await apiFetch("/api/v1/dialogs?" + query.toString());
      items = data.items;
      latestNextCursor = data.nextCursor || null;
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }

    const container = document.getElementById("listContainer");
    if (items.length === 0) {
      const hint = BOOT.navFlags.canCreateAny ? "" : ` ${esc(T("texts.no_access"))}`;
      container.innerHTML = `<p class="empty-state" style="margin-top:1.5rem;">${esc(T("dialogs.empty"))}${hint}</p>`;
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

  function showForm(existing) {
    statusBar.setMessage("");
    const isEdit = !!existing;
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <h1>${esc(T(isEdit ? "dialogs.edit_title" : "dialogs.new_title"))}</h1>
      <div class="form-card" style="max-width: none;">
        <form id="dialogForm">
          <input type="text" id="title" placeholder="${esc(T("texts.field_title"))}" ${isEdit ? "" : "autofocus"} required value="${esc(existing?.title)}">
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="language">
              <select id="language" required>${languageOptions(existing?.language)}</select>
            </pf-field>
            <pf-field label="${esc(T("texts.field_script"))}" for="script">
              <select id="script" required>${scriptOptions(existing?.script)}</select>
            </pf-field>
          </div>
          <pf-field label="${esc(T("texts.field_tags"))}" for="tags">
            <input type="text" id="tags" placeholder="${esc(T("texts.field_tags_hint"))}" value="${esc((existing?.tags || []).join(", "))}">
          </pf-field>
          <div class="editor-tabs">
            <button type="button" class="editor-tab-btn active" data-tab="source">${esc(T("texts.tab_source"))}</button>
            <button type="button" class="editor-tab-btn" id="tab-btn-transcription" data-tab="transcription">${esc(T("texts.tab_transcription"))}</button>
            <button type="button" class="editor-tab-btn" data-tab="translation">${esc(T("texts.tab_translation"))}</button>
          </div>
          <div class="editor-tab-pane" id="tab-source">
            <textarea id="field-source" placeholder="${esc(T("dialogs.field_body_hint"))}" required>${esc(existing?.body)}</textarea>
          </div>
          <div class="editor-tab-pane" id="tab-transcription" style="display:none;">
            <textarea id="field-transcription" placeholder="${esc(T("texts.field_transcription_hint"))}">${esc(existing?.transcription)}</textarea>
          </div>
          <div class="editor-tab-pane" id="tab-translation" style="display:none;">
            <textarea id="field-translation" placeholder="${esc(T("texts.field_translation_hint"))}">${esc(existing?.translation)}</textarea>
          </div>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("texts.save"))}</pf-button>
          </div>
        </form>
      </div>
    `;

    document.getElementById("backLink").addEventListener("click", (e) => {
      e.preventDefault();
      if (isEdit) showView(existing.id);
      else showList();
    });

    pfInitEditor();

    document.getElementById("dialogForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        title: document.getElementById("title").value,
        language: document.getElementById("language").value,
        script: document.getElementById("script").value,
        body: document.getElementById("field-source").value,
        transcription: document.getElementById("field-transcription").value,
        translation: document.getElementById("field-translation").value,
        tags: document.getElementById("tags").value,
      };
      try {
        const result = isEdit
          ? await apiFetch(`/api/v1/dialogs/${existing.id}`, { method: "PUT", body: JSON.stringify(payload) })
          : await apiFetch("/api/v1/dialogs", { method: "POST", body: JSON.stringify(payload) });
        showView(result.id);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }
  function showNew() {
    showForm();
  }

  // showIngest mirrors texts-app.js's showIngest exactly (same form shape,
  // same client-side FileReader handling and validation), posting to
  // /api/v1/dialogs/ingest instead. See texts-app.js for the inline
  // reasoning comments.
  function showIngest() {
    statusBar.setMessage("");
    let fileContent = null;
    let fileName = "";
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <h1>${esc(T("dialogs.ingest_title"))}</h1>
      <div class="form-card" style="max-width: none;">
        <form id="ingestForm">
          <pf-field label="${esc(T("texts.ingest_source_label"))}" for="ingest-source">
            <select id="ingest-source">
              <option value="text">${esc(T("texts.ingest_source_text"))}</option>
              <option value="file">${esc(T("texts.ingest_source_file"))}</option>
              <option value="url">${esc(T("texts.ingest_source_url"))}</option>
            </select>
          </pf-field>
          <div id="ingest-field-text">
            <textarea id="ingest-content" placeholder="${esc(T("texts.ingest_field_text"))}"></textarea>
          </div>
          <div id="ingest-field-file" style="display:none;">
            <pf-field label="${esc(T("texts.ingest_field_file"))}" for="ingest-file">
              <input type="file" id="ingest-file" accept=".md,.txt">
            </pf-field>
          </div>
          <div id="ingest-field-url" style="display:none;">
            <input type="url" id="ingest-url" placeholder="${esc(T("texts.ingest_field_url"))}">
          </div>
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="ingest-language">
              <select id="ingest-language" required>${languageOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("texts.field_script"))}" for="ingest-script">
              <select id="ingest-script" required>${scriptOptions()}</select>
            </pf-field>
          </div>
          <div class="form-actions">
            <pf-button variant="primary" type="submit" id="ingestSubmitBtn">${esc(T("texts.ingest_submit"))}</pf-button>
          </div>
        </form>
      </div>
    `;

    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(); });

    const sourceSelect = document.getElementById("ingest-source");
    const contentField = document.getElementById("ingest-content");
    const urlField = document.getElementById("ingest-url");
    const fileField = document.getElementById("ingest-file");
    const submitBtn = document.getElementById("ingestSubmitBtn");

    function updateSourceFields() {
      const source = sourceSelect.value;
      document.getElementById("ingest-field-text").style.display = source === "text" ? "" : "none";
      document.getElementById("ingest-field-file").style.display = source === "file" ? "" : "none";
      document.getElementById("ingest-field-url").style.display = source === "url" ? "" : "none";
      contentField.required = source === "text";
      fileField.required = source === "file";
      urlField.required = source === "url";
    }
    updateSourceFields();
    sourceSelect.addEventListener("change", updateSourceFields);

    fileField.addEventListener("change", () => {
      const file = fileField.files[0];
      fileContent = null;
      fileName = "";
      if (!file) return;
      submitBtn.setAttribute("disabled", "");
      const reader = new FileReader();
      reader.onload = () => {
        fileContent = reader.result;
        fileName = file.name;
        submitBtn.removeAttribute("disabled");
      };
      reader.onerror = () => {
        statusBar.setMessage(T("texts.err_ingest_file_read"), true);
        submitBtn.removeAttribute("disabled");
      };
      reader.readAsText(file);
    });

    document.getElementById("ingestForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const source = sourceSelect.value;
      const payload = {
        source,
        language: document.getElementById("ingest-language").value,
        script: document.getElementById("ingest-script").value,
      };
      if (source === "text") {
        payload.content = contentField.value;
      } else if (source === "file") {
        if (!fileContent) {
          statusBar.setMessage(T("texts.err_ingest_no_file"), true);
          return;
        }
        payload.content = fileContent;
        payload.filename = fileName;
      } else if (source === "url") {
        payload.url = urlField.value;
      }
      try {
        await apiFetch("/api/v1/dialogs/ingest", { method: "POST", body: JSON.stringify(payload) });
        statusBar.setMessage(T("texts.ingest_started"));
        showList();
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }

  // Renders either the real rendered HTML for a field, or the same inline
  // error notice dialogs-view.html used (never both) — a render error is
  // data on an otherwise-200 response, not a failed request.
  function renderedOrError(renderedHtml, errorText, extraClass) {
    if (errorText) {
      return `<p class="empty-state">${esc(T("dialogs.render_error_prefix"))} ${esc(errorText)}</p>`;
    }
    return `<article class="prose${extraClass || ""}">${renderedHtml || ""}</article>`;
  }

  // generateFromDialog mirrors texts-app.js's generateFromText — see that
  // function's doc comment (dialog-vocabulary-models-generation).
  async function generateFromDialog(id, endpoint, startedKey) {
    try {
      await apiFetch(`/api/v1/dialogs/${id}/${endpoint}`, { method: "POST" });
      statusBar.setMessage(T(startedKey));
    } catch (e) {
      statusBar.setMessage(e.message, true);
    }
  }

  // generateFieldFromDialog mirrors texts-app.js's generateFieldFromText —
  // see that function's doc comment (background-generate-title-
  // transcription-translation).
  async function generateFieldFromDialog(id, endpoint, startedKey, locale) {
    try {
      await apiFetch(`/api/v1/dialogs/${id}/${endpoint}`, {
        method: "POST",
        body: locale ? JSON.stringify({ locale }) : undefined,
      });
      statusBar.setMessage(T(startedKey));
    } catch (e) {
      statusBar.setMessage(e.message, true);
    }
  }

  async function showView(id) {
    statusBar.setMessage("");
    let dialog;
    try {
      dialog = await apiFetch(`/api/v1/dialogs/${id}`);
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    const tags = (dialog.tags || [])
      .map((t) => `<a class="badge tag-link" href="#" data-tag-filter="${esc(t)}">${esc(t)}</a>`)
      .join("");
    const hasTabs = !!dialog.transcription || dialog.hasTranslation;
    const linkedLists = [
      dialog.vocabularyListId != null ? `<a href="#" class="linked-list-link" data-goto-vocab="${dialog.vocabularyListId}">${esc(T("texts.linked_vocabulary"))}</a>` : "",
      dialog.modelsListId != null ? `<a href="#" class="linked-list-link" data-goto-models="${dialog.modelsListId}">${esc(T("texts.linked_models"))}</a>` : "",
    ].filter(Boolean).join("");
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <div>
          <h1 style="margin-bottom:0.25rem;">${esc(dialog.title)}</h1>
          <span class="badge">${esc(dialog.language)} / ${esc(dialog.script)}</span>
          ${tags}
        </div>
        <div class="resource-actions">
          <pf-button variant="secondary" type="button" id="copyBtn">Copy</pf-button>
          ${dialog.canEdit && !dialog.title ? `<pf-button variant="secondary" type="button" id="generateTitleBtn">${esc(T("texts.generate_title"))}</pf-button>` : ""}
          ${dialog.canEdit && dialog.needsTranscription && !dialog.transcription ? `<pf-button variant="secondary" type="button" id="generateTranscriptionBtn">${esc(T("texts.generate_transcription"))}</pf-button>` : ""}
          ${dialog.canEdit && !dialog.hasTranslation ? `<pf-button variant="secondary" type="button" id="generateTranslationBtn">${esc(T("texts.generate_translation"))}</pf-button>` : ""}
          ${dialog.canEdit ? `<pf-button variant="secondary" type="button" id="generateVocabBtn">${esc(T("texts.generate_vocabulary"))}</pf-button><pf-button variant="secondary" type="button" id="generateModelsBtn">${esc(T("texts.generate_models"))}</pf-button>` : ""}
          ${dialog.canEdit ? `<pf-button variant="primary" type="button" id="editBtn">${esc(T("texts.edit"))}</pf-button><pf-button variant="danger" type="button" id="deleteBtn">${esc(T("texts.delete"))}</pf-button>` : ""}
        </div>
      </div>
      <textarea id="copy-source-markdown" class="copy-source" readonly>${esc(dialog.sourceMarkdown)}</textarea>
      ${dialog.transcription ? `<textarea id="copy-transcription-markdown" class="copy-source" readonly>${esc(dialog.transcriptionMarkdown)}</textarea>` : ""}
      ${dialog.hasTranslation ? `<textarea id="copy-translation-markdown" class="copy-source" readonly>${esc(dialog.translationMarkdown)}</textarea>` : ""}
      ${
        hasTabs || linkedLists
          ? `<div class="editor-tabs-row">
              ${
                hasTabs
                  ? `<div class="editor-tabs">
                      <button type="button" class="editor-tab-btn active" data-tab="source">${esc(T("texts.tab_source"))}</button>
                      ${dialog.transcription ? `<button type="button" class="editor-tab-btn" data-tab="transcription">${esc(T("texts.tab_transcription"))}</button>` : ""}
                      ${dialog.hasTranslation ? `<button type="button" class="editor-tab-btn" data-tab="translation">${esc(T("texts.tab_translation"))}</button>` : ""}
                    </div>`
                  : ""
              }
              ${linkedLists ? `<div class="linked-lists">${linkedLists}</div>` : ""}
            </div>`
          : ""
      }
      <div class="editor-tab-pane" id="tab-source">
        ${renderedOrError(dialog.renderedBody, dialog.bodyError, dialog.scriptEnlarged ? " prose-enlarged" : "")}
      </div>
      ${
        dialog.transcription
          ? `<div class="editor-tab-pane" id="tab-transcription" style="display:none;">${renderedOrError(dialog.renderedTranscription, dialog.transcriptionError)}</div>`
          : ""
      }
      ${
        dialog.hasTranslation
          ? `<div class="editor-tab-pane" id="tab-translation" style="display:none;">${renderedOrError(dialog.renderedTranslation, dialog.translationError)}</div>`
          : ""
      }
    `;
    // Note: dialogs-view.html set dir="{{.Script.Direction}}" on the source
    // article; the rendered dialog markup itself already carries its own
    // dir attribute (see the real payload: dialog.s-latn dir="ltr"), so the
    // outer article doesn't need it duplicated here.
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(); });
    document.getElementById("copyBtn").addEventListener("click", (e) => {
      phraseforgeCopyActiveMarkdown(e.currentTarget.querySelector("button"));
    });
    document.querySelectorAll(".editor-tab-btn").forEach((btn) => {
      btn.addEventListener("click", () => pfSwitchTab(btn.dataset.tab));
    });
    const editBtn = document.getElementById("editBtn");
    if (editBtn) editBtn.addEventListener("click", () => showForm(dialog));
    const generateTitleBtn = document.getElementById("generateTitleBtn");
    if (generateTitleBtn) {
      generateTitleBtn.addEventListener("click", () => generateFieldFromDialog(id, "generate-title", "texts.generate_title_started"));
    }
    const generateTranscriptionBtn = document.getElementById("generateTranscriptionBtn");
    if (generateTranscriptionBtn) {
      generateTranscriptionBtn.addEventListener("click", () => generateFieldFromDialog(id, "generate-transcription", "texts.generate_transcription_started"));
    }
    const generateTranslationBtn = document.getElementById("generateTranslationBtn");
    if (generateTranslationBtn) {
      generateTranslationBtn.addEventListener("click", () => generateFieldFromDialog(id, "generate-translation", "texts.generate_translation_started", BOOT.locale));
    }
    const generateVocabBtn = document.getElementById("generateVocabBtn");
    if (generateVocabBtn) {
      generateVocabBtn.addEventListener("click", () => generateFromDialog(id, "generate-vocabulary", "texts.generate_vocabulary_started"));
    }
    const generateModelsBtn = document.getElementById("generateModelsBtn");
    if (generateModelsBtn) {
      generateModelsBtn.addEventListener("click", () => generateFromDialog(id, "generate-models", "texts.generate_models_started"));
    }
    const deleteBtn = document.getElementById("deleteBtn");
    if (deleteBtn) {
      deleteBtn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("dialogs.delete_confirm"), { danger: true });
        if (!ok) return;
        try {
          await apiFetch(`/api/v1/dialogs/${id}`, { method: "DELETE" });
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
    document.querySelectorAll("[data-goto-vocab]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        window.pfShowSection("vocabulary", { viewId: Number(a.dataset.gotoVocab) });
      });
    });
    document.querySelectorAll("[data-goto-models]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        window.pfShowSection("models", { viewId: Number(a.dataset.gotoModels) });
      });
    });
  }

  window.pfSections.dialogs = {
    setBootstrap(b) { BOOT = b; },
    show: showList,
  };
})();
