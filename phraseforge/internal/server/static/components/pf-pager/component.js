// pf-pager: numbered page navigation — page and page-count attributes in, a
// bubbling pf-pager-select event ({page}) out. Previous/Next show whenever
// there is more than one page; the page numbers between them only appear
// with more than two pages, at most ten at a time with an ellipsis on each
// side that has hidden pages (specs/features/phraseforge-hash-navigation.md).
// Never navigates itself, so the host decides what a page change means.
(function () {
  const STYLE_ID = "pf-pager-style";
  const MAX_NUMBERS = 10; // most page numbers shown at once

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-pager/component.css";
    document.head.appendChild(link);
  }

  // pageItems returns the numbers to show, with null standing for an
  // ellipsis. Up to MAX_NUMBERS consecutive pages around the current one
  // (4 before, 5 after, shifted at either end); an ellipsis marks each side
  // that has hidden pages.
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

  class PfPager extends HTMLElement {
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
      button.className = "pf-pager-item";
      button.textContent = label;
      if (current) {
        button.classList.add("active");
        button.setAttribute("aria-current", "page");
      }
      button.disabled = disabled;
      button.addEventListener("click", () => {
        this.dispatchEvent(new CustomEvent("pf-pager-select", { detail: { page }, bubbles: true }));
      });
      return button;
    }

    _render() {
      if (!this.isConnected) return;
      this.textContent = "";
      const { page, pageCount } = this;
      if (pageCount <= 1) return;
      const nav = document.createElement("nav");
      nav.className = "pf-pager-items";
      const prev = this._button(this.getAttribute("prev-label") || "Previous", page - 1, { disabled: page <= 1 });
      prev.classList.add("pf-pager-prev");
      nav.appendChild(prev);
      if (pageCount > 2) {
        for (const item of pageItems(page, pageCount)) {
          if (item === null) {
            const gap = document.createElement("span");
            gap.className = "pf-pager-gap";
            gap.textContent = "…";
            nav.appendChild(gap);
          } else {
            nav.appendChild(this._button(String(item), item, { current: item === page }));
          }
        }
      }
      const next = this._button(this.getAttribute("next-label") || "Next", page + 1, { disabled: page >= pageCount });
      next.classList.add("pf-pager-next");
      nav.appendChild(next);
      this.appendChild(nav);
    }
  }

  PfPager.pageItems = pageItems;
  customElements.define("pf-pager", PfPager);
})();
