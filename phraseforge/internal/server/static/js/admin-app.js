// admin-app.js: client-side rendering for the Admin section of the unified
// SPA shell (app.html) — only present in the page at all when the current
// user is an admin (see app.html/handleApp). Unlike the resource sections,
// Admin is a single-page dashboard, not a list-plus-items resource — it has
// no New/View/Edit navigation states. Instead it's split into four tabs
// (Permissions, IME, LLM, Config) via the pf-tabs component, replacing
// admin.html's old single scroll of five stacked cards. Every mutation
// re-fetches the whole aggregate (GET /api/v1/admin) and re-renders the
// current tab — this app's established "things can shift under you, always
// re-fetch" habit, generalized to independent tabs instead of item
// positions. Registers itself on window.pfSections.admin for shell.js to
// call into — see specs/features/phraseforge-spa-unified-shell.md.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.admin;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("admin-app");
  const statusBar = document.getElementById("statusBar");

  const TABS = ["grants", "ime", "llm", "languages", "config"];
  let activeTab = "grants";
  let data = null;
  // In-page "which row, if any, is being edited" state for IME/LLM — pure
  // client state, replacing what would otherwise need retyping a whole
  // (possibly long) LLM prompt from scratch to change one field. null = add
  // mode. Reset to null after every successful mutation (add/update/delete),
  // matching this app's existing editingPosition pattern (see
  // vocabulary-app.js/models-app.js).
  let editingIme = null; // "{language}|{script}" or null
  let editingLlm = null; // "{kind}|{sourceLanguage}|{targetLanguage}" or null
  let editingSnippet = null; // "{language}|{grammar|transcription}" or null

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

  function languageOptions() {
    return BOOT.languages.map((l) => `<option value="${esc(l.Code)}">${esc(l.Name)}</option>`).join("");
  }
  function scriptOptions() {
    return BOOT.scripts.map((s) => `<option value="${esc(s.Code)}">${esc(s.Name)}</option>`).join("");
  }
  function imePresetOptions() {
    return BOOT.imePresets.map((p) => `<option value="${esc(p.Code)}">${esc(p.Name)}</option>`).join("");
  }
  function siteLanguageOptions() {
    return BOOT.siteLanguages.map((l) => `<option value="${esc(l.Code)}">${esc(l.Name)}</option>`).join("");
  }
  // Kinds keyed by a real target locale; every other kind stores its own
  // name as a sentinel target_language (see refreshLLMTarget).
  const TARGETED_LLM_KINDS = ["translation", "vocabulary_item", "models_item"];
  function hasTargetLanguage(kind) {
    return TARGETED_LLM_KINDS.includes(kind);
  }
  // llmKindLabel/llmKindOptions derive the LLM-prompt kind <select> and its
  // row labels from the backend's own ai.ValidKinds list (data.llmKinds)
  // instead of a hardcoded copy here — the same list also backs the API's
  // own validation (server/admin.go), so the UI can never offer, or fail to
  // offer, a kind the backend actually accepts.
  function llmKindLabel(kind) {
    return T("admin.llm_kind_" + kind);
  }
  function llmKindOptions() {
    return (data.llmKinds || []).map((k) => `<option value="${esc(k)}">${esc(llmKindLabel(k))}</option>`).join("");
  }

  async function loadAndRender(tab) {
    statusBar.setMessage("");
    try {
      data = await apiFetch("/api/v1/admin");
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    render(tab || activeTab);
  }

  function render(tab) {
    activeTab = tab;
    root.innerHTML = `
      <h1>${esc(T("admin.title"))}</h1>
      <pf-tabs id="adminTabs" style="margin-bottom:1.5rem;"></pf-tabs>
      <div id="tabGrants"></div>
      <div id="tabIme"></div>
      <div id="tabLlm"></div>
      <div id="tabLanguages"></div>
      <div id="tabConfig"></div>
    `;
    const tabs = document.getElementById("adminTabs");
    tabs.items = [
      { id: "grants", label: T("admin.tab_grants") },
      { id: "ime", label: T("admin.tab_ime") },
      { id: "llm", label: T("admin.tab_llm") },
      { id: "languages", label: T("admin.tab_languages") },
      { id: "config", label: T("admin.tab_config") },
    ];
    tabs.active = activeTab;
    tabs.addEventListener("pf-tabs-select", (e) => {
      showTab(e.detail.id);
      if (e.detail.id === "llm") {
        usage.purgeResult = "";
        loadUsage();
      }
    });

    renderGrantsTab();
    renderIMETab();
    renderLLMTab();
    renderLanguagesTab();
    renderConfigTab();
    showTab(activeTab);
  }

  function showTab(id) {
    activeTab = id;
    document.getElementById("adminTabs").active = id;
    for (const t of TABS) {
      document.getElementById("tab" + t[0].toUpperCase() + t.slice(1)).style.display = t === id ? "" : "none";
    }
  }

  function renderGrantsTab() {
    const grants = data.grants || [];
    const users = data.users || [];
    const rows =
      grants
        .map(
          (g) => `
      <tr>
        <td>${esc(g.username)}</td>
        <td>${esc(T("role." + g.role))}</td>
        <td>${g.language ? `${esc(g.languageName)} (${esc(g.language)})` : `<em>${esc(T("admin.site_wide"))}</em>`}</td>
        <td><pf-button variant="danger" data-revoke-id="${g.id}">${esc(T("texts.delete"))}</pf-button></td>
      </tr>`,
        )
        .join("") || `<tr><td colspan="4"><em>${esc(T("admin.no_grants"))}</em></td></tr>`;

    document.getElementById("tabGrants").innerHTML = `
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.grant_heading"))}</h2>
        <form id="grantForm">
          <div class="field-row">
            <pf-field label="${esc(T("admin.user"))}" for="grant-user">
              <select id="grant-user" required>${users.map((u) => `<option value="${u.id}">${esc(u.username)}</option>`).join("")}</select>
            </pf-field>
            <pf-field label="${esc(T("admin.role"))}" for="grant-role">
              <select id="grant-role" required>
                <option value="admin">${esc(T("profile.role_admin"))}</option>
                <option value="teacher">${esc(T("role.teacher"))}</option>
                <option value="student">${esc(T("role.student"))}</option>
              </select>
            </pf-field>
            <pf-field label="${esc(T("admin.language_scoped"))}" for="grant-language">
              <select id="grant-language">${languageOptions()}</select>
            </pf-field>
          </div>
          <pf-button variant="primary" type="submit">${esc(T("admin.grant"))}</pf-button>
        </form>
      </div>
      <div class="card">
        <h2 style="margin-top:0;">${esc(T("admin.grants_heading"))}</h2>
        <div style="overflow-x:auto;">
          <table class="admin-table">
            <thead><tr><th>${esc(T("admin.user"))}</th><th>${esc(T("admin.role"))}</th><th>${esc(T("texts.field_language"))}</th><th></th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      </div>
    `;

    const roleSelect = document.getElementById("grant-role");
    const languageSelect = document.getElementById("grant-language");
    const refreshLanguageDisabled = () => {
      languageSelect.disabled = roleSelect.value === "admin";
    };
    roleSelect.addEventListener("change", refreshLanguageDisabled);
    refreshLanguageDisabled();

    document.getElementById("grantForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        userId: Number(document.getElementById("grant-user").value),
        role: roleSelect.value,
        language: languageSelect.disabled ? "" : languageSelect.value,
      };
      try {
        await apiFetch("/api/v1/admin/grants", { method: "POST", body: JSON.stringify(payload) });
        await loadAndRender("grants");
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    document.getElementById("tabGrants").querySelectorAll("[data-revoke-id]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("admin.revoke_confirm"), { danger: true });
        if (!ok) return;
        try {
          await apiFetch(`/api/v1/admin/grants/${btn.dataset.revokeId}`, { method: "DELETE" });
          await loadAndRender("grants");
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    });
  }

  function renderIMETab() {
    const configs = data.imeConfigs || [];
    const editingConfig = editingIme ? configs.find((c) => `${c.language}|${c.script}` === editingIme) : null;
    const rows =
      configs
        .map((c) => {
          const key = `${c.language}|${c.script}`;
          return `
      <tr>
        <td>${esc(c.language)}</td>
        <td>${esc(c.script)}</td>
        <td>${c.source_ime ? esc(c.source_ime) : `<em>${esc(T("admin.ime_none"))}</em>`}</td>
        <td>${c.transcription_ime ? esc(c.transcription_ime) : `<em>${esc(T("admin.ime_none"))}</em>`}</td>
        <td>${c.needs_transcription ? "✓" : "—"}</td>
        <td style="white-space:nowrap;">
          <pf-button data-edit-ime="${esc(key)}">${esc(T("texts.edit"))}</pf-button>
          <pf-button variant="danger" data-delete-ime="${esc(key)}">${esc(T("texts.delete"))}</pf-button>
        </td>
      </tr>`;
        })
        .join("") || `<tr><td colspan="6"><em>${esc(T("admin.ime_no_configs"))}</em></td></tr>`;

    document.getElementById("tabIme").innerHTML = `
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.ime_heading"))}</h2>
        <form id="imeForm">
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="ime-language">
              <select id="ime-language" required>${languageOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("texts.field_script"))}" for="ime-script">
              <select id="ime-script" required>${scriptOptions()}</select>
            </pf-field>
          </div>
          <div class="field-row">
            <pf-field label="${esc(T("admin.ime_source"))}" for="ime-source">
              <select id="ime-source">
                <option value="">${esc(T("admin.ime_none"))}</option>
                ${imePresetOptions()}
              </select>
            </pf-field>
            <pf-field label="${esc(T("admin.ime_transcription"))}" for="ime-transcription">
              <select id="ime-transcription">
                <option value="">${esc(T("admin.ime_none"))}</option>
                ${imePresetOptions()}
              </select>
            </pf-field>
          </div>
          <label style="display:flex; align-items:center; gap:0.5rem; margin-bottom:1rem;">
            <input type="checkbox" id="ime-needs-transcription" style="width:auto; margin:0;">
            ${esc(T("admin.ime_needs_transcription"))}
          </label>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("admin.ime_save"))}</pf-button>
            ${editingIme ? `<pf-button id="imeCancelEdit">${esc(T("vocabulary.cancel_edit"))}</pf-button>` : ""}
          </div>
        </form>
      </div>
      <div class="card">
        <h2 style="margin-top:0;">${esc(T("admin.ime_configs_heading"))}</h2>
        <div style="overflow-x:auto;">
          <table class="admin-table">
            <thead>
              <tr>
                <th>${esc(T("texts.field_language"))}</th><th>${esc(T("texts.field_script"))}</th>
                <th>${esc(T("admin.ime_source"))}</th><th>${esc(T("admin.ime_transcription"))}</th>
                <th>${esc(T("admin.ime_needs_transcription"))}</th><th></th>
              </tr>
            </thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      </div>
    `;

    if (editingConfig) {
      document.getElementById("ime-language").value = editingConfig.language;
      document.getElementById("ime-script").value = editingConfig.script;
      document.getElementById("ime-source").value = editingConfig.source_ime || "";
      document.getElementById("ime-transcription").value = editingConfig.transcription_ime || "";
      document.getElementById("ime-needs-transcription").checked = !!editingConfig.needs_transcription;
    }

    const cancelLink = document.getElementById("imeCancelEdit");
    if (cancelLink) {
      cancelLink.addEventListener("click", (e) => {
        e.preventDefault();
        editingIme = null;
        renderIMETab();
      });
    }

    document.getElementById("imeForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        language: document.getElementById("ime-language").value,
        script: document.getElementById("ime-script").value,
        sourceIme: document.getElementById("ime-source").value,
        transcriptionIme: document.getElementById("ime-transcription").value,
        needsTranscription: document.getElementById("ime-needs-transcription").checked,
      };
      try {
        await apiFetch("/api/v1/admin/ime", { method: "POST", body: JSON.stringify(payload) });
        editingIme = null;
        await loadAndRender("ime");
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    document.getElementById("tabIme").querySelectorAll("[data-edit-ime]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        editingIme = a.dataset.editIme;
        renderIMETab();
      });
    });

    document.getElementById("tabIme").querySelectorAll("[data-delete-ime]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("admin.ime_remove_confirm"), { danger: true });
        if (!ok) return;
        const [language, script] = btn.dataset.deleteIme.split("|");
        try {
          await apiFetch(`/api/v1/admin/ime/${encodeURIComponent(language)}/${encodeURIComponent(script)}`, { method: "DELETE" });
          editingIme = null;
          await loadAndRender("ime");
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    });
  }

  function renderLLMTab() {
    const prompts = data.llmPrompts || [];
    const editingPrompt = editingLlm
      ? prompts.find((p) => `${p.kind}|${p.source_language}|${p.target_language}` === editingLlm)
      : null;
    const rows =
      prompts
        .map((p) => {
          const key = `${p.kind}|${p.source_language}|${p.target_language}`;
          return `
      <tr>
        <td>${esc(llmKindLabel(p.kind))}</td>
        <td>${esc(p.source_language)}</td>
        <td>${hasTargetLanguage(p.kind) ? esc(p.target_language) : `<em>${esc(T("admin.llm_target_not_applicable"))}</em>`}</td>
        <td>${(p.model_tags || []).length ? esc(p.model_tags.join(", ")) : `<em>${esc(T("admin.llm_model_tags_default"))}</em>`}</td>
        <td>${p.think ? "✓" : "—"}</td>
        <td>${p.timeout_seconds ? esc(String(p.timeout_seconds)) : `<em>${esc(T("admin.llm_timeout_default"))}</em>`}</td>
        <td style="white-space:nowrap;">
          <pf-button data-edit-llm="${esc(key)}">${esc(T("texts.edit"))}</pf-button>
          <pf-button variant="danger" data-delete-llm="${esc(key)}">${esc(T("texts.delete"))}</pf-button>
        </td>
      </tr>`;
        })
        .join("") || `<tr><td colspan="7"><em>${esc(T("admin.llm_no_configs"))}</em></td></tr>`;

    document.getElementById("tabLlm").innerHTML = `
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.llm_heading"))}</h2>
        <form id="llmForm">
          <div class="field-row">
            <pf-field label="${esc(T("admin.llm_kind"))}" for="llm-kind">
              <select id="llm-kind" required>${llmKindOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("admin.llm_source_language"))}" for="llm-source-language">
              <select id="llm-source-language" required>${languageOptions()}</select>
            </pf-field>
            <pf-field id="llm-target-field" label="${esc(T("admin.llm_target_language"))}" for="llm-target-language">
              <select id="llm-target-language" required>${siteLanguageOptions()}</select>
            </pf-field>
          </div>
          <pf-field label="${esc(T("admin.llm_model_tags"))}">
            ${(data.tags || []).length
              ? `<pf-tag-picker id="llm-model-tags" aria-label="${esc(T("admin.llm_model_tags"))}"></pf-tag-picker>
            <p class="muted" id="llm-model-tags-hint" style="margin:0.4rem 0 1rem;"></p>`
              : `<p class="muted" style="margin:0 0 1rem;">${esc(T("admin.llm_model_tags_none"))}</p>`}
          </pf-field>
          <div class="field-row" style="margin-bottom:1rem; align-items:center;">
            <label style="display:flex; align-items:center; gap:0.5rem;">
              <input type="checkbox" id="llm-think" style="width:auto; margin:0;">
              ${esc(T("admin.llm_think"))}
            </label>
            <pf-field label="${esc(T("admin.llm_timeout_seconds"))}" for="llm-timeout-seconds">
              <input type="number" id="llm-timeout-seconds" min="1" max="3600" placeholder="${esc(T("admin.llm_timeout_placeholder"))}">
            </pf-field>
          </div>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("admin.llm_save"))}</pf-button>
            ${editingLlm ? `<pf-button id="llmCancelEdit">${esc(T("vocabulary.cancel_edit"))}</pf-button>` : ""}
          </div>
        </form>
      </div>
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.llm_prompt"))}</h2>
        <p class="muted" id="llm-placeholders-hint"></p>
        <textarea id="llm-prompt" readonly aria-label="${esc(T("admin.llm_prompt"))}" style="height:16rem;"></textarea>
      </div>
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.llm_configs_heading"))}</h2>
        <div style="overflow-x:auto;">
          <table class="admin-table">
            <thead><tr><th>${esc(T("admin.llm_kind"))}</th><th>${esc(T("admin.llm_source_language"))}</th><th>${esc(T("admin.llm_target_language"))}</th><th>${esc(T("admin.llm_model_tags"))}</th><th>${esc(T("admin.llm_think"))}</th><th>${esc(T("admin.llm_timeout_seconds"))}</th><th></th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      </div>
      <div id="llmUsage"></div>
    `;

    const kindSelect = document.getElementById("llm-kind");
    const targetField = document.getElementById("llm-target-field");
    const targetSelect = document.getElementById("llm-target-language");
    // pf-field's own CSS sets `display: block` unconditionally (an author
    // rule, which beats the UA [hidden] rule regardless of specificity), so
    // toggling the `hidden` attribute on it would silently do nothing —
    // must set inline style directly instead, same as admin.html's original
    // inline script did.
    // Every kind except TARGETED_LLM_KINDS has no real target language of its
    // own — its target_language is a fixed sentinel equal to the kind's own
    // name (see phraseforge/internal/ingest's transcriptionTargetLanguage
    // doc comment for the full rationale), so only those kinds show/need
    // this field.
    function refreshLLMTarget() {
      const isTargeted = hasTargetLanguage(kindSelect.value);
      targetField.style.display = isTargeted ? "" : "none";
      targetSelect.required = isTargeted;
    }
    kindSelect.addEventListener("change", refreshLLMTarget);

    // The prompt is shown read-only: a row stores only model tags, think
    // and timeout, the text is always the kind's default, and the language's
    // rules come from the Languages tab through the placeholders.
    const promptArea = document.getElementById("llm-prompt");
    function refreshLLMPrompt() {
      const kind = kindSelect.value;
      promptArea.value = (data.llmDefaults || {})[kind] || "";
      const names = ((data.llmPlaceholders || {})[kind] || []).map((n) => "{{" + n + "}}").join(", ");
      document.getElementById("llm-placeholders-hint").textContent = T("admin.llm_placeholders_hint").replace("%s", names);
    }
    kindSelect.addEventListener("change", refreshLLMPrompt);

    // No tag ticked means "inherit the purpose default", so the hint names the
    // tags that default is for the chosen kind.
    const tagPicker = document.getElementById("llm-model-tags");
    function refreshModelTagsHint() {
      if (!tagPicker) return;
      const defaults = ((data.llm_default_model_tags || {})[kindSelect.value] || []).join(", ");
      document.getElementById("llm-model-tags-hint").textContent = T("admin.llm_model_tags_hint").replace("%s", defaults);
    }
    if (tagPicker) {
      tagPicker.options = data.tags;
      tagPicker.value = editingPrompt ? editingPrompt.model_tags || [] : [];
      kindSelect.addEventListener("change", refreshModelTagsHint);
    }
    document.getElementById("llm-think").checked = editingPrompt ? !!editingPrompt.think : false;
    if (editingPrompt) {
      kindSelect.value = editingPrompt.kind;
      document.getElementById("llm-source-language").value = editingPrompt.source_language;
      document.getElementById("llm-timeout-seconds").value = editingPrompt.timeout_seconds || "";
    }
    refreshLLMTarget();
    refreshLLMPrompt();
    refreshModelTagsHint();
    if (editingPrompt && hasTargetLanguage(editingPrompt.kind)) {
      targetSelect.value = editingPrompt.target_language;
    }

    const cancelLink = document.getElementById("llmCancelEdit");
    if (cancelLink) {
      cancelLink.addEventListener("click", (e) => {
        e.preventDefault();
        editingLlm = null;
        renderLLMTab();
      });
    }

    document.getElementById("llmForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const kind = kindSelect.value;
      const timeoutSecondsRaw = document.getElementById("llm-timeout-seconds").value.trim();
      const payload = {
        kind,
        sourceLanguage: document.getElementById("llm-source-language").value,
        // Sentinel convention: every non-targeted kind's target_language
        // is fixed to its own kind name (see refreshLLMTarget's comment).
        targetLanguage: hasTargetLanguage(kind) ? targetSelect.value : kind,
        // Empty means "inherit the purpose default" (the API reads omitted and
        // empty alike).
        model_tags: tagPicker ? tagPicker.value : [],
        think: document.getElementById("llm-think").checked,
        // Blank means "inherit the purpose default" (llm-purpose-timeout-
        // and-prompt-config) — omitted entirely rather than sent as 0/null,
        // matching apiAdminLLMPromptRequest.TimeoutSeconds's *int, nil-means-
        // absent contract.
        ...(timeoutSecondsRaw ? { timeoutSeconds: Number(timeoutSecondsRaw) } : {}),
      };
      try {
        await apiFetch("/api/v1/admin/llm-prompts", { method: "POST", body: JSON.stringify(payload) });
        editingLlm = null;
        await loadAndRender("llm");
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    document.getElementById("tabLlm").querySelectorAll("[data-edit-llm]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        editingLlm = a.dataset.editLlm;
        renderLLMTab();
        document.getElementById("llmForm").scrollIntoView({ behavior: "smooth", block: "start" });
        const firstTag = document.querySelector("#llm-model-tags input");
        if (firstTag) firstTag.focus({ preventScroll: true });
      });
    });

    document.getElementById("tabLlm").querySelectorAll("[data-delete-llm]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("admin.llm_delete_confirm"), { danger: true });
        if (!ok) return;
        const [kind, sourceLanguage, targetLanguage] = btn.dataset.deleteLlm.split("|");
        try {
          await apiFetch(
            `/api/v1/admin/llm-prompts/${encodeURIComponent(kind)}/${encodeURIComponent(sourceLanguage)}/${encodeURIComponent(targetLanguage)}`,
            { method: "DELETE" },
          );
          editingLlm = null;
          await loadAndRender("llm");
        } catch (e) {
          statusBar.setMessage(e.message, true);
        }
      });
    });

    renderLLMUsage();
  }

  // The LLM tab's usage sections: the configured models, the performance of
  // the attempts in a period, the latest attempts, and the purge of old attempt
  // records. Their state outlives renderLLMTab (every prompt save re-renders
  // the tab), and the numbers are fetched when the tab is opened, not on every
  // re-render.
  const ATTEMPTS_LIMIT = 50;
  const usage = { window: "24h", groupBy: "model", modelId: "", outcome: "", aggregates: [], attempts: [], purgeResult: "" };
  let aggregatesRequest = 0;
  let attemptsRequest = 0;

  function usageElement(id) {
    return document.getElementById(id);
  }

  function renderLLMUsage() {
    document.getElementById("llmUsage").innerHTML = `
      <div class="card" style="margin-bottom:1.5rem;"><pf-llm-models id="llmModels"></pf-llm-models></div>
      <div class="card" style="margin-bottom:1.5rem;"><pf-llm-performance id="llmPerformance"></pf-llm-performance></div>
      <div class="card" style="margin-bottom:1.5rem;"><pf-llm-attempts id="llmAttempts"></pf-llm-attempts></div>
      <div class="card" style="margin-bottom:1.5rem;"><pf-llm-purge id="llmPurge"></pf-llm-purge></div>
    `;
    const models = usageElement("llmModels");
    models.labels = {
      heading: T("admin.llm_models_heading"), refresh: T("admin.llm_models_refresh"), colModel: T("admin.llm_col_model"),
      colProvider: T("admin.llm_col_provider"), colTags: T("admin.llm_model_tags"), colPriority: T("admin.llm_col_priority"),
      colAvailability: T("admin.llm_col_availability"), available: T("admin.llm_available"), unavailable: T("admin.llm_unavailable"),
      reasons: { cooldown: T("admin.llm_reason_cooldown"), missing_credentials: T("admin.llm_reason_no_credentials") },
      none: T("admin.llm_models_none"),
    };
    models.locale = BOOT.locale;
    models.addEventListener("pf-llm-models-refresh", refreshModels);

    const outcomeLabels = {
      outcomeOk: T("admin.llm_outcome_ok"), outcomeError: T("admin.llm_outcome_error"), outcomeTimeout: T("admin.llm_outcome_timeout"),
      outcomeRateLimited: T("admin.llm_outcome_rate_limited"), outcomeCanceled: T("admin.llm_outcome_canceled"),
    };
    const performance = usageElement("llmPerformance");
    performance.labels = {
      ...outcomeLabels,
      heading: T("admin.llm_perf_heading"), period: T("admin.llm_perf_period"), groupBy: T("admin.llm_perf_group"),
      hint: T("admin.llm_perf_hint"), none: T("admin.llm_perf_none"),
      window1h: T("admin.llm_window_1h"), window24h: T("admin.llm_window_24h"), window7d: T("admin.llm_window_7d"),
      window30d: T("admin.llm_window_30d"), windowAll: T("admin.llm_window_all"),
      groupModel: T("admin.llm_group_model"), groupKindModel: T("admin.llm_group_kind_model"),
      colModel: T("admin.llm_col_model"), colCalls: T("admin.llm_short_calls"), colSuccess: T("admin.llm_short_success"),
      colTime: T("admin.llm_short_time"), colTokens: T("admin.llm_short_tokens"),
      colTokensPerSecond: T("admin.llm_short_tokens_per_second"), colCost: T("admin.llm_short_cost"),
      tipCalls: T("admin.llm_col_calls"), tipSuccess: T("admin.llm_col_success"), tipTime: T("admin.llm_col_time_stats"),
      tipTokens: T("admin.llm_col_tokens"), tipTokensPerSecond: T("admin.llm_col_tokens_per_second"), tipCost: T("admin.llm_col_cost"),
    };
    performance.locale = BOOT.locale;
    performance.kindLabel = llmKindLabel;
    performance.windowValue = usage.window;
    performance.groupBy = usage.groupBy;
    performance.addEventListener("pf-llm-performance-change", (e) => {
      usage.window = e.detail.window;
      usage.groupBy = e.detail.groupBy;
      loadAggregates();
    });

    const attempts = usageElement("llmAttempts");
    attempts.labels = {
      ...outcomeLabels,
      heading: T("admin.llm_attempts_heading"), filterModel: T("admin.llm_col_model"), filterOutcome: T("admin.llm_col_outcome"),
      filterAll: T("admin.llm_attempts_filter_all"), none: T("admin.llm_attempts_none"),
      colTime: T("admin.llm_col_time"), colPurpose: T("admin.llm_col_purpose"), colModel: T("admin.llm_col_model"),
      colOutcome: T("admin.llm_col_outcome"), colDuration: T("admin.llm_col_duration"), colTokens: T("admin.llm_short_tokens"),
      colHttp: T("admin.llm_col_http"), request: T("admin.llm_attempt_request"),
      tipTokens: T("admin.llm_col_tokens"),
    };
    attempts.locale = BOOT.locale;
    attempts.kindLabel = llmKindLabel;
    attempts.addEventListener("pf-llm-attempts-filter", (e) => {
      usage.modelId = e.detail.modelId;
      usage.outcome = e.detail.outcome;
      loadAttempts();
    });

    const purge = usageElement("llmPurge");
    purge.labels = {
      heading: T("admin.llm_purge_heading"), label: T("admin.llm_purge_label"), option7: T("admin.llm_purge_option_7"),
      option30: T("admin.llm_purge_option_30"), option90: T("admin.llm_purge_option_90"), optionAll: T("admin.llm_purge_option_all"),
      button: T("admin.llm_purge_button"), confirmDays: T("admin.llm_purge_confirm_days"), confirmAll: T("admin.llm_purge_confirm_all"),
      confirmButton: T("admin.llm_purge_confirm_button"), cancel: T("admin.llm_purge_cancel"), hint: T("admin.llm_purge_hint"),
    };
    purge.addEventListener("pf-llm-purge-confirm", (e) => purgeAttempts(e.detail.olderThanDays));

    applyUsage();
  }

  // applyUsage hands the current state to whichever usage elements are on the
  // page; a response that arrives after the tab was re-rendered still lands.
  function applyUsage() {
    const models = usageElement("llmModels");
    if (!models) return;
    models.models = data.models || [];
    usageElement("llmPerformance").items = usage.aggregates;
    const attempts = usageElement("llmAttempts");
    const modelIds = new Set([...(data.models || []).map((m) => m.id), ...usage.aggregates.map((a) => a.model_id)]);
    attempts.modelIds = [...modelIds].sort();
    attempts.items = usage.attempts;
    usageElement("llmPurge").result = usage.purgeResult;
  }

  async function loadAggregates() {
    const request = ++aggregatesRequest;
    const since = customElements.get("pf-llm-performance").sinceFor(usage.window, new Date());
    try {
      const body = await apiFetch(`/api/v1/admin/llm-aggregates?since=${encodeURIComponent(since)}&group_by=${usage.groupBy}`);
      if (request !== aggregatesRequest) return;
      usage.aggregates = body.items || [];
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    applyUsage();
  }

  async function loadAttempts() {
    const request = ++attemptsRequest;
    const query = new URLSearchParams({ limit: String(ATTEMPTS_LIMIT) });
    if (usage.modelId) query.set("model_id", usage.modelId);
    if (usage.outcome) query.set("outcome", usage.outcome);
    try {
      const body = await apiFetch(`/api/v1/admin/llm-calls?${query}`);
      if (request !== attemptsRequest) return;
      usage.attempts = body.items || [];
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    applyUsage();
  }

  async function refreshModels() {
    try {
      const body = await apiFetch("/api/v1/admin/models");
      data.models = body.models || [];
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    applyUsage();
  }

  function loadUsage() {
    return Promise.all([loadAggregates(), loadAttempts()]);
  }

  async function purgeAttempts(olderThanDays) {
    try {
      const body = await apiFetch(`/api/v1/admin/llm-calls?older_than_days=${olderThanDays}`, { method: "DELETE" });
      usage.purgeResult = T("admin.llm_purge_done").replace("%s", new Intl.NumberFormat(BOOT.locale).format(body.deleted));
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }
    await Promise.all([loadUsage(), refreshModels()]);
  }

  function languageName(code) {
    const l = BOOT.languages.find((x) => x.Code === code);
    return l ? l.Name : code;
  }

  // The Languages tab: one table row per (language, snippet). A snippet is the
  // text a prompt receives where it has {{grammarPrompt}},
  // {{transcriptionPrompt}} or {{dictionaryPrompt}}; it is stored per language in
  // language_llm_sections (one row holding all), so saving or deleting one
  // snippet re-sends the others unchanged.
  const SNIPPET_FIELDS = [
    { id: "grammar", prop: "grammar_prompt", payload: "grammarPrompt", label: "admin.sections_grammar" },
    { id: "transcription", prop: "transcription_prompt", payload: "transcriptionPrompt", label: "admin.sections_transcription" },
    { id: "dictionary", prop: "dictionary_prompt", payload: "dictionaryPrompt", label: "admin.sections_dictionary" },
  ];

  function snippetRows() {
    return (data.languageSections || []).flatMap((ls) =>
      SNIPPET_FIELDS.filter((f) => (ls[f.prop] || "").trim() !== "").map((f) => ({ language: ls.language, field: f, text: ls[f.prop] })),
    );
  }

  // saveSnippet stores text as language's snippet of the given field, keeping
  // the language's other snippets; when both end up blank the language's row is
  // deleted instead.
  async function saveSnippet(language, field, text) {
    const current = (data.languageSections || []).find((ls) => ls.language === language) || {};
    const payload = { language };
    for (const f of SNIPPET_FIELDS) payload[f.payload] = f === field ? text : current[f.prop] || "";
    if (SNIPPET_FIELDS.every((f) => payload[f.payload].trim() === "")) {
      await apiFetch(`/api/v1/admin/language-sections/${encodeURIComponent(language)}`, { method: "DELETE" });
    } else {
      await apiFetch("/api/v1/admin/language-sections", { method: "POST", body: JSON.stringify(payload) });
    }
  }

  function renderLanguagesTab() {
    const tiles =
      snippetRows()
        .map((r) => {
          const key = `${r.language}|${r.field.id}`;
          return `
      <pf-card>
        <h3 class="snippet-title">${esc(languageName(r.language))} · ${esc(T(r.field.label))}</h3>
        <pre class="snippet-text">${esc(r.text)}</pre>
        <div class="form-actions">
          <pf-button data-edit-snippet="${esc(key)}">${esc(T("texts.edit"))}</pf-button>
          <pf-button variant="danger" data-delete-snippet="${esc(key)}">${esc(T("texts.delete"))}</pf-button>
        </div>
      </pf-card>`;
        })
        .join("") || `<p><em>${esc(T("admin.sections_no_configs"))}</em></p>`;
    document.getElementById("tabLanguages").innerHTML = `
      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("admin.sections_heading"))}</h2>
        <p class="muted">${esc(T("admin.sections_hint"))}</p>
        <form id="snippetForm">
          <div class="field-row">
            <pf-field label="${esc(T("texts.field_language"))}" for="snippet-language">
              <select id="snippet-language" required>${languageOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("admin.snippet_field"))}" for="snippet-field">
              <select id="snippet-field" required>${SNIPPET_FIELDS.map((f) => `<option value="${f.id}">${esc(T(f.label))}</option>`).join("")}</select>
            </pf-field>
          </div>
          <pf-field label="${esc(T("admin.snippet_text"))}" for="snippet-text">
            <textarea id="snippet-text" required style="height:14rem;"></textarea>
          </pf-field>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("admin.sections_save"))}</pf-button>
            ${editingSnippet ? `<pf-button id="snippetCancelEdit">${esc(T("vocabulary.cancel_edit"))}</pf-button>` : ""}
          </div>
        </form>
      </div>
      <h2>${esc(T("admin.sections_configs_heading"))}</h2>
      <div class="snippet-list">${tiles}</div>`;
    bindLanguagesTab();
  }

  function bindLanguagesTab() {
    const languageSelect = document.getElementById("snippet-language");
    const fieldSelect = document.getElementById("snippet-field");
    const textArea = document.getElementById("snippet-text");
    const fieldOf = (id) => SNIPPET_FIELDS.find((f) => f.id === id);

    // The box shows the language's own snippet; a language without one starts
    // from the default snippet. Changing language or snippet swaps the text
    // only while the box is untouched (it still holds what was put there).
    let shown = null;
    function refreshSnippetText() {
      const field = fieldOf(fieldSelect.value);
      const section = (data.languageSections || []).find((ls) => ls.language === languageSelect.value) || {};
      const next = (section[field.prop] || "").trim() !== "" ? section[field.prop] : (data.snippetDefaults || {})[field.id] || "";
      if (textArea.value === "" || textArea.value === shown) {
        textArea.value = next;
        shown = next;
      }
    }
    if (editingSnippet) {
      const [language, fieldId] = editingSnippet.split("|");
      languageSelect.value = language;
      fieldSelect.value = fieldId;
    }
    refreshSnippetText();
    languageSelect.addEventListener("change", refreshSnippetText);
    fieldSelect.addEventListener("change", refreshSnippetText);

    const cancelLink = document.getElementById("snippetCancelEdit");
    if (cancelLink) {
      cancelLink.addEventListener("click", (e) => {
        e.preventDefault();
        editingSnippet = null;
        renderLanguagesTab();
      });
    }
    document.getElementById("snippetForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      try {
        await saveSnippet(languageSelect.value, fieldOf(fieldSelect.value), textArea.value);
        editingSnippet = null;
        await loadAndRender("languages");
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
    const tab = document.getElementById("tabLanguages");
    tab.querySelectorAll("[data-edit-snippet]").forEach((a) => {
      a.addEventListener("click", (e) => {
        e.preventDefault();
        editingSnippet = a.dataset.editSnippet;
        renderLanguagesTab();
        // The form is above the list: bring it into view and put the cursor in
        // the text, so it is clear which snippet is being edited.
        document.getElementById("snippetForm").scrollIntoView({ behavior: "smooth", block: "start" });
        document.getElementById("snippet-text").focus({ preventScroll: true });
      });
    });
    tab.querySelectorAll("[data-delete-snippet]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const confirmDialog = document.getElementById("confirmDialog");
        const ok = await confirmDialog.confirm(T("admin.sections_delete_confirm"), { danger: true });
        if (!ok) return;
        const [language, fieldId] = btn.dataset.deleteSnippet.split("|");
        try {
          await saveSnippet(language, fieldOf(fieldId), "");
          editingSnippet = null;
          await loadAndRender("languages");
        } catch (err) {
          statusBar.setMessage(err.message, true);
        }
      });
    });
  }

  function renderConfigTab() {
    document.getElementById("tabConfig").innerHTML = `
      <div class="card">
        <h2 style="margin-top:0;">${esc(T("admin.config_heading"))}</h2>
        <p class="muted">${esc(T("admin.config_import_hint"))}</p>
        <div class="form-actions" style="align-items:center; gap:0.75rem; flex-wrap:wrap;">
          <a class="btn" href="/admin/config/export">${esc(T("admin.config_export"))}</a>
          <form id="importForm" style="display:flex; align-items:center; gap:0.75rem; margin:0;">
            <label class="field-label" for="admin-config-file" style="margin:0;">${esc(T("admin.config_import_file"))}</label>
            <input type="file" id="admin-config-file" accept="application/json,.json" required style="margin:0; width:auto;">
            <pf-button variant="primary" type="submit">${esc(T("admin.config_import"))}</pf-button>
          </form>
        </div>
      </div>
    `;

    document.getElementById("importForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const confirmDialog = document.getElementById("confirmDialog");
      const ok = await confirmDialog.confirm(T("admin.config_import_confirm"), { danger: true });
      if (!ok) return;
      const fileInput = document.getElementById("admin-config-file");
      const file = fileInput.files[0];
      if (!file) return;
      const formData = new FormData();
      formData.append("config_file", file);
      try {
        const res = await fetch("/api/v1/admin/config/import", { method: "POST", body: formData });
        if (!res.ok) {
          let message = res.statusText;
          try {
            const body = await res.json();
            if (body && body.error && body.error.message) message = body.error.message;
          } catch (_e) {
            // not JSON — fall through to statusText
          }
          throw new Error(message);
        }
        statusBar.setMessage("Configuration imported.");
        await loadAndRender("config");
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }

  window.pfSections.admin = {
    setBootstrap(b) { BOOT = b; },
    show() { loadAndRender("grants"); },
  };
})();
