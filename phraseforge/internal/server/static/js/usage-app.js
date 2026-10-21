// usage-app.js: client-side rendering for the Usage section of the unified SPA
// shell (app.html) — only present in the page when the current user is an
// admin, like the Tasks and Config sections. Its pages are listed in the
// sidebar: Usage (what each user used of each model), Models (the configured
// models and whether each can take calls), Performance (speed and success per
// model) and Attempts (every call, a page at a time, with the purge of old
// records). The tables are the shared dp-usage-table, dp-model-table and
// dp-attempt-table, the same as in the other apps.
//
// Own bootstrap/i18n comes from window.__PF_APP_BOOTSTRAP__.usage, like every
// section's. Registers itself on window.pfSections.usage for shell.js.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.usage;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("usage-app");
  const statusBar = document.getElementById("statusBar");

  const PAGES = ["usage", "models", "performance", "attempts"];
  const ATTEMPTS_PAGE_SIZE = 25;
  const OUTCOMES = ["ok", "error", "timeout", "rate_limited", "canceled"];
  const OUTCOME_KEYS = { ok: "admin.llm_outcome_ok", error: "admin.llm_outcome_error", timeout: "admin.llm_outcome_timeout", rate_limited: "admin.llm_outcome_rate_limited", canceled: "admin.llm_outcome_canceled" };
  const WINDOWS = [["1h", "admin.llm_window_1h"], ["24h", "admin.llm_window_24h"], ["7d", "admin.llm_window_7d"], ["30d", "admin.llm_window_30d"], ["all", "admin.llm_window_all"]];

  // What the reader chose outlives a page change, since every change draws the page again.
  const state = { window: "all", group: "", performanceWindow: "24h", performanceGroupBy: "model", modelId: "", outcome: "", purgeResult: "" };
  let request = 0; // a response for an earlier page or choice is ignored

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  }

  async function apiFetch(path, options) {
    const res = await fetch(path, options);
    if (!res.ok) {
      let message = res.statusText;
      try {
        const body = await res.json();
        if (body && body.error && body.error.message) message = body.error.message;
      } catch (_e) {
        // not JSON (a redirect to /login on an expired session): statusText it is
      }
      throw new Error(message);
    }
    return res.json();
  }

  // sinceFor is the RFC 3339 start of a period ending now; "all" is the epoch.
  function sinceFor(windowValue) {
    return customElements.get("pf-llm-performance").sinceFor(windowValue, new Date());
  }

  function select(id, label, options, value) {
    return `<label class="usage-filter"><span>${esc(label)}</span><select id="${id}">${options
      .map(([v, text]) => `<option value="${esc(v)}"${v === value ? " selected" : ""}>${esc(text)}</option>`)
      .join("")}</select></label>`;
  }

  function periodSelect(id, value) {
    return select(id, T("admin.llm_perf_period"), WINDOWS.map(([v, key]) => [v, T(key)]), value);
  }

  // sidebarNav is the list of this section's pages for the sidebar; shell.js
  // calls it on every route change.
  function sidebarNav(route) {
    const tabs = document.createElement("dp-tabs");
    tabs.setAttribute("orientation", "vertical");
    tabs.setAttribute("label", T("usage.title"));
    tabs.items = PAGES.map((id) => ({ id, label: T("usage.page_" + id) }));
    tabs.active = PAGES.includes(route.sub) ? route.sub : "usage";
    tabs.addEventListener("dp-tabs-select", (e) => window.pfRouter.navigate({ section: "usage", sub: e.detail.id === "usage" ? "" : e.detail.id }));
    return tabs;
  }

  // ── Usage ──────────────────────────────────────────────────────────────────

  async function showUsage() {
    root.innerHTML = `
      <h1>${esc(T("usage.page_usage"))}</h1>
      <div class="usage-controls">
        ${periodSelect("usagePeriod", state.window)}
        ${select("usageGroup", T("usage.group_by"), [["", T("usage.group_all")], ["user", T("usage.group_user")], ["provider", T("usage.group_provider")], ["model", T("usage.group_model")]], state.group)}
        <span class="muted" id="usageSummary"></span>
      </div>
      <div class="card"><dp-usage-table id="usageTable"></dp-usage-table></div>`;
    const table = document.getElementById("usageTable");
    table.emptyText = T("usage.empty");
    table.labels = { user: T("usage.col_user"), provider: T("admin.llm_col_provider"), model: T("admin.llm_col_model"), calls: T("usage.col_calls"), inputTokens: T("usage.col_input"), outputTokens: T("usage.col_output"), cost: T("usage.col_cost") };
    table.groupBy = state.group;
    document.getElementById("usagePeriod").addEventListener("change", (e) => {
      state.window = e.target.value;
      showUsage();
    });
    document.getElementById("usageGroup").addEventListener("change", (e) => {
      state.group = e.target.value;
      table.groupBy = state.group; // the rows are already here; the table sums them
    });
    const mine = ++request;
    try {
      const body = await apiFetch(`/api/v1/admin/llm-usage?since=${encodeURIComponent(sinceFor(state.window))}`);
      if (mine !== request) return;
      const items = body.items || [];
      table.rows = items.map((r) => ({ user: r.username, userId: r.user_id == null ? undefined : String(r.user_id), provider: r.provider_id, model: r.model_id, calls: r.calls, inputTokens: r.input_tokens, outputTokens: r.output_tokens, costUsd: r.cost_usd }));
      const calls = items.reduce((n, r) => n + r.calls, 0);
      const cost = items.reduce((n, r) => n + r.cost_usd, 0);
      const count = new Intl.NumberFormat(BOOT.locale);
      const dollars = new Intl.NumberFormat(BOOT.locale, { style: "currency", currency: "USD" });
      document.getElementById("usageSummary").textContent = items.length ? T("usage.summary").replace("%s", count.format(calls)).replace("%s", dollars.format(cost)) : "";
    } catch (e) {
      if (mine === request) statusBar.setMessage(e.message, true);
    }
  }

  // ── Models ─────────────────────────────────────────────────────────────────

  async function showModels() {
    root.innerHTML = `
      <div class="usage-heading"><h1>${esc(T("usage.page_models"))}</h1><dp-button variant="secondary" id="modelsRefresh">${esc(T("admin.llm_models_refresh"))}</dp-button></div>
      <div class="card"><dp-model-table id="modelsTable"></dp-model-table></div>`;
    const table = document.getElementById("modelsTable");
    table.emptyText = T("admin.llm_models_none");
    table.labels = { provider: T("admin.llm_col_provider"), model: T("admin.llm_col_model"), tags: T("admin.llm_model_tags"), priority: T("admin.llm_col_priority"), availability: T("admin.llm_col_availability"), available: T("admin.llm_available"), heldOut: T("admin.llm_reason_cooldown"), noCredentials: T("admin.llm_reason_no_credentials") };
    document.getElementById("modelsRefresh").addEventListener("click", showModels);
    const mine = ++request;
    try {
      const body = await apiFetch("/api/v1/admin/models");
      if (mine !== request) return;
      table.rows = modelRows(body.models);
    } catch (e) {
      if (mine === request) statusBar.setMessage(e.message, true);
    }
  }

  // modelRows turns the API's models into the table's rows, highest priority first.
  function modelRows(models) {
    return [...(models || [])]
      .sort((a, b) => b.priority - a.priority)
      .map((m) => ({
        provider: m.provider_id,
        model: m.id,
        tags: m.tags || [],
        priority: m.priority,
        state: m.available ? "available" : m.unavailable_reason === "missing_credentials" ? "no_credentials" : "held_out",
      }));
  }

  // ── Performance ────────────────────────────────────────────────────────────

  async function showPerformance() {
    root.innerHTML = `<div class="card"><pf-llm-performance id="performance"></pf-llm-performance></div>`;
    const performance = document.getElementById("performance");
    const outcome = (k) => T(OUTCOME_KEYS[k]);
    performance.labels = {
      outcomeOk: outcome("ok"), outcomeError: outcome("error"), outcomeTimeout: outcome("timeout"), outcomeRateLimited: outcome("rate_limited"), outcomeCanceled: outcome("canceled"),
      heading: T("admin.llm_perf_heading"), period: T("admin.llm_perf_period"), groupBy: T("admin.llm_perf_group"),
      hint: T("admin.llm_perf_hint"), none: T("admin.llm_perf_none"),
      window1h: T("admin.llm_window_1h"), window24h: T("admin.llm_window_24h"), window7d: T("admin.llm_window_7d"), window30d: T("admin.llm_window_30d"), windowAll: T("admin.llm_window_all"),
      groupModel: T("admin.llm_group_model"), groupKindModel: T("admin.llm_group_kind_model"),
      colModel: T("admin.llm_col_model"), colCalls: T("admin.llm_short_calls"), colSuccess: T("admin.llm_short_success"), colTime: T("admin.llm_short_time"),
      colTokens: T("admin.llm_short_tokens"), colTokensPerSecond: T("admin.llm_short_tokens_per_second"), colCost: T("admin.llm_short_cost"),
      tipCalls: T("admin.llm_col_calls"), tipSuccess: T("admin.llm_col_success"), tipTime: T("admin.llm_col_time_stats"),
      tipTokens: T("admin.llm_col_tokens"), tipTokensPerSecond: T("admin.llm_col_tokens_per_second"), tipCost: T("admin.llm_col_cost"),
    };
    performance.locale = BOOT.locale;
    performance.kindLabel = (kind) => T("admin.llm_kind_" + kind);
    performance.windowValue = state.performanceWindow;
    performance.groupBy = state.performanceGroupBy;
    const load = async () => {
      const mine = ++request;
      try {
        const body = await apiFetch(`/api/v1/admin/llm-aggregates?since=${encodeURIComponent(sinceFor(state.performanceWindow))}&group_by=${state.performanceGroupBy}`);
        if (mine === request) performance.items = body.items || [];
      } catch (e) {
        if (mine === request) statusBar.setMessage(e.message, true);
      }
    };
    performance.addEventListener("pf-llm-performance-change", (e) => {
      state.performanceWindow = e.detail.window;
      state.performanceGroupBy = e.detail.groupBy;
      load();
    });
    await load();
  }

  // ── Attempts ───────────────────────────────────────────────────────────────

  async function showAttempts(route) {
    const page = route.page;
    root.innerHTML = `
      <h1>${esc(T("usage.page_attempts"))}</h1>
      <div class="usage-controls">
        ${periodSelect("attemptsPeriod", state.window)}
        ${select("attemptsModel", T("admin.llm_col_model"), [["", T("admin.llm_attempts_filter_all")]], state.modelId)}
        ${select("attemptsOutcome", T("admin.llm_col_outcome"), [["", T("admin.llm_attempts_filter_all")], ...OUTCOMES.map((o) => [o, T(OUTCOME_KEYS[o])])], state.outcome)}
        <span class="muted" id="attemptsSummary"></span>
      </div>
      <div class="card"><dp-attempt-table id="attemptsTable"></dp-attempt-table></div>
      <dp-pager id="attemptsPager" style="margin-top:1.5rem;"></dp-pager>
      <div class="card" style="margin-top:1.5rem;"><pf-llm-purge id="purge"></pf-llm-purge></div>`;
    const table = document.getElementById("attemptsTable");
    table.emptyText = T("admin.llm_attempts_none");
    table.labels = { started: T("admin.llm_col_time"), user: T("usage.col_user"), provider: T("admin.llm_col_provider"), model: T("admin.llm_col_model"), outcome: T("admin.llm_col_outcome"), inputTokens: T("usage.col_input"), outputTokens: T("usage.col_output"), cost: T("usage.col_cost"), duration: T("usage.col_duration") };
    const pager = document.getElementById("attemptsPager");
    pager.setAttribute("prev-label", T("texts.pagination_previous"));
    pager.setAttribute("next-label", T("texts.pagination_next"));
    pager.addEventListener("dp-pager-select", (e) => window.pfRouter.navigate({ section: "usage", sub: "attempts", page: e.detail.page }));

    // A choice goes back to the first page, which the address names.
    const choose = (key) => (e) => {
      state[key] = e.target.value;
      window.pfRouter.navigate({ section: "usage", sub: "attempts" });
    };
    document.getElementById("attemptsPeriod").addEventListener("change", choose("window"));
    document.getElementById("attemptsModel").addEventListener("change", choose("modelId"));
    document.getElementById("attemptsOutcome").addEventListener("change", choose("outcome"));

    const purge = document.getElementById("purge");
    purge.labels = {
      heading: T("admin.llm_purge_heading"), label: T("admin.llm_purge_label"), option7: T("admin.llm_purge_option_7"),
      option30: T("admin.llm_purge_option_30"), option90: T("admin.llm_purge_option_90"), optionAll: T("admin.llm_purge_option_all"),
      button: T("admin.llm_purge_button"), confirmDays: T("admin.llm_purge_confirm_days"), confirmAll: T("admin.llm_purge_confirm_all"),
      confirmButton: T("admin.llm_purge_confirm_button"), cancel: T("admin.llm_purge_cancel"), hint: T("admin.llm_purge_hint"),
    };
    purge.result = state.purgeResult;
    purge.addEventListener("pf-llm-purge-confirm", async (e) => {
      try {
        const body = await apiFetch(`/api/v1/admin/llm-calls?older_than_days=${e.detail.olderThanDays}`, { method: "DELETE" });
        state.purgeResult = T("admin.llm_purge_done").replace("%s", new Intl.NumberFormat(BOOT.locale).format(body.deleted));
      } catch (err) {
        statusBar.setMessage(err.message, true);
        return;
      }
      window.pfRouter.navigate({ section: "usage", sub: "attempts" });
    });

    const mine = ++request;
    try {
      // The model filter's choices are the configured models and any model that
      // made an attempt, so a model that was removed from the configuration can still be picked.
      const [models, aggregates] = await Promise.all([
        apiFetch("/api/v1/admin/models"),
        apiFetch(`/api/v1/admin/llm-aggregates?since=${encodeURIComponent(sinceFor("all"))}`),
      ]);
      if (mine !== request) return;
      const ids = [...new Set([...(models.models || []).map((m) => m.id), ...(aggregates.items || []).map((a) => a.model_id)])].sort();
      const modelSelect = document.getElementById("attemptsModel");
      for (const id of ids) modelSelect.append(new Option(id, id, false, id === state.modelId));
      modelSelect.value = state.modelId;

      const query = new URLSearchParams({ limit: String(ATTEMPTS_PAGE_SIZE), offset: String((page - 1) * ATTEMPTS_PAGE_SIZE), since: sinceFor(state.window) });
      if (state.modelId) query.set("model_id", state.modelId);
      if (state.outcome) query.set("outcome", state.outcome);
      const body = await apiFetch(`/api/v1/admin/llm-calls?${query}`);
      if (mine !== request) return;
      const pageCount = Math.max(1, Math.ceil(body.total / ATTEMPTS_PAGE_SIZE));
      if (page > pageCount) {
        // The list shrank under the reader (a purge, a filter): the last page instead.
        window.pfRouter.replace({ section: "usage", sub: "attempts", page: pageCount });
        return;
      }
      pager.setAttribute("page", String(page));
      pager.setAttribute("page-count", String(pageCount));
      document.getElementById("attemptsSummary").textContent = body.total ? T("usage.attempts_total").replace("%s", new Intl.NumberFormat(BOOT.locale).format(body.total)) : "";
      table.rows = (body.items || []).map((a) => ({ startedAt: a.started_at, user: a.username || "—", userId: a.user_id == null ? undefined : String(a.user_id), provider: a.provider_id, model: a.model_id, outcome: a.outcome, error: a.error, inputTokens: a.input_tokens, outputTokens: a.output_tokens, costUsd: a.cost_usd, durationMs: a.duration_ms }));
    } catch (e) {
      if (mine === request) statusBar.setMessage(e.message, true);
    }
  }

  window.pfSections.usage = {
    setBootstrap(b) { BOOT = b; },
    sidebarNav,
    show(route) {
      statusBar.setMessage("");
      const sub = PAGES.includes(route.sub) ? route.sub : "usage";
      if (sub === "usage") return showUsage();
      if (sub === "models") return showModels();
      if (sub === "performance") return showPerformance();
      return showAttempts(route);
    },
  };
})();
