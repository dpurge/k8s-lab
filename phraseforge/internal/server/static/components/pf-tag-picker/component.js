// pf-tag-picker: a set of checkboxes, one per tag, for choosing zero or more
// tags. Set .options (every tag offered) and .value (the ticked ones); read
// .value back, and listen for pf-tag-picker-change ({ detail: { value } }).
// A ticked tag that is not among the options is still shown (and stays ticked),
// so a saved choice never disappears silently when a tag leaves the list.
// Built with DOM nodes and textContent only, never innerHTML, since tags come
// from configuration.
(function () {
  const STYLE_ID = "pf-tag-picker-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-tag-picker/component.css";
    document.head.appendChild(link);
  }

  class PfTagPicker extends HTMLElement {
    constructor() {
      super();
      this._options = [];
      this._selected = new Set();
    }

    connectedCallback() {
      ensureStyle();
      this.setAttribute("role", "group");
      this._render();
    }

    set options(list) {
      this._options = [...(list || [])];
      this._render();
    }

    get options() {
      return [...this._options];
    }

    set value(list) {
      this._selected = new Set(list || []);
      this._render();
    }

    // The ticked tags, in the order they are shown.
    get value() {
      return this._shownTags().filter((tag) => this._selected.has(tag));
    }

    _shownTags() {
      const extra = [...this._selected].filter((tag) => !this._options.includes(tag));
      return [...this._options, ...extra];
    }

    _render() {
      const labels = this._shownTags().map((tag) => {
        const label = document.createElement("label");
        label.className = "pf-tag-picker-option";
        const input = document.createElement("input");
        input.type = "checkbox";
        input.value = tag;
        input.checked = this._selected.has(tag);
        input.addEventListener("change", () => {
          if (input.checked) this._selected.add(tag);
          else this._selected.delete(tag);
          this.dispatchEvent(new CustomEvent("pf-tag-picker-change", { detail: { value: this.value } }));
        });
        label.append(input, document.createTextNode(tag));
        return label;
      });
      this.replaceChildren(...labels);
    }
  }

  customElements.define("pf-tag-picker", PfTagPicker);
})();
