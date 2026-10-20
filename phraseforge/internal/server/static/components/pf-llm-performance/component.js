// pf-llm-performance: per-model (or per-purpose-and-model) statistics of the
// LLM attempts in a period. Set .labels (the texts), .items (the items of
// GET /api/v1/admin/llm-aggregates), .groupBy ("model" or "kind_model"),
// .windowValue ("1h", "24h", "7d", "30d" or "all") and .locale; listen for
// pf-llm-performance-change ({ detail: { window, groupBy, since } }) when the
// user picks another period or grouping, where since is the RFC 3339 start of
// the period for the host to fetch. A null number is shown as a dash. The
// table has seven columns (model with its provider, and in the purpose grouping
// the purpose, as a muted second line) and fits a card about 600 px wide.
// Built with DOM nodes and textContent only, never innerHTML.
(function () {
  const STYLE_ID = "pf-llm-performance-style";
  const DASH = "—";
  const HOUR_MS = 3600 * 1000;
  const WINDOWS = [
    { value: "1h", labelKey: "window1h", ms: HOUR_MS },
    { value: "24h", labelKey: "window24h", ms: 24 * HOUR_MS },
    { value: "7d", labelKey: "window7d", ms: 7 * 24 * HOUR_MS },
    { value: "30d", labelKey: "window30d", ms: 30 * 24 * HOUR_MS },
    { value: "all", labelKey: "windowAll", ms: null },
  ];
  const GROUPINGS = [
    { value: "model", labelKey: "groupModel" },
    { value: "kind_model", labelKey: "groupKindModel" },
  ];
  const OUTCOMES = [
    ["ok_calls", "outcomeOk"],
    ["error_calls", "outcomeError"],
    ["timeout_calls", "outcomeTimeout"],
    ["rate_limited_calls", "outcomeRateLimited"],
    ["canceled_calls", "outcomeCanceled"],
  ];
  const ALL_TIME_SINCE = "1970-01-01T00:00:00Z";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-llm-performance/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // sinceFor returns the RFC 3339 start of a period ending at now; "all" starts
  // at the epoch because a missing since means the last 24 hours to the API.
  function sinceFor(windowValue, now) {
    const period = WINDOWS.find((w) => w.value === windowValue);
    if (!period || period.ms === null) return ALL_TIME_SINCE;
    return new Date(now.getTime() - period.ms).toISOString();
  }

  class PfLlmPerformance extends HTMLElement {
    constructor() {
      super();
      this._items = [];
      this._labels = {};
      this._locale = undefined;
      this._groupBy = "model";
      this._window = "24h";
    }

    connectedCallback() {
      ensureStyle();
      this._renderControls();
      this._renderTable();
    }

    set items(list) {
      this._items = list || [];
      this._renderTable();
    }

    set labels(value) {
      this._labels = value || {};
      this._renderControls();
      this._renderTable();
    }

    set locale(value) {
      this._locale = value;
      this._renderTable();
    }

    set groupBy(value) {
      this._groupBy = value;
      this._renderControls();
      this._renderTable();
    }

    get groupBy() {
      return this._groupBy;
    }

    set windowValue(value) {
      this._window = value;
      this._renderControls();
    }

    get windowValue() {
      return this._window;
    }

    _emitChange() {
      const detail = { window: this._window, groupBy: this._groupBy, since: sinceFor(this._window, new Date()) };
      this.dispatchEvent(new CustomEvent("pf-llm-performance-change", { detail, bubbles: true }));
    }

    // The controls are drawn apart from the table so that choosing a period
    // does not rebuild the select the user is on when the new numbers arrive.
    _renderControls() {
      if (!this.isConnected) return;
      const L = this._labels;
      const select = el("select");
      select.setAttribute("aria-label", L.period || "");
      for (const w of WINDOWS) {
        const option = el("option", "", L[w.labelKey] || w.value);
        option.value = w.value;
        option.selected = w.value === this._window;
        select.appendChild(option);
      }
      select.addEventListener("change", () => {
        this._window = select.value;
        this._emitChange();
      });

      const group = el("div", "pf-llm-toggle");
      group.setAttribute("role", "group");
      group.setAttribute("aria-label", L.groupBy || "");
      for (const g of GROUPINGS) {
        const button = el("button", "btn btn-secondary", L[g.labelKey] || g.value);
        button.type = "button";
        button.dataset.groupBy = g.value;
        button.setAttribute("aria-pressed", String(g.value === this._groupBy));
        button.addEventListener("click", () => {
          if (this._groupBy === g.value) return;
          this._groupBy = g.value;
          this._renderControls();
          this._renderTable();
          this._emitChange();
        });
        group.appendChild(button);
      }
      const toolbar = el("div", "pf-llm-toolbar");
      toolbar.append(select, group);
      const heading = el("h2", "pf-llm-title", L.heading || "");
      if (!this._controls) {
        this._controls = el("div", "pf-llm-controls");
        this._table = el("div", "pf-llm-scroll");
        this._hint = el("p", "pf-llm-hint");
        this.replaceChildren(this._controls, this._table, this._hint);
      }
      this._controls.replaceChildren(heading, toolbar);
      this._hint.textContent = L.hint || "";
    }

    _renderTable() {
      if (!this.isConnected || !this._table) return;
      const L = this._labels;
      const byKind = this._groupBy === "kind_model";
      const integer = new Intl.NumberFormat(this._locale);
      const percent = new Intl.NumberFormat(this._locale, { style: "percent", maximumFractionDigits: 1 });
      const oneDecimal = new Intl.NumberFormat(this._locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
      const cost = new Intl.NumberFormat(this._locale, { minimumFractionDigits: 4, maximumFractionDigits: 4 });
      const orDash = (value, format) => (value === null || value === undefined ? DASH : format(value));
      const seconds = (ms) => oneDecimal.format(ms / 1000);

      // Seven columns, each header short with its full wording in the title,
      // so the table fits a card about 600 px wide without sideways scrolling.
      const columns = [
        [L.colModel, ""],
        [L.colCalls, L.tipCalls],
        [L.colSuccess, L.tipSuccess],
        [L.colTime, L.tipTime],
        [L.colTokens, L.tipTokens],
        [L.colTokensPerSecond, L.tipTokensPerSecond],
        [L.colCost, L.tipCost],
      ];
      const head = el("tr");
      for (const [text, tip] of columns) {
        const th = el("th", "", text || "");
        if (tip) th.title = tip;
        head.appendChild(th);
      }

      const body = el("tbody");
      for (const item of this._items) {
        const row = el("tr");
        row.dataset.modelId = item.model_id;
        const model = el("td", "pf-llm-model", item.model_id);
        const kind = byKind ? (this.kindLabel ? this.kindLabel(item.kind) : item.kind || "") : "";
        model.appendChild(el("div", "pf-llm-sub", [kind, item.provider_id].filter((part) => part).join(" · ")));
        const calls = el("td", "pf-llm-number", integer.format(item.calls));
        calls.title = OUTCOMES.map(([key, labelKey]) => `${L[labelKey] || key}: ${integer.format(item[key] || 0)}`).join(", ");
        const time = [item.mean_duration_ms, item.p50_duration_ms, item.p95_duration_ms]
          .map((ms) => orDash(ms, seconds))
          .join(" / ");
        row.append(
          model,
          calls,
          el("td", "pf-llm-number", percent.format(item.success_rate)),
          el("td", "pf-llm-number", time),
          el("td", "pf-llm-number", `${integer.format(item.input_tokens)} / ${integer.format(item.output_tokens)}`),
          el("td", "pf-llm-number", orDash(item.output_tokens_per_second, (v) => oneDecimal.format(v))),
          el("td", "pf-llm-number", orDash(item.cost_usd, (v) => cost.format(v))),
        );
        body.appendChild(row);
      }
      if (this._items.length === 0) {
        const cell = el("td", "pf-llm-empty", L.none || "");
        cell.colSpan = columns.length;
        const row = el("tr");
        row.appendChild(cell);
        body.appendChild(row);
      }
      const table = el("table", "admin-table");
      const thead = el("thead");
      thead.appendChild(head);
      table.append(thead, body);
      this._table.replaceChildren(table);
    }
  }

  PfLlmPerformance.sinceFor = sinceFor;
  customElements.define("pf-llm-performance", PfLlmPerformance);
})();
