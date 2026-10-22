// The model pages: Models (what is configured and whether it can take calls),
// Usage (what each kind of call used of each model) and Attempts (every call, a
// page at a time). The data is /api/v1/admin/models, /llm-aggregates and
// /llm-calls; the tables are the shared dp-model-table, dp-usage-table and
// dp-attempt-table.
//
// Knowledge has no users, so the tables' user column is relabelled "Kind" and
// shows the kind of call (chat, embed, ...). The data holds only configured ids,
// numbers and outcomes: no prompt, reply, text, address or key.
//
// A classic script after app.js, which gives it api(), message() and $(). The
// pure parts (kbLlm) are also exported for node's test runner.
const kbLlm = (() => {
  const PERIODS = [
    ["24h", "Last 24 hours"],
    ["7d", "Last 7 days"],
    ["30d", "Last 30 days"],
    ["all", "All time"],
  ];
  const OUTCOMES = ["ok", "error", "timeout", "rate_limited", "canceled"];
  const KINDS = ["chat", "generate_title", "generate_summary", "translate", "embed"];
  const ATTEMPTS_PAGE_SIZE = 25;
  const HOURS = { "24h": 24, "7d": 24 * 7, "30d": 24 * 30 };

  // sinceFor is the RFC 3339 start of a period ending at now; "all" is the epoch.
  function sinceFor(period, now = new Date()) {
    if (!(period in HOURS)) return "1970-01-01T00:00:00Z";
    return new Date(now.getTime() - HOURS[period] * 3600 * 1000).toISOString();
  }

  // modelRows are the rows of dp-model-table. An embedding model also shows the
  // vector space it produces as a tag.
  function modelRows(models) {
    return (models || []).map((m) => ({
      provider: m.provider_id,
      model: m.id,
      tags: m.embedding_space ? [...(m.tags || []), `space ${m.embedding_space}`] : m.tags || [],
      priority: m.priority,
      state: m.available ? "available" : m.unavailable_reason === "missing_credentials" ? "no_credentials" : "held_out",
    }));
  }

  // usageRows are the rows of dp-usage-table, one per kind, provider and model.
  // Only successful calls count, as in the other apps; a row with none is left out.
  function usageRows(aggregates) {
    return (aggregates || [])
      .filter((a) => a.ok_calls > 0)
      .map((a) => ({
        user: a.kind,
        provider: a.provider_id,
        model: a.model_id,
        calls: a.ok_calls,
        inputTokens: a.input_tokens,
        outputTokens: a.output_tokens,
        costUsd: a.cost_usd,
      }));
  }

  // attemptRows are the rows of dp-attempt-table.
  function attemptRows(items) {
    return (items || []).map((a) => ({
      startedAt: a.started_at,
      user: a.kind,
      provider: a.provider_id,
      model: a.model_id,
      outcome: a.outcome,
      error: a.error,
      inputTokens: a.input_tokens,
      outputTokens: a.output_tokens,
      costUsd: a.cost_usd,
      durationMs: a.duration_ms,
    }));
  }

  // attemptsQuery is the query string of one page of attempts.
  function attemptsQuery(state, page, now = new Date()) {
    const query = new URLSearchParams({
      limit: String(ATTEMPTS_PAGE_SIZE),
      offset: String((Math.max(1, page) - 1) * ATTEMPTS_PAGE_SIZE),
      since: sinceFor(state.period, now),
    });
    if (state.model) query.set("model_id", state.model);
    if (state.kind) query.set("kind", state.kind);
    if (state.outcome) query.set("outcome", state.outcome);
    return query.toString();
  }

  function pageCount(total) {
    return Math.max(1, Math.ceil(total / ATTEMPTS_PAGE_SIZE));
  }

  function options(pairs, selected) {
    return pairs.map(([value, label]) => `<option value="${value}"${value === selected ? " selected" : ""}>${label}</option>`).join("");
  }

  return { PERIODS, OUTCOMES, KINDS, ATTEMPTS_PAGE_SIZE, sinceFor, modelRows, usageRows, attemptRows, attemptsQuery, pageCount, options };
})();

if (typeof module !== "undefined") module.exports = kbLlm;

// ── the pages (they need app.js and the shared components, and touch the page
// only when called; they are plain top-level functions so app.js can call them) ──

