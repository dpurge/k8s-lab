// as-usage: what each user used of each model of each provider: calls, tokens
// and cost, highest cost first, with a period filter and a grouping. It loads
// its own data from the JSON API. Only successful calls count; a failed attempt
// has no tokens and no cost.
//
// Attributes: period ("all", "24h", "7d" or "30d"; default "all"); group
// ("user", "provider" or "model" sums the rows over the other two; anything
// else, the default, shows one row per user, provider and model).
// Events (bubbling): as-list-change {period, group} when the user picks one,
// as-load {ok, message, live: false, at} after every load. It never changes
// its own attributes: the host decides what a change means.
(function () {
  const STYLE_ID = "as-usage-style";
  const PERIODS = { all: ["All time", 0], "24h": ["Last 24 hours", 24 * 3600], "7d": ["Last 7 days", 7 * 24 * 3600], "30d": ["Last 30 days", 30 * 24 * 3600] };
  // The groupings the select offers; dp-usage-table sums the rows for each.
  const GROUPS = { "": "User, provider and model", user: "User", provider: "Provider", model: "Model" };

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-usage/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsUsage extends HTMLElement {
    static get observedAttributes() {
      return ["period", "group"];
    }

    get group() {
      const v = this.getAttribute("group") || "";
      return Object.prototype.hasOwnProperty.call(GROUPS, v) ? v : "";
    }

    get period() {
      const v = this.getAttribute("period") || "all";
      return Object.prototype.hasOwnProperty.call(PERIODS, v) ? v : "all";
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
      this._groupSelect.value = this.group;
      if (name === "group") {
        this._table.groupBy = this.group;
        return;
      }
      this._load();
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      this._users = new Map();

      const controls = el("div", "as-usage-controls");
      const label = el("label", "as-usage-filter");
      label.append(el("span", "", "Period "));
      const select = document.createElement("select");
      select.className = "as-usage-period";
      for (const [value, [text]] of Object.entries(PERIODS)) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = text;
        select.appendChild(option);
      }
      select.addEventListener("change", () => {
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: select.value, group: this.group }, bubbles: true }));
      });
      label.append(select);
      const groupLabel = el("label", "as-usage-filter");
      groupLabel.append(el("span", "", "Group by "));
      const groupSelect = document.createElement("select");
      groupSelect.className = "as-usage-group";
      for (const [value, text] of Object.entries(GROUPS)) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = text;
        groupSelect.appendChild(option);
      }
      groupSelect.addEventListener("change", () => {
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: this.period, group: groupSelect.value }, bubbles: true }));
      });
      groupLabel.append(groupSelect);
      this._groupSelect = groupSelect;
      this._summary = el("span", "as-usage-summary");
      controls.append(label, groupLabel, this._summary);

      // The table is the shared dp-usage-table; this element loads the rows and
      // hands them over, with the grouping.
      this._table = document.createElement("dp-usage-table");
      this._table.setAttribute("empty-text", "No model calls in this period.");
      this._table.rows = [];

      this._message = el("p", "as-usage-message");
      this._message.setAttribute("role", "status");
      this._select = select;
      // The history is purged after ASSISTANT_LLM_CALLS_RETENTION_DAYS (default 90).
      const note = el("p", "as-usage-note", "Covers only the retained call history (90 days by default).");
      this.append(controls, this._table, this._message, note);
      select.value = this.period;
      groupSelect.value = this.group;
    }

    _since() {
      const seconds = PERIODS[this.period][1];
      return seconds ? new Date(Date.now() - seconds * 1000).toISOString() : "";
    }

    async _load() {
      const seq = ++this._seq;
      this.setAttribute("aria-busy", "true");
      let failure = "";
      try {
        const since = this._since();
        const body = await window.asApi.getJSON(`/api/v1/usage${since ? `?since=${encodeURIComponent(since)}` : ""}`);
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
        this._render(body.items);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = `Could not load usage: ${err.message}`;
        this._message.textContent = failure;
        this._message.classList.add("error");
      }
      this.removeAttribute("aria-busy");
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok: failure === "", message: failure, live: false, at: new Date() }, bubbles: true }));
    }

    _render(items) {
      const f = window.asFormat;
      this._message.classList.remove("error");
      this._message.textContent = "";

      let calls = 0;
      let cost = 0;
      for (const row of items) {
        calls += row.calls;
        cost += row.cost_usd;
      }
      this._table.groupBy = this.group;
      this._table.rows = items.map((row) => ({
        user: this._users.get(row.user_id) || window.asApi.shortId(row.user_id),
        userId: row.user_id,
        provider: row.provider_id,
        model: row.model_id,
        calls: row.calls,
        inputTokens: row.input_tokens,
        outputTokens: row.output_tokens,
        costUsd: row.cost_usd,
      }));
      this._summary.textContent = items.length === 0 ? "" : `${calls === 1 ? "1 call" : `${f.tokens(calls)} calls`} · ${f.cost(cost)}`;
    }
  }

  customElements.define("as-usage", AsUsage);
})();
