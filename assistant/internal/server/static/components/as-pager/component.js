// as-pager: numbered page navigation: page and page-count attributes in, a
// bubbling as-pager-select event ({page}) out. Previous and Next show whenever
// there is more than one page; the page numbers between them only appear with
// more than two pages, at most ten at a time with an ellipsis on each side
// that has hidden pages. It never navigates itself, so the host decides what a
// page change means. (Same behaviour as phraseforge's pf-pager.)
(function () {
  const STYLE_ID = "as-pager-style";
  const MAX_NUMBERS = 10; // most page numbers shown at once

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-pager/component.css";
    document.head.appendChild(link);
  }

  // pageItems returns the numbers to show, with null standing for an ellipsis.
  function pageItems(page, pageCount) {
    const size = Math.min(MAX_NUMBERS, pageCount);
    const start = Math.max(1, Math.min(page - 4, pageCount - size + 1));
    const end = start + size - 1;
    const items = [];
    if (start > 1) items.push(null);
    for (let p = start; p <= end; p++) items.push(p);
    if (end < pageCount) items.push(null);
    return items;
  }

  class AsPager extends HTMLElement {
    static get observedAttributes() {
      return ["page", "page-count", "prev-label", "next-label"];
    }

    get page() {
      return Number(this.getAttribute("page")) || 1;
    }

    get pageCount() {
      return Number(this.getAttribute("page-count")) || 1;
    }

    connectedCallback() {
      ensureStyle();
      this._render();
    }

    attributeChangedCallback() {
      this._render();
    }

    _button(label, page, { current = false, disabled = false } = {}) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "as-pager-item";
      button.textContent = label;
      if (current) {
        button.classList.add("active");
        button.setAttribute("aria-current", "page");
      }
      button.disabled = disabled;
      button.addEventListener("click", () => {
        this.dispatchEvent(new CustomEvent("as-pager-select", { detail: { page }, bubbles: true }));
      });
      return button;
    }

    _render() {
      if (!this.isConnected) return;
      this.textContent = "";
      const { page, pageCount } = this;
      if (pageCount <= 1) return;
      const nav = document.createElement("nav");
      nav.className = "as-pager-items";
      nav.setAttribute("aria-label", "Pages");
      const prev = this._button(this.getAttribute("prev-label") || "Previous", page - 1, { disabled: page <= 1 });
      prev.classList.add("as-pager-prev");
      nav.appendChild(prev);
      if (pageCount > 2) {
        for (const item of pageItems(page, pageCount)) {
          if (item === null) {
            const gap = document.createElement("span");
            gap.className = "as-pager-gap";
            gap.textContent = "…";
            nav.appendChild(gap);
          } else {
            nav.appendChild(this._button(String(item), item, { current: item === page }));
          }
        }
      }
      const next = this._button(this.getAttribute("next-label") || "Next", page + 1, { disabled: page >= pageCount });
      next.classList.add("as-pager-next");
      nav.appendChild(next);
      this.appendChild(nav);
    }
  }

  AsPager.pageItems = pageItems;
  customElements.define("as-pager", AsPager);
})();
