// dp-pager: numbered page navigation. It never navigates itself: it reports
// the page the user picked and the host decides what that means.
//
//   <dp-pager page="3" page-count="12"></dp-pager>
//   pager.addEventListener("dp-pager-select", (e) => goTo(e.detail.page));
//
// Previous and Next show whenever there is more than one page; the numbers
// between them only with more than two pages, at most max-numbers at a time
// (ten by default) with an ellipsis on each side that has hidden pages. With
// one page or fewer nothing is drawn.
//
// Attributes (also properties: page, pageCount, prevLabel, nextLabel, label,
//   maxNumbers)
//   page          the current page, 1-based (kept within 1..page-count)
//   page-count    the number of pages
//   prev-label    text of the Previous button ("Previous")
//   next-label    text of the Next button ("Next")
//   label         accessible name of the navigation ("Pages")
//   max-numbers   most page numbers shown at once (10)
//
// Events
//   dp-pager-select  detail { page }; bubbles, composed. The current page's
//                    button sends it too, so the host can reload.
//
// Parts: nav, item, current, prev, next, gap
//
// CSS custom properties: --dp-pager-color (falls back to --muted),
//   --dp-pager-hover-color, --dp-pager-active-bg (falls back to --btn-bg),
//   --dp-pager-active-color, --dp-pager-focus-color.
import { LitElement, html, nothing, unsafeCSS } from "lit";
import styles from "./component.css";

// pageItems returns the numbers to show, with null standing for an ellipsis:
// up to maxNumbers consecutive pages around the current one (the window sits
// as far before it as after it, shifted at either end), and an ellipsis where
// pages are hidden.
export function pageItems(page: number, pageCount: number, maxNumbers = 10): (number | null)[] {
  const size = Math.min(maxNumbers, pageCount);
  const before = Math.floor((size - 1) / 2);
  const start = Math.max(1, Math.min(page - before, pageCount - size + 1));
  const end = start + size - 1;
  const items: (number | null)[] = [];
  if (start > 1) items.push(null);
  for (let p = start; p <= end; p++) items.push(p);
  if (end < pageCount) items.push(null);
  return items;
}

export class DpPager extends LitElement {
  static properties = {
    page: { type: Number },
    pageCount: { type: Number, attribute: "page-count" },
    prevLabel: { attribute: "prev-label" },
    nextLabel: { attribute: "next-label" },
    label: {},
    maxNumbers: { type: Number, attribute: "max-numbers" },
  };

  static styles = unsafeCSS(styles);

  static pageItems = pageItems;

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare page: number;
  declare pageCount: number;
  declare prevLabel: string;
  declare nextLabel: string;
  declare label: string;
  declare maxNumbers: number;

  constructor() {
    super();
    this.page = 1;
    this.pageCount = 1;
    this.prevLabel = "";
    this.nextLabel = "";
    this.label = "";
    this.maxNumbers = 10;
  }

  #select(page: number) {
    this.dispatchEvent(new CustomEvent("dp-pager-select", { detail: { page }, bubbles: true, composed: true }));
  }

  #item(text: string, page: number, kind: string, disabled = false, current = false) {
    return html`<button
      type="button"
      class=${current ? `item current ${kind}` : `item ${kind}`}
      part=${current ? `item current ${kind}` : `item ${kind}`}
      ?disabled=${disabled}
      aria-current=${current ? "page" : nothing}
      @click=${() => this.#select(page)}
    >${text}</button>`;
  }

  render() {
    const count = Math.max(1, Math.floor(Number(this.pageCount)) || 1);
    if (count <= 1) return nothing;
    const page = Math.min(count, Math.max(1, Math.floor(Number(this.page)) || 1));
    const maxNumbers = Math.max(1, Math.floor(Number(this.maxNumbers)) || 10);
    return html`
      <nav part="nav" aria-label=${this.label || "Pages"}>
        ${this.#item(this.prevLabel || "Previous", page - 1, "prev", page <= 1)}
        ${count > 2
          ? pageItems(page, count, maxNumbers).map((item) =>
              item === null ? html`<span class="gap" part="gap">…</span>` : this.#item(String(item), item, "number", false, item === page),
            )
          : nothing}
        ${this.#item(this.nextLabel || "Next", page + 1, "next", page >= count)}
      </nav>
    `;
  }
}

customElements.define("dp-pager", DpPager);
