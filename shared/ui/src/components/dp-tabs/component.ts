// dp-tabs: a bar of choices, one of them current: a tab bar, a section menu,
// a header navigation. It never switches anything itself; it reports the
// choice and the host decides what it means.
//
//   const tabs = document.querySelector("dp-tabs");
//   tabs.items = [{ id: "texts", label: "Texts" }, { id: "docs", label: "Docs", href: "#/docs" }];
//   tabs.active = "texts";
//   tabs.addEventListener("dp-tabs-select", (e) => show(e.detail.id));
//
// Properties and attributes
//   items        the choices, [{ id, label, href? }]; as an attribute, a JSON
//                list. An item with an href is drawn as a real link (so
//                middle-click and "open in a new tab" work, and the page's
//                own router can follow it); one without is a button.
//   active       the id of the current choice (marked aria-current="page")
//   orientation  horizontal (default) or vertical (a stacked sidebar list)
//   variant      pills (default) or underline (quiet links, a line under the
//                current one)
//   label        the accessible name of the navigation
//
// Events
//   dp-tabs-select  detail { id }; bubbles, composed, cancelable. Sent on a
//                   click. Unless a listener calls preventDefault(), `active`
//                   then becomes that id.
//
// Parts: nav, item, active
//
// CSS custom properties: --dp-tabs-gap, --dp-tabs-color (falls back to --muted),
//   --dp-tabs-hover-color, --dp-tabs-active-bg (falls back to --btn-bg),
//   --dp-tabs-active-color, --dp-tabs-focus-color, and for the vertical list
//   --dp-tabs-vertical-color, --dp-tabs-vertical-active-bg,
//   --dp-tabs-vertical-active-color.
import { LitElement, html, nothing, unsafeCSS } from "lit";
import styles from "./component.css";

export interface TabItem {
  id: string;
  label: string;
  href?: string;
}

// Anything that is not a list of objects with an id and a label is ignored, so
// a bad JSON attribute shows an empty bar rather than breaking the page.
function validItems(raw: unknown): TabItem[] {
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((i) => i && typeof i === "object" && (typeof i.id === "string" || typeof i.id === "number") && typeof i.label === "string")
    .map((i) => ({ id: String(i.id), label: i.label, href: typeof i.href === "string" ? i.href : undefined }));
}

export class DpTabs extends LitElement {
  static properties = {
    items: { type: Array },
    active: {},
    orientation: { reflect: true },
    variant: { reflect: true },
    label: {},
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare items: TabItem[];
  declare active: string;
  declare orientation: string;
  declare variant: string;
  declare label: string;

  constructor() {
    super();
    this.items = [];
    this.active = "";
    this.orientation = "horizontal";
    this.variant = "pills";
    this.label = "";
  }

  #select(id: string) {
    const proceed = this.dispatchEvent(new CustomEvent("dp-tabs-select", { detail: { id }, bubbles: true, composed: true, cancelable: true }));
    if (proceed) this.active = id;
  }

  #item(item: TabItem) {
    const current = item.id === this.active;
    const cls = current ? "item active" : "item";
    const aria = current ? "page" : nothing;
    return item.href !== undefined
      ? html`<a class=${cls} part=${cls} href=${item.href} aria-current=${aria} @click=${() => this.#select(item.id)}>${item.label}</a>`
      : html`<button type="button" class=${cls} part=${cls} aria-current=${aria} @click=${() => this.#select(item.id)}>${item.label}</button>`;
  }

  render() {
    return html`<nav part="nav" aria-label=${this.label || nothing}>${validItems(this.items).map((item) => this.#item(item))}</nav>`;
  }
}

customElements.define("dp-tabs", DpTabs);
