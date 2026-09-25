// texts-app.js: client-side rendering for the Texts section of the unified
// SPA shell (app.html). Single-URL model (no deep-linking — matches
// knowledge exactly, per the user's explicit decision in
// phraseforge-spa-shell-texts): list/new/view/edit are all in-page states
// inside #texts-app, never a URL change. Registers itself on
// window.pfSections.texts for shell.js to call into — see
// specs/features/phraseforge-spa-unified-shell.md.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.texts;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("texts-app");
  const statusBar = document.getElementById("statusBar");

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

  // buildExportURL appends the current language filter (if any) to an
  // export endpoint, mirroring the same ?language= convention showList's own
  // list-fetch query already applies — export always respects "whatever the
  // current filtered view would show" (see
  // specs/features/phraseforge-export-import.md).
  function buildExportURL(endpoint, languageFilter) {
    return languageFilter ? `${endpoint}?language=${encodeURIComponent(languageFilter)}` : endpoint;
  }

  // handleImportFile reads the chosen file client-side (FileReader), same
  // convention as showIngest's own file handling, then POSTs its raw text to
  // endpoint — the backend accepts a plain YAML/JSON body (see
  // export_import.go's readImportBody), no multipart upload needed. Reloads
  // the list first (so imported/updated/deleted rows show up immediately),
  // then shows the result summary — reversed order matters, since showList's
  // own statusBar.setMessage("") would otherwise wipe out the summary set
  // beforehand (same ordering issue jobs-app.js's retry handler documents).
  function handleImportFile(input, endpoint, tagFilter) {
    const file = input.files[0];
    input.value = ""; // allow re-selecting the same file next time
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

  // showImportResult mirrors jobs-app.js's View action's use of
  // #confirmDialog as a read-only details view: the {imported, deleted,
  // unchanged} counts always go to the status bar (matching every other
  // one-line outcome message in this file); per-item errors (index/id/
  // message), if any, go to the dialog instead, since a handful of error
  // lines don't fit that single-line surface.
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

  async function showList(tagFilter) {
    statusBar.setMessage("");
    // Read fresh on every call (not just at initial load) — same reasoning
    // as the fetch query below, but needed earlier here too since the
    // Export link's href is built from it.
    const languageFilter = window.pfGetLanguageFilter();
    root.innerHTML = `
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <h1 style="margin-bottom:0;">${esc(T("texts.title"))}</h1>
        ${
          BOOT.navFlags.canCreateAny
            ? `<div style="display:flex; gap:0.5rem; flex-wrap:wrap;">
                <pf-button variant="primary" id="newTextBtn">${esc(T("texts.new"))}</pf-button>
                <pf-button variant="secondary" id="ingestTextBtn">${esc(T("texts.ingest"))}</pf-button>
                <a class="btn" id="exportTextsLink" href="${esc(buildExportURL("/api/v1/texts/export", languageFilter))}">${esc(T("texts.export"))}</a>
                <pf-button variant="secondary" id="importTextsBtn">${esc(T("texts.import"))}</pf-button>
                <input type="file" id="importTextsFile" accept=".yaml,.yml" style="display:none;">
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
    const newBtn = document.getElementById("newTextBtn");
    if (newBtn) newBtn.addEventListener("click", () => showNew());
    const ingestBtn = document.getElementById("ingestTextBtn");
    if (ingestBtn) ingestBtn.addEventListener("click", () => showIngest());
    const importBtn = document.getElementById("importTextsBtn");
    const importFile = document.getElementById("importTextsFile");
    if (importBtn && importFile) {
      importBtn.addEventListener("click", () => importFile.click());
      importFile.addEventListener("change", () => handleImportFile(importFile, "/api/v1/texts/import", tagFilter));
    }
    const clearLink = document.getElementById("clearTagFilter");
    if (clearLink) clearLink.addEventListener("click", (e) => { e.preventDefault(); showList(); });

    const query = new URLSearchParams();
    if (languageFilter) query.set("language", languageFilter);
    if (tagFilter) query.set("tag", tagFilter);
    let items;
    try {
      const data = await apiFetch("/api/v1/texts?" + query.toString());
      items = data.items;
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }

    const container = document.getElementById("listContainer");
    if (items.length === 0) {
      const hint = BOOT.navFlags.canCreateAny ? "" : ` ${esc(T("texts.no_access"))}`;
      container.innerHTML = `<p class="empty-state" style="margin-top:1.5rem;">${esc(T("texts.empty"))}${hint}</p>`;
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

  // Shared by New and Edit — `existing` is undefined for New, or the full
  // apiTextDetail object for Edit (prefilled, same fields new.html/edit.html
  // both feed into the identical field set).
  function showForm(existing) {
    statusBar.setMessage("");
    const isEdit = !!existing;
    root.innerHTML = `
      ${isEdit ? `<a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>` : ""}
      <h1>${esc(T(isEdit ? "texts.edit_title" : "texts.new_title"))}</h1>
      <div class="form-card" style="max-width: none;">
        <form id="textForm">
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
            <textarea id="field-source" placeholder="${esc(T("texts.field_body"))}" required>${esc(existing?.body)}</textarea>
          </div>
          <div class="editor-tab-pane" id="tab-transcription" style="display:none;">
            <textarea id="field-transcription" placeholder="${esc(T("texts.field_transcription_hint"))}">${esc(existing?.transcription)}</textarea>
          </div>
          <div class="editor-tab-pane" id="tab-translation" style="display:none;">
            <textarea id="field-translation" placeholder="${esc(T("texts.field_translation_hint"))}">${esc(existing?.translation)}</textarea>
          </div>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("texts.save"))}</pf-button>
            <pf-button variant="secondary" class="transcribe-action" type="button" id="transcribeBtn">${esc(T("llm.transcribe"))}</pf-button>
            <pf-button variant="secondary" type="button" id="translateBtn">${esc(T("llm.translate"))}</pf-button>
            <select id="translation-target" aria-label="Translation target language">
              <option value="en">English</option>
              <option value="pl">Polish</option>
            </select>
          </div>
        </form>
      </div>
    `;

    const backLink = document.getElementById("backLink");
    if (backLink) backLink.addEventListener("click", (e) => { e.preventDefault(); showView(existing.id); });

    document.getElementById("transcribeBtn").addEventListener("click", (e) => {
      phraseforgeGenerate("transcription", "field-source", "field-transcription", "text", e.currentTarget.querySelector("button"));
    });
    document.getElementById("translateBtn").addEventListener("click", (e) => {
      phraseforgeGenerate("translation", "field-source", "field-translation", "text", e.currentTarget.querySelector("button"));
    });

    // Re-wires #language/#script change listeners and runs the initial
    // IME/script-styling/transcription-visibility pass against THIS
    // render's fresh DOM nodes — editor.js itself is unchanged; it was
    // always agnostic of how its elements got into the DOM, only ever
    // calling DOMContentLoaded once (a no-op the first time, since
    // #texts-app starts empty).
    pfInitEditor();

    document.getElementById("textForm").addEventListener("submit", async (e) => {
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
          ? await apiFetch(`/api/v1/texts/${existing.id}`, { method: "PUT", body: JSON.stringify(payload) })
          : await apiFetch("/api/v1/texts", { method: "POST", body: JSON.stringify(payload) });
        showView(result.id);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }
  function showNew() {
    showForm();
  }

  // showIngest renders the Ingest form: a source selector (paste text /
  // upload file / fetch URL) with its matching conditional field, plus the
  // same Language/Script selects the New form uses (languageOptions/
  // scriptOptions, unchanged). On submit it posts to /api/v1/texts/ingest
  // and returns to the list — there is no live progress to show a
  // non-admin user (see specs/features/phraseforge-ingest-texts-dialogs.md).
  function showIngest() {
    statusBar.setMessage("");
    let fileContent = null;
    let fileName = "";
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <h1>${esc(T("texts.ingest_title"))}</h1>
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

    // Only the field matching the selected source is shown, and only that
    // field is `required` — required's native browser validation blocks
    // the "submit" event from ever firing on a missing language/script or
    // (for the text/url sources) an empty/invalid field, matching the New
    // form's own reliance on `required` rather than hand-rolled messages.
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

    // A chosen file is read client-side (FileReader), not uploaded as
    // multipart — its text becomes the ingest request's `content`, and its
    // name becomes `filename`, matching apiIngestRequest's contract. The
    // submit button is disabled while a read is in flight so a fast click
    // can't race an incomplete FileReader result.
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
        await apiFetch("/api/v1/texts/ingest", { method: "POST", body: JSON.stringify(payload) });
        statusBar.setMessage(T("texts.ingest_started"));
        showList();
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }

  // generateFromText posts to one of the two generate-vocabulary/
  // generate-models endpoints (phraseforge-generate-vocab-models-from-text)
  // and shows a "started" acknowledgment via the status bar — no live
  // progress tracking, matching this app's established Jobs-page-is-
  // admin-only convention (same shape as showIngest's own submit handler).
  async function generateFromText(id, endpoint, startedKey) {
    try {
      await apiFetch(`/api/v1/texts/${id}/${endpoint}`, { method: "POST" });
      statusBar.setMessage(T(startedKey));
    } catch (e) {
      statusBar.setMessage(e.message, true);
    }
  }

  async function showView(id) {
    statusBar.setMessage("");
    let text;
    try {
      text = await apiFetch(`/api/v1/texts/${id}`);
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    const tags = (text.tags || [])
      .map((t) => `<a class="badge tag-link" href="#" data-tag-filter="${esc(t)}">${esc(t)}</a>`)
      .join("");
    const hasTabs = !!text.transcription || text.hasTranslation;
    root.innerHTML = `
      <a href="#" id="backLink" class="back-link">${esc(T("texts.back"))}</a>
      <div style="display:flex; align-items:center; justify-content:space-between; gap:1rem;">
        <div>
          <h1 style="margin-bottom:0.25rem;">${esc(text.title)}</h1>
          <span class="badge">${esc(text.language)} / ${esc(text.script)}</span>
          ${tags}
        </div>
        <div class="resource-actions">
          <pf-button variant="secondary" type="button" id="copyBtn">Copy</pf-button>
          ${text.canEdit ? `<pf-button variant="secondary" type="button" id="generateVocabBtn">${esc(T("texts.generate_vocabulary"))}</pf-button><pf-button variant="secondary" type="button" id="generateModelsBtn">${esc(T("texts.generate_models"))}</pf-button>` : ""}
          ${text.canEdit ? `<pf-button variant="primary" type="button" id="editBtn">${esc(T("texts.edit"))}</pf-button><pf-button variant="danger" type="button" id="deleteBtn">${esc(T("texts.delete"))}</pf-button>` : ""}
        </div>
      </div>
      <textarea id="copy-source-markdown" class="copy-source" readonly>${esc(text.sourceMarkdown)}</textarea>
      ${text.transcription ? `<textarea id="copy-transcription-markdown" class="copy-source" readonly>${esc(text.transcriptionMarkdown)}</textarea>` : ""}
      ${text.hasTranslation ? `<textarea id="copy-translation-markdown" class="copy-source" readonly>${esc(text.translationMarkdown)}</textarea>` : ""}
      ${
        hasTabs
          ? `<div class="editor-tabs">
              <button type="button" class="editor-tab-btn active" data-tab="source">${esc(T("texts.tab_source"))}</button>
              ${text.transcription ? `<button type="button" class="editor-tab-btn" data-tab="transcription">${esc(T("texts.tab_transcription"))}</button>` : ""}
              ${text.hasTranslation ? `<button type="button" class="editor-tab-btn" data-tab="translation">${esc(T("texts.tab_translation"))}</button>` : ""}
            </div>`
          : ""
      }
      <div class="editor-tab-pane" id="tab-source">
        <article class="prose${text.scriptEnlarged ? " prose-enlarged" : ""}" dir="${esc(text.scriptDirection)}">${text.renderedBody}</article>
      </div>
      ${
        text.transcription
          ? `<div class="editor-tab-pane" id="tab-transcription" style="display:none;"><article class="prose">${text.renderedTranscription}</article></div>`
          : ""
      }
      ${
        text.hasTranslation
          ? `<div class="editor-tab-pane" id="tab-translation" style="display:none;"><article class="prose">${text.renderedTranslation}</article></div>`
          : ""
      }
    `;
    document.getElementById("backLink").addEventListener("click", (e) => { e.preventDefault(); showList(); });
    document.getElementById("copyBtn").addEventListener("click", (e) => {
      phraseforgeCopyActiveMarkdown(e.currentTarget.querySelector("button"));
    });
    document.querySelectorAll(".editor-tab-btn").forEach((btn) => {
      btn.addEventListener("click", () => pfSwitchTab(btn.dataset.tab));
    });
    const editBtn = document.getElementById("editBtn");
    if (editBtn) editBtn.addEventListener("click", () => showForm(text));
    const generateVocabBtn = document.getElementById("generateVocabBtn");
    if (generateVocabBtn) {
      generateVocabBtn.addEventListener("click", () => generateFromText(id, "generate-vocabulary", "texts.generate_vocabulary_started"));
    }
    const generateModelsBtn = document.getElementById("generateModelsBtn");
    if (generateModelsBtn) {
      generateModelsBtn.addEventListener("click", () => generateFromText(id, "generate-models", "texts.generate_models_started"));
    }
    const deleteBtn = document.getElementById("deleteBtn");
    if (deleteBtn) {
      deleteBtn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("texts.delete_confirm"), { danger: true });
        if (!ok) return;
        try {
          await apiFetch(`/api/v1/texts/${id}`, { method: "DELETE" });
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

  window.pfSections.texts = {
    setBootstrap(b) { BOOT = b; },
    show: showList,
  };
})();
