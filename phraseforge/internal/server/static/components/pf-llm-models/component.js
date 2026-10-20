// pf-llm-models: a table of the configured LLM models (id, provider, tags,
// priority, availability, and when unavailable a small reason badge from
// unavailable_reason) with a Refresh button. Set .labels (the texts, with
// .reasons mapping a reason code to its text) and
// .models (the rows of GET /api/v1/admin/models); listen for
// pf-llm-models-refresh, which the host answers by setting .models again.
// Built with DOM nodes and textContent only, never innerHTML, since every
// value comes from configuration.
(function () {
  const STYLE_ID = "pf-llm-models-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-llm-models/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class PfLlmModels extends HTMLElement {
    constructor() {
      super();
      this._models = [];
      this._labels = {};
      this._locale = undefined;
    }

    connectedCallback() {
      ensureStyle();
      this._render();
    }

    set models(list) {
      this._models = list || [];
      this._render();
    }

    set labels(value) {
      this._labels = value || {};
      this._render();
    }

    set locale(value) {
      this._locale = value;
      this._render();
    }

    _render() {
      if (!this.isConnected) return;
      const L = this._labels;
      const numbers = new Intl.NumberFormat(this._locale);

      const refresh = el("button", "btn btn-secondary pf-llm-refresh", L.refresh || "");
      refresh.type = "button";
      refresh.addEventListener("click", () => {
        this.dispatchEvent(new CustomEvent("pf-llm-models-refresh", { bubbles: true }));
      });
      const header = el("div", "pf-llm-header");
      header.append(el("h2", "pf-llm-title", L.heading || ""), refresh);

      const head = el("tr");
      for (const text of [L.colModel, L.colProvider, L.colTags, L.colPriority, L.colAvailability]) {
        head.appendChild(el("th", "", text || ""));
      }
      const body = el("tbody");
      for (const model of this._models) {
        const row = el("tr");
        row.dataset.modelId = model.id;
        const tags = el("td");
        for (const tag of model.tags || []) tags.appendChild(el("span", "badge", tag));
        const badge = el("span", "badge pf-llm-availability", model.available ? L.available : L.unavailable);
        badge.dataset.available = String(!!model.available);
        const availability = el("td");
        availability.appendChild(badge);
        if (!model.available && model.unavailable_reason) {
          const text = (L.reasons || {})[model.unavailable_reason] || model.unavailable_reason;
          const reason = el("span", "badge pf-llm-reason", text);
          reason.title = text;
          reason.dataset.reason = model.unavailable_reason;
          availability.appendChild(reason);
        }
        row.append(el("td", "", model.id), el("td", "", model.provider_id), tags, el("td", "", numbers.format(model.priority)), availability);
        body.appendChild(row);
      }
      if (this._models.length === 0) {
        const cell = el("td", "pf-llm-empty", L.none || "");
        cell.colSpan = 5;
        const row = el("tr");
        row.appendChild(cell);
        body.appendChild(row);
      }
      const table = el("table", "admin-table");
      const thead = el("thead");
      thead.appendChild(head);
      table.append(thead, body);
      const scroll = el("div", "pf-llm-scroll");
      scroll.appendChild(table);
      this.replaceChildren(header, scroll);
    }
  }

  customElements.define("pf-llm-models", PfLlmModels);
})();
