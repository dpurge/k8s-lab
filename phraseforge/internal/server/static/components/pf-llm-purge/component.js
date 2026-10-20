// pf-llm-purge: deletes the old LLM attempt records, after a confirmation. Set
// .labels (the texts); pick an age, press Delete, then confirm the message that
// says what will be deleted. Only the confirmation dispatches
// pf-llm-purge-confirm ({ detail: { olderThanDays } }, 0 meaning everything),
// which the host answers with the request and then .result = the text to show.
// Built with DOM nodes and textContent only, never innerHTML.
(function () {
  const STYLE_ID = "pf-llm-purge-style";
  const CHOICES = [
    { days: 7, labelKey: "option7" },
    { days: 30, labelKey: "option30" },
    { days: 90, labelKey: "option90" },
    { days: 0, labelKey: "optionAll" },
  ];

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-llm-purge/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class PfLlmPurge extends HTMLElement {
    constructor() {
      super();
      this._labels = {};
      this._days = 30;
      this._isConfirming = false;
      this._result = "";
    }

    connectedCallback() {
      ensureStyle();
      this._render();
    }

    set labels(value) {
      this._labels = value || {};
      this._render();
    }

    set result(text) {
      this._result = text || "";
      this._render();
    }

    _confirmText() {
      const L = this._labels;
      return this._days === 0 ? L.confirmAll || "" : (L.confirmDays || "").replace("%s", String(this._days));
    }

    _render() {
      if (!this.isConnected) return;
      const L = this._labels;
      const select = el("select");
      select.setAttribute("aria-label", L.label || "");
      select.disabled = this._isConfirming;
      for (const choice of CHOICES) {
        const option = el("option", "", L[choice.labelKey] || String(choice.days));
        option.value = String(choice.days);
        option.selected = choice.days === this._days;
        select.appendChild(option);
      }
      select.addEventListener("change", () => {
        this._days = Number(select.value);
      });
      const start = el("button", "btn btn-danger pf-llm-start", L.button || "");
      start.type = "button";
      start.disabled = this._isConfirming;
      start.addEventListener("click", () => {
        this._isConfirming = true;
        this._result = "";
        this._render();
      });
      const label = el("label", "pf-llm-label", L.label || "");
      const row = el("div", "pf-llm-row");
      row.append(label, select, start);

      const children = [el("h2", "pf-llm-title", L.heading || ""), row];
      if (this._isConfirming) {
        const yes = el("button", "btn btn-danger pf-llm-confirm", L.confirmButton || "");
        yes.type = "button";
        yes.addEventListener("click", () => {
          const olderThanDays = this._days;
          this._isConfirming = false;
          this._render();
          this.dispatchEvent(new CustomEvent("pf-llm-purge-confirm", { detail: { olderThanDays }, bubbles: true }));
        });
        const no = el("button", "btn btn-secondary pf-llm-cancel", L.cancel || "");
        no.type = "button";
        no.addEventListener("click", () => {
          this._isConfirming = false;
          this._render();
        });
        const confirm = el("div", "pf-llm-confirmation");
        confirm.setAttribute("role", "alert");
        confirm.append(el("p", "pf-llm-confirm-text", this._confirmText()), yes, no);
        children.push(confirm);
      }
      if (this._result) children.push(el("p", "pf-llm-result", this._result));
      children.push(el("p", "pf-llm-hint", L.hint || ""));
      this.replaceChildren(...children);
    }
  }

  customElements.define("pf-llm-purge", PfLlmPurge);
})();
