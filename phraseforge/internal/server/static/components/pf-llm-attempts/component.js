// pf-llm-attempts: the latest LLM attempts with a model and an outcome filter.
// Set .labels (the texts), .modelIds (the models the filter offers), .items
// (the items of GET /api/v1/admin/llm-calls) and .locale; listen for
// pf-llm-attempts-filter ({ detail: { modelId, outcome } }, "" for no filter),
// which the host answers by setting .items again. Each attempt is one row of six
// columns plus, only when there is something to show, a muted second row with
// the HTTP status, the request id and the provider's error text; the error is
// cut to two lines and its whole text is in its title.
// Built with DOM nodes and textContent only, never innerHTML, since an error
// text comes from a provider.
(function () {
  const STYLE_ID = "pf-llm-attempts-style";
  const OUTCOMES = [
    ["ok", "outcomeOk"],
    ["error", "outcomeError"],
    ["timeout", "outcomeTimeout"],
    ["rate_limited", "outcomeRateLimited"],
    ["canceled", "outcomeCanceled"],
  ];

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-llm-attempts/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class PfLlmAttempts extends HTMLElement {
    constructor() {
      super();
      this._items = [];
      this._labels = {};
      this._modelIds = [];
      this._locale = undefined;
      this._modelId = "";
      this._outcome = "";
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

    set modelIds(list) {
      this._modelIds = list || [];
      this._renderControls();
    }

    set locale(value) {
      this._locale = value;
      this._renderTable();
    }

    _emitFilter() {
      const detail = { modelId: this._modelId, outcome: this._outcome };
      this.dispatchEvent(new CustomEvent("pf-llm-attempts-filter", { detail, bubbles: true }));
    }

    _select(label, choices, current, onChange) {
      const select = el("select");
      select.setAttribute("aria-label", label || "");
      const all = el("option", "", `${label || ""}: ${this._labels.filterAll || ""}`);
      all.value = "";
      select.appendChild(all);
      for (const [value, text] of choices) {
        const option = el("option", "", text);
        option.value = value;
        select.appendChild(option);
      }
      select.value = current;
      select.addEventListener("change", () => onChange(select.value));
      return select;
    }

    // Drawn apart from the table so that choosing a filter does not rebuild the
    // select the user is on when the new rows arrive.
    _renderControls() {
      if (!this.isConnected) return;
      const L = this._labels;
      const models = this._select(L.filterModel, this._modelIds.map((id) => [id, id]), this._modelId, (value) => {
        this._modelId = value;
        this._emitFilter();
      });
      models.dataset.filter = "model";
      const outcomes = this._select(L.filterOutcome, OUTCOMES.map(([value, key]) => [value, L[key] || value]), this._outcome, (value) => {
        this._outcome = value;
        this._emitFilter();
      });
      outcomes.dataset.filter = "outcome";
      const toolbar = el("div", "pf-llm-toolbar");
      toolbar.append(models, outcomes);
      if (!this._controls) {
        this._controls = el("div", "pf-llm-controls");
        this._table = el("div", "pf-llm-scroll");
        this.replaceChildren(this._controls, this._table);
      }
      this._controls.replaceChildren(el("h2", "pf-llm-title", L.heading || ""), toolbar);
    }

    _purpose(item) {
      const kind = this.kindLabel ? this.kindLabel(item.kind) : item.kind;
      const languages = [item.source_language, item.target_language].filter((l) => l);
      return languages.length ? `${kind} (${languages.join(" → ")})` : kind;
    }

    // The second, muted line of an attempt: HTTP status, request id and the
    // provider's error text. Null when there is nothing to show.
    _detailRow(item, columnCount) {
      const L = this._labels;
      const hasStatus = item.http_status !== null && item.http_status !== undefined;
      if (!hasStatus && !item.request_id && !item.error) return null;
      const facts = [];
      if (hasStatus) facts.push(`${L.colHttp || "HTTP"} ${item.http_status}`);
      if (item.request_id) facts.push(`${L.request || "request"} ${item.request_id}`);
      const cell = el("td", "pf-llm-detail-cell");
      cell.colSpan = columnCount;
      if (facts.length) cell.appendChild(el("div", "pf-llm-facts", facts.join(" · ")));
      if (item.error) {
        const text = el("div", "pf-llm-error", item.error);
        text.title = item.error;
        cell.appendChild(text);
      }
      const row = el("tr", "pf-llm-detail");
      row.dataset.detailFor = item.id;
      row.appendChild(cell);
      return row;
    }

    _renderTable() {
      if (!this.isConnected || !this._table) return;
      const L = this._labels;
      const time = new Intl.DateTimeFormat(this._locale, { dateStyle: "short", timeStyle: "short" });
      const integer = new Intl.NumberFormat(this._locale);
      const oneDecimal = new Intl.NumberFormat(this._locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
      const columns = [
        [L.colTime, ""],
        [L.colPurpose, ""],
        [L.colModel, ""],
        [L.colOutcome, ""],
        [L.colDuration, ""],
        [L.colTokens, L.tipTokens],
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
        row.dataset.attemptId = item.id;
        const outcomeKey = (OUTCOMES.find(([value]) => value === item.outcome) || [])[1];
        const badge = el("span", "badge pf-llm-outcome", (outcomeKey && L[outcomeKey]) || item.outcome);
        badge.dataset.outcome = item.outcome;
        const outcome = el("td");
        outcome.appendChild(badge);
        row.append(
          el("td", "pf-llm-time", time.format(new Date(item.started_at))),
          el("td", "pf-llm-wrap", this._purpose(item)),
          el("td", "pf-llm-wrap", item.model_id),
          outcome,
          el("td", "pf-llm-number", oneDecimal.format(item.duration_ms / 1000)),
          el("td", "pf-llm-number", `${integer.format(item.input_tokens)} / ${integer.format(item.output_tokens)}`),
        );
        const detail = this._detailRow(item, columns.length);
        if (detail) row.classList.add("pf-llm-has-detail");
        body.appendChild(row);
        if (detail) body.appendChild(detail);
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

  customElements.define("pf-llm-attempts", PfLlmAttempts);
})();
