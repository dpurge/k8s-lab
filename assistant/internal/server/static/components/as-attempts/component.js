// as-attempts: every attempt to call a model, newest first, a page at a time:
// when, who, which provider and model, how it ended, tokens, cost and how long
// it took. Failed attempts are in the list (the usage figures leave them out).
// It loads its own data from the JSON API.
//
// Attributes: period ("all", "24h", "7d" or "30d"; default "all"), page
// (1-based), page-size (default 25).
// Events (bubbling): as-list-change {period, page} when the user picks a period
// or a page, as-load {ok, message, live: false, at} after every load. It never
// changes its own attributes: the host decides what a change means.
(function () {
  const STYLE_ID = "as-attempts-style";
  const PERIODS = { all: ["All time", 0], "24h": ["Last 24 hours", 24 * 3600], "7d": ["Last 7 days", 7 * 24 * 3600], "30d": ["Last 30 days", 30 * 24 * 3600] };
  const DEFAULT_PAGE_SIZE = 25;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-attempts/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsAttempts extends HTMLElement {
    static get observedAttributes() {
      return ["period", "page", "page-size"];
    }

    get period() {
      const v = this.getAttribute("period") || "all";
      return Object.prototype.hasOwnProperty.call(PERIODS, v) ? v : "all";
    }

    get page() {
      const n = parseInt(this.getAttribute("page"), 10);
      return n >= 1 ? n : 1;
    }

    get pageSize() {
      const n = parseInt(this.getAttribute("page-size"), 10);
      return n >= 1 && n <= 200 ? n : DEFAULT_PAGE_SIZE;
    }

    connectedCallback() {
      ensureStyle();
      this._build();
      this._load();
    }

    disconnectedCallback() {
      this._seq = (this._seq || 0) + 1; // a response arriving later is ignored
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      this._select.value = this.period;
      this._load();
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      this._users = new Map();

      const controls = el("div", "as-attempts-controls");
      const label = el("label", "as-attempts-filter");
      label.append(el("span", "", "Period "));
      const select = document.createElement("select");
      select.className = "as-attempts-period";
      for (const [value, [text]] of Object.entries(PERIODS)) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = text;
        select.appendChild(option);
      }
      select.addEventListener("change", () => {
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: select.value, page: 1 }, bubbles: true }));
      });
      label.append(select);
      this._summary = el("span", "as-attempts-summary");
      controls.append(label, this._summary);

      // The table is the shared dp-attempt-table; this element loads and pages
      // and hands it the rows.
      this._table = document.createElement("dp-attempt-table");
      this._table.setAttribute("empty-text", "No model calls in this period.");
      this._table.rows = [];

      this._message = el("p", "as-attempts-message");
      this._message.setAttribute("role", "status");
      this._pager = document.createElement("dp-pager");
      this._pager.addEventListener("dp-pager-select", (e) => {
        e.stopPropagation();
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: this.period, page: e.detail.page }, bubbles: true }));
      });
      this._select = select;
      this.append(controls, this._table, this._message, this._pager);
      select.value = this.period;
    }

    _query() {
      const params = new URLSearchParams();
      const seconds = PERIODS[this.period][1];
      if (seconds) params.set("since", new Date(Date.now() - seconds * 1000).toISOString());
      params.set("limit", String(this.pageSize));
      params.set("offset", String((this.page - 1) * this.pageSize));
      return params.toString();
    }

    async _load() {
      const seq = ++this._seq;
      this.setAttribute("aria-busy", "true");
      let failure = "";
      try {
        const body = await window.asApi.getJSON(`/api/v1/llm-calls?${this._query()}`);
        if (seq !== this._seq) return;
        if (body.items.some((row) => !this._users.has(row.user_id))) {
          try {
            const users = await window.asApi.loadUsers();
            if (seq === this._seq) this._users = users;
          } catch (e) {
            /* rows are still shown, with the user's id */
          }
        }
        if (seq !== this._seq) return;
        this._render(body);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = `Could not load attempts: ${err.message}`;
        this._message.textContent = failure;
        this._message.classList.add("error");
      }
      this.removeAttribute("aria-busy");
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok: failure === "", message: failure, live: false, at: new Date() }, bubbles: true }));
    }

    _render({ items, total, limit }) {
      const f = window.asFormat;
      const pageCount = Math.max(1, Math.ceil(total / limit));
      this._message.classList.remove("error");
      this._message.textContent = "";
      if (items.length === 0 && total > 0 && this.page > pageCount) {
        // The list shrank under the reader (the history is purged): ask the host
        // for the last page.
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: this.period, page: pageCount }, bubbles: true }));
      }
      this._summary.textContent = total === 1 ? "1 attempt" : total === 0 ? "" : `${f.tokens(total)} attempts`;
      this._pager.setAttribute("page", String(this.page));
      this._pager.setAttribute("page-count", String(pageCount));
      this._table.rows = items.map((call) => ({
        startedAt: call.started_at,
        user: this._users.get(call.user_id) || window.asApi.shortId(call.user_id),
        userId: call.user_id,
        provider: call.provider_id,
        model: call.model_id,
        outcome: call.outcome,
        error: call.error,
        inputTokens: call.input_tokens,
        outputTokens: call.output_tokens,
        costUsd: call.cost_usd,
        durationMs: call.duration_ms,
      }));
    }
  }

  customElements.define("as-attempts", AsAttempts);
})();
