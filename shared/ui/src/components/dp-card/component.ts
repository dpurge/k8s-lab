// dp-card: a surface for grouping content.
//
//   <dp-card heading="Tasks">...anything...</dp-card>
//   <dp-card interactive value="42">...</dp-card>
//
// Attributes
//   heading      optional title, drawn as an <h2> above the content
//   interactive  the whole card can be activated: it dispatches dp-card-select
//   value        an opaque string sent in the dp-card-select event (an id, say)
//
// Slots
//   (default)    the content; page CSS styles it, because it stays in the page
//
// Events
//   dp-card-select  detail { value }; bubbles, composed, cancelable. Sent when
//                   an interactive card is clicked on its body (not on a link,
//                   button or form control, and not while text is selected) or
//                   activated with Enter or Space. If nobody calls
//                   preventDefault(), the card then clicks the link marked
//                   [data-open] inside it, if there is one, so a whole tile can
//                   open the item while the real link stays for keyboard,
//                   middle-click and screen readers.
//
// Parts: card, heading
//
// CSS custom properties (each falls back to a token from tokens.css, then to a
// literal, so the card also works in an app that does not load the tokens).
// An interactive card shows it can be clicked by highlighting its border, not
// its background, which would swallow the surface-coloured badges inside it:
//   --dp-card-bg, --dp-card-border-color, --dp-card-radius, --dp-card-padding,
//   --dp-card-shadow, --dp-card-heading-size, --dp-card-heading-gap,
//   --dp-card-hover-border-color, --dp-card-focus-color
//
// An interactive card with no [data-open] link inside is made focusable
// (tabindex 0, role button); one with a link is not, so Tab does not stop twice.
import { LitElement, html, nothing, unsafeCSS } from "lit";
import styles from "./component.css";

const CONTROLS = "a, button, input, select, textarea, label, summary";

export class DpCard extends LitElement {
  static properties = {
    heading: {},
    interactive: { type: Boolean, reflect: true },
    value: {},
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare heading: string;
  declare interactive: boolean;
  declare value: string;

  #ownsFocusability = false;

  constructor() {
    super();
    this.heading = "";
    this.interactive = false;
    this.value = "";
  }

  connectedCallback() {
    super.connectedCallback();
    this.addEventListener("click", this.#onClick);
    this.addEventListener("keydown", this.#onKeydown);
  }

  disconnectedCallback() {
    this.removeEventListener("click", this.#onClick);
    this.removeEventListener("keydown", this.#onKeydown);
    super.disconnectedCallback();
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has("interactive")) this.#syncFocusability();
  }

  render() {
    return html`
      <div class="card" part="card">
        ${this.heading ? html`<h2 class="heading" part="heading">${this.heading}</h2>` : nothing}
        <slot @slotchange=${() => this.#syncFocusability()}></slot>
      </div>
    `;
  }

  #syncFocusability() {
    const needsFocus = this.interactive && !this.querySelector("[data-open]");
    if (needsFocus && !this.hasAttribute("tabindex")) {
      this.tabIndex = 0;
      this.setAttribute("role", "button");
      this.#ownsFocusability = true;
    } else if (!needsFocus && this.#ownsFocusability) {
      this.removeAttribute("tabindex");
      this.removeAttribute("role");
      this.#ownsFocusability = false;
    }
  }

  #onClick = (event: MouseEvent) => {
    if (!this.interactive) return;
    const origin = event.composedPath()[0] as Element | undefined;
    const control = origin?.closest?.(CONTROLS);
    if (control && control !== this && this.contains(control)) return;
    if (this.ownerDocument.defaultView?.getSelection()?.toString()) return;
    this.#activate();
  };

  #onKeydown = (event: KeyboardEvent) => {
    if (!this.interactive || event.target !== this) return;
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    this.#activate();
  };

  #activate() {
    const proceed = this.dispatchEvent(
      new CustomEvent("dp-card-select", {
        detail: { value: this.value },
        bubbles: true,
        composed: true,
        cancelable: true,
      }),
    );
    if (proceed) this.querySelector<HTMLElement>("[data-open]")?.click();
  }
}

customElements.define("dp-card", DpCard);