// What the reader chose, kept across page changes.
const llmState = { usagePeriod: "7d", usageGroup: "model", period: "7d", model: "", kind: "", outcome: "", page: 1 };

const MODEL_LABELS = {
  provider: "Provider",
  model: "Model",
  tags: "Tags",
  priority: "Priority",
  availability: "Availability",
  available: "available",
  heldOut: "held out",
  noCredentials: "no credentials",
  until: "until",
};
const USAGE_LABELS = { user: "Kind", provider: "Provider", model: "Model", calls: "Calls", inputTokens: "Input tokens", outputTokens: "Output tokens", cost: "Cost" };
const ATTEMPT_LABELS = { started: "Started", user: "Kind", provider: "Provider", model: "Model", outcome: "Outcome", inputTokens: "Input tokens", outputTokens: "Output tokens", cost: "Cost", duration: "Duration" };
const KIND_LABELS = { chat: "chat", generate_title: "generate title", generate_summary: "generate summary", translate: "translate", embed: "embed" };

// Fills a <select> and keeps what the reader picked; onChange gets its value.
function fillSelect(id, pairs, selected, onChange) {
  const select = $(id);
  select.innerHTML = kbLlm.options(pairs, selected);
  select.onchange = () => onChange(select.value);
}

async function loadModelsPage() {
  const table = $("modelsTable");
  table.labels = MODEL_LABELS;
  table.emptyText = "No models are configured.";
  const body = await api("/admin/models");
  table.rows = kbLlm.modelRows(body.models);
}

async function loadUsagePage() {
  fillSelect("usagePeriod", kbLlm.PERIODS, llmState.usagePeriod, (v) => {
    llmState.usagePeriod = v;
    run(loadUsagePage);
  });
  fillSelect(
    "usageGroup",
    [
      ["user", "By kind"],
      ["provider", "By provider"],
      ["model", "By model"],
      ["", "Kind, provider and model"],
    ],
    llmState.usageGroup,
    (v) => {
      llmState.usageGroup = v;
      $("usageTable").groupBy = v; // the rows are already here; the table sums them
    },
  );
  const table = $("usageTable");
  table.labels = USAGE_LABELS;
  table.emptyText = "No successful model calls in this period.";
  table.groupBy = llmState.usageGroup;
  const since = encodeURIComponent(kbLlm.sinceFor(llmState.usagePeriod));
  const body = await api(`/admin/llm-aggregates?since=${since}&group_by=kind_model`);
  table.rows = kbLlm.usageRows(body.items);
}

async function loadAttemptsPage(page = llmState.page) {
  llmState.page = Math.max(1, page);
  const models = await api("/admin/models");
  fillSelect("attemptsPeriod", kbLlm.PERIODS, llmState.period, (v) => {
    llmState.period = v;
    run(() => loadAttemptsPage(1));
  });
  fillSelect("attemptsModel", [["", "All models"], ...(models.models || []).map((m) => [m.id, esc(m.id)])], llmState.model, (v) => {
    llmState.model = v;
    run(() => loadAttemptsPage(1));
  });
  fillSelect("attemptsKind", [["", "All kinds"], ...kbLlm.KINDS.map((k) => [k, KIND_LABELS[k]])], llmState.kind, (v) => {
    llmState.kind = v;
    run(() => loadAttemptsPage(1));
  });
  fillSelect("attemptsOutcome", [["", "All outcomes"], ...kbLlm.OUTCOMES.map((o) => [o, o.replace("_", " ")])], llmState.outcome, (v) => {
    llmState.outcome = v;
    run(() => loadAttemptsPage(1));
  });
  const table = $("attemptsTable");
  table.labels = ATTEMPT_LABELS;
  table.emptyText = "No model calls match.";
  const body = await api("/admin/llm-calls?" + kbLlm.attemptsQuery(llmState, llmState.page));
  table.rows = kbLlm.attemptRows(body.items);
  const pager = $("attemptsPager");
  pager.setAttribute("page", String(llmState.page));
  pager.setAttribute("page-count", String(kbLlm.pageCount(body.total)));
}


// The pager is in the page only in a browser.
if (typeof document !== "undefined") {
  $("attemptsPager").addEventListener("dp-pager-select", (e) => run(() => loadAttemptsPage(e.detail.page)));
}
