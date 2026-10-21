// dp-button: a styled <button> in a shadow root. It looks like phraseforge's
// pf-button (variants: primary, danger, icon; no variant is the secondary
// look) and takes its colours from the tokens in tokens.css. The styles are
// in component.css.
//
// A button inside a shadow root is not part of the page's <form>: it does not
// submit it, and without a submit button of the form's own the Enter key in a
// field does nothing when the form has more than one field. So a type="submit"
// host keeps a hidden real <button type="submit"> as a child, in the form's DOM
// (it is not shown, and it follows `disabled`): Enter in a field activates it,
// and a click on the visible button asks the form to submit with it.
//
//   <dp-button variant="primary" type="submit" block>Save</dp-button>
//
// Attributes: variant (primary | danger | icon; none is the secondary look),
//   type (button | submit), disabled, block (fill the container's width),
//   aria-label (forwarded to the inner button).
// Slots: (default) the label. Parts: button.
// CSS: reads the --border, --surface-alt, --text, --btn-bg, --btn-bg-hover,
//   --danger, --danger-hover and --link tokens, each with a literal fallback.
//   --dp-button-padding and --dp-button-font-size make a smaller button, for
//   example in a table row (not for the icon variant, which has its own size).
import { LitElement, html, unsafeCSS } from "lit";
import { ifDefined } from "lit/directives/if-defined.js";
import styles from "./component.css";

export class DpButton extends LitElement {
  static properties = {
    variant: { reflect: true },
    type: {},
    disabled: { type: Boolean, reflect: true },
    block: { type: Boolean, reflect: true },
    ariaLabelText: { attribute: "aria-label" },
  };

  // focus() on the host, and Tab, reach the real button inside.
  static shadowRootOptions = { ...LitElement.shadowRootOptions, delegatesFocus: true };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare variant: string;
  declare type: string;
  declare disabled: boolean;
  declare block: boolean;
  declare ariaLabelText: string | null;

  constructor() {
    super();
    this.variant = "";
    this.type = "button";
    this.disabled = false;
    this.block = false;
    this.ariaLabelText = null;
  }

  render() {
    return html`
      <button
        part="button"
        type="button"
        ?disabled=${this.disabled}
        aria-label=${ifDefined(this.ariaLabelText ?? undefined)}
        @click=${this.#submitForm}
      >
        <slot></slot>
      </button>
    `;
  }

  #submitForm() {
    if (this.type === "submit") this.closest("form")?.requestSubmit(this.#submitProxy ?? undefined);
  }

  connectedCallback() {
    super.connectedCallback();
    this.#childObserver.observe(this, { childList: true });
    this.#syncSubmitProxy();
  }

  disconnectedCallback() {
    this.#childObserver.disconnect();
    super.disconnectedCallback();
  }

  updated() {
    this.#syncSubmitProxy();
  }

  // A page that sets textContent on the host removes the proxy, so it is put
  // back when the children change. Nothing changes when it is already right,
  // so this cannot loop.
  #childObserver = new MutationObserver(() => this.#syncSubmitProxy());
  #submitProxy: HTMLButtonElement | null = null;

  #syncSubmitProxy() {
    if (this.type !== "submit") {
      this.#submitProxy?.remove();
      this.#submitProxy = null;
      return;
    }
    if (!this.#submitProxy || this.#submitProxy.parentElement !== this) {
      const proxy = this.ownerDocument.createElement("button");
      proxy.type = "submit";
      proxy.hidden = true;
      proxy.tabIndex = -1;
      proxy.setAttribute("aria-hidden", "true");
      proxy.setAttribute("slot", "dp-submit"); // no such slot: it is never drawn
      this.append(proxy);
      this.#submitProxy = proxy;
    }
    this.#submitProxy.disabled = this.disabled;
  }
}

customElements.define("dp-button", DpButton);
