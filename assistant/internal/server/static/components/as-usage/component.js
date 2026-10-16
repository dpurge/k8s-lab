// as-usage: what each user used of each model: calls, tokens and cost, highest
// cost first, with a period filter. It loads its own data from the JSON API.
// Only successful calls count; a failed attempt has no tokens and no cost.
//
// Attribute: period ("all", "24h", "7d" or "30d"; default "all").
// Events (bubbling): as-list-change {period} when the user picks a period,
// as-load {ok, message, live: false, at} after every load. It never changes
// its own attributes: the host decides what a change means.
(function () {
  const STYLE_ID = "as-usage-style";
  const PERIODS = { all: ["All time", 0], "24h": ["Last 24 hours", 24 * 3600], "7d": ["Last 7 days", 7 * 24 * 3600], "30d": ["Last 30 days", 30 * 24 * 3600] };
  const COLUMNS = ["User", "Model", "Calls", "Input tokens", "Output tokens", "Cost"];

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-usage/component.css";
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
      return ["period"];
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
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { period: select.value }, bubbles: true }));
      });
      label.append(select);
      this._summary = el("span", "as-usage-summary");
      controls.append(label, this._summary);

      const wrap = el("div", "as-usage-table-wrap");
      const table = document.createElement("table");
      table.className = "as-usage-table";
      const head = document.createElement("tr");
      for (const name of COLUMNS) head.appendChild(el("th", "", name));
      const thead = document.createElement("thead");
      thead.appendChild(head);
      this._body = document.createElement("tbody");
      table.append(thead, this._body);
      wrap.appendChild(table);
      this._wrap = wrap;

      this._message = el("p", "as-usage-message");
      this._message.setAttribute("role", "status");
      this._select = select;
      this.append(controls, wrap, this._message);
      select.value = this.period;
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
      this._body.textContent = "";
      this._wrap.hidden = items.length === 0;
      this._message.textContent = items.length === 0 ? "No model calls in this period." : "";

      let calls = 0;
      let cost = 0;
      for (const row of items) {
        calls += row.calls;
        cost += row.cost_usd;
        const user = el("td", "as-usage-user", this._users.get(row.user_id) || window.asApi.shortId(row.user_id));
        user.title = row.user_id;
        const tr = document.createElement("tr");
        tr.append(
          user,
          el("td", "as-usage-model", row.model_id),
          el("td", "as-usage-calls", f.tokens(row.calls)),
          el("td", "as-usage-input", f.tokens(row.input_tokens)),
          el("td", "as-usage-output", f.tokens(row.output_tokens)),
          el("td", "as-usage-cost", f.cost(row.cost_usd)),
        );
        this._body.appendChild(tr);
      }
      this._summary.textContent = items.length === 0 ? "" : `${calls === 1 ? "1 call" : `${f.tokens(calls)} calls`} · ${f.cost(cost)}`;
    }
  }

  customElements.define("as-usage", AsUsage);
})();
