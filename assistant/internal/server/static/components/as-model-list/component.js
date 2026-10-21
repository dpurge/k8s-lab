// as-model-list: the configured models in the order they are tried (highest
// priority first) and whether each can take calls now. A model the circuit
// breaker holds out shows when it will be tried again. It loads its own data
// and refreshes itself, since availability changes without anyone acting.
//
// Attribute: refresh-ms (default 10000; 0 turns refreshing off).
// Event (bubbling): as-load {ok, message, live, at} after every load.
(function () {
  const STYLE_ID = "as-model-list-style";
  const DEFAULT_REFRESH_MS = 10000;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-model-list/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsModelList extends HTMLElement {
    static get observedAttributes() {
      return ["refresh-ms"];
    }

    get refreshMs() {
      const raw = this.getAttribute("refresh-ms");
      if (raw === null) return DEFAULT_REFRESH_MS;
      const n = Number(raw);
      return Number.isFinite(n) && n >= 0 ? n : DEFAULT_REFRESH_MS;
    }

    connectedCallback() {
      ensureStyle();
      this._build();
      this._load();
    }

    disconnectedCallback() {
      clearTimeout(this._timer);
      this._seq = (this._seq || 0) + 1;
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      clearTimeout(this._timer);
      this._schedule();
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      // The table is the shared dp-model-table; this element loads and refreshes
      // and hands it the rows.
      this._table = document.createElement("dp-model-table");
      this._table.setAttribute("empty-text", "No models are configured.");
      this._table.rows = [];
      this._message = el("p", "as-model-list-message");
      this._message.setAttribute("role", "status");
      this.append(this._table, this._message);
    }

    async _load() {
      clearTimeout(this._timer);
      const seq = ++this._seq;
      let failure = "";
      try {
        const body = await window.asApi.getJSON("/api/v1/models");
        if (seq !== this._seq) return;
        this._render(body.items);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = `Could not load models: ${err.message}`;
        this._message.textContent = failure;
        this._message.classList.add("error");
      }
      this._schedule();
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok: failure === "", message: failure, live: !!this._live, at: new Date() }, bubbles: true }));
    }

    _schedule() {
      clearTimeout(this._timer);
      this._live = false;
      if (this.refreshMs > 0 && this.isConnected) {
        this._live = true;
        this._timer = setTimeout(() => this._load(), this.refreshMs);
      }
    }

    _render(items) {
      this._message.classList.remove("error");
      this._message.textContent = "";
      // Tried in this order: highest priority first, ties as in the file.
      const ordered = items.map((m, i) => [m, i]).sort((a, b) => b[0].priority - a[0].priority || a[1] - b[1]).map(([m]) => m);
      this._table.rows = ordered.map((m) => ({
        provider: m.provider,
        model: m.id,
        tags: m.tags || [],
        priority: m.priority,
        state: m.available ? "available" : m.unavailable_reason === "missing_credentials" ? "no_credentials" : "held_out",
        until: !m.available && m.unavailable_until ? m.unavailable_until : undefined,
      }));
    }
  }

  customElements.define("as-model-list", AsModelList);
})();
