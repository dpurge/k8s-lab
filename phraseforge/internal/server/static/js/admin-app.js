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

  const TABS = ["grants", "ime", "llm", "config"];
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
  function providerOptions() {
    return (data.providers || []).map((p) => `<option value="${esc(p)}">${esc(p)}</option>`).join("");
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
      <div id="tabConfig"></div>
    `;
    const tabs = document.getElementById("adminTabs");
    tabs.items = [
      { id: "grants", label: T("admin.tab_grants") },
      { id: "ime", label: T("admin.tab_ime") },
      { id: "llm", label: T("admin.tab_llm") },
      { id: "config", label: T("admin.tab_config") },
    ];
    tabs.active = activeTab;
    tabs.addEventListener("pf-tabs-select", (e) => showTab(e.detail.id));

    renderGrantsTab();
    renderIMETab();
    renderLLMTab();
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
        <td><button class="link-btn" type="button" data-revoke-id="${g.id}">${esc(T("texts.delete"))}</button></td>
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
          <a href="#" data-edit-ime="${esc(key)}">${esc(T("texts.edit"))}</a>
          <button class="link-btn" type="button" data-delete-ime="${esc(key)}" style="margin-left:0.5rem;">${esc(T("texts.delete"))}</button>
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
            ${editingIme ? `<a href="#" id="imeCancelEdit" class="back-link">${esc(T("vocabulary.cancel_edit"))}</a>` : ""}
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
        <td>${p.kind === "translation" ? esc(p.target_language) : `<em>${esc(T("admin.llm_target_not_applicable"))}</em>`}</td>
        <td>${esc(p.provider)}</td>
        <td>${p.model ? esc(p.model) : `<em>${esc(T("admin.llm_model_default"))}</em>`}</td>
        <td>${p.think ? "✓" : "—"}</td>
        <td style="white-space:pre-wrap;">${esc(p.prompt)}</td>
        <td style="white-space:nowrap;">
          <a href="#" data-edit-llm="${esc(key)}">${esc(T("texts.edit"))}</a>
          <button class="link-btn" type="button" data-delete-llm="${esc(key)}" style="margin-left:0.5rem;">${esc(T("texts.delete"))}</button>
        </td>
      </tr>`;
        })
        .join("") || `<tr><td colspan="8"><em>${esc(T("admin.llm_no_configs"))}</em></td></tr>`;

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
          <div class="field-row">
            <pf-field label="${esc(T("admin.llm_provider"))}" for="llm-provider">
              <select id="llm-provider" required>${providerOptions()}</select>
            </pf-field>
            <pf-field label="${esc(T("admin.llm_model"))}" for="llm-model">
              <input type="text" id="llm-model" placeholder="${esc(T("admin.llm_model_placeholder"))}">
            </pf-field>
          </div>
          <label style="display:flex; align-items:center; gap:0.5rem; margin-bottom:1rem;">
            <input type="checkbox" id="llm-think" style="width:auto; margin:0;">
            ${esc(T("admin.llm_think"))}
          </label>
          <pf-field label="${esc(T("admin.llm_prompt"))}" for="llm-prompt">
            <textarea id="llm-prompt" required style="height:10rem;" placeholder="${esc(T("admin.llm_prompt_placeholder"))}"></textarea>
          </pf-field>
          <div class="form-actions">
            <pf-button variant="primary" type="submit">${esc(T("admin.llm_save"))}</pf-button>
            ${editingLlm ? `<a href="#" id="llmCancelEdit" class="back-link">${esc(T("vocabulary.cancel_edit"))}</a>` : ""}
          </div>
        </form>
      </div>
      <div class="card">
        <h2 style="margin-top:0;">${esc(T("admin.llm_configs_heading"))}</h2>
        <div style="overflow-x:auto;">
          <table class="admin-table">
            <thead><tr><th>${esc(T("admin.llm_kind"))}</th><th>${esc(T("admin.llm_source_language"))}</th><th>${esc(T("admin.llm_target_language"))}</th><th>${esc(T("admin.llm_provider"))}</th><th>${esc(T("admin.llm_model"))}</th><th>${esc(T("admin.llm_think"))}</th><th>${esc(T("admin.llm_prompt"))}</th><th></th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      </div>
    `;

    const kindSelect = document.getElementById("llm-kind");
    const targetField = document.getElementById("llm-target-field");
    const targetSelect = document.getElementById("llm-target-language");
    // pf-field's own CSS sets `display: block` unconditionally (an author
    // rule, which beats the UA [hidden] rule regardless of specificity), so
    // toggling the `hidden` attribute on it would silently do nothing —
    // must set inline style directly instead, same as admin.html's original
    // inline script did.
    // Every kind except "translation" has no real target language of its
    // own — its target_language is a fixed sentinel equal to the kind's own
    // name (see phraseforge/internal/ingest's transcriptionTargetLanguage
    // doc comment for the full rationale), so only "translation" shows/needs
    // this field.
    function refreshLLMTarget() {
      const isTranslation = kindSelect.value === "translation";
      targetField.style.display = isTranslation ? "" : "none";
      targetSelect.required = isTranslation;
    }
    kindSelect.addEventListener("change", refreshLLMTarget);

    // A saved row's provider is always explicit (unlike model, where empty
    // means "use the purpose default") — default a new row to "ollama"
    // since this bootstrap data doesn't expose the purpose's own default.
    const providerSelect = document.getElementById("llm-provider");
    providerSelect.value = editingPrompt ? editingPrompt.provider : "ollama";
    document.getElementById("llm-think").checked = editingPrompt ? !!editingPrompt.think : false;
    if (editingPrompt) {
      kindSelect.value = editingPrompt.kind;
      document.getElementById("llm-source-language").value = editingPrompt.source_language;
      document.getElementById("llm-model").value = editingPrompt.model || "";
      document.getElementById("llm-prompt").value = editingPrompt.prompt;
    }
    refreshLLMTarget();
    if (editingPrompt && editingPrompt.kind === "translation") {
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
      const payload = {
        kind,
        sourceLanguage: document.getElementById("llm-source-language").value,
        // Sentinel convention: every non-"translation" kind's target_language
        // is fixed to its own kind name (see refreshLLMTarget's comment).
        targetLanguage: kind === "translation" ? targetSelect.value : kind,
        provider: providerSelect.value,
        model: document.getElementById("llm-model").value,
        think: document.getElementById("llm-think").checked,
        prompt: document.getElementById("llm-prompt").value,
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
