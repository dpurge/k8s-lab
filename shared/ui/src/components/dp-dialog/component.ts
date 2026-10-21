// dp-dialog: a modal dialog on the browser's native <dialog>, so it traps
// focus, closes on Escape, sits above everything and makes the page behind it
// inert. One instance lives in the page and is reused.
//
//   <dp-dialog id="dialog"></dp-dialog>
//   if (await dialog.confirm("Delete this item?", { danger: true })) remove();
//   await dialog.showContent(formElement, { okLabel: "Export", wide: true });
//
// Methods (the API the apps' own dialogs already had)
//   confirm(message, options)      show a plain-text message; resolves true for
//                                  OK, false for Cancel, Escape or the backdrop
//   showContent(node, options)     show a node of your own (a form, a table);
//                                  same result. The node is placed in this
//                                  element's light DOM, so it stays the page's
//                                  own: its form controls, ids, scripts, styles
//                                  and input methods work as anywhere else.
//                                  Build it with the DOM API, not from a string
//                                  of untrusted HTML.
//   close(confirmed = false)       close it from code
//   Opening a dialog that is already open answers the earlier call with false.
//
// options: okLabel ("OK"), cancelLabel ("Cancel"), danger (red OK button, focus
//   starts on Cancel), wide (a wide, resizable box), hideCancel (a view with
//   nothing to decide).
//
// Attributes
//   heading  optional title above the message or content
//   open     reflects whether it is open; setting it opens or closes it
//
// Events
//   dp-dialog-close  detail { confirmed }; bubbles, composed. After it closes.
//
// Parts: dialog, box, heading, message, content, actions
//
// CSS custom properties: --dp-dialog-bg, --dp-dialog-color, --dp-dialog-border-color,
//   --dp-dialog-radius, --dp-dialog-padding, --dp-dialog-max-width (28rem),
//   --dp-dialog-wide-width (64rem), --dp-dialog-backdrop.
import { LitElement, html, nothing, unsafeCSS } from "lit";
import "../dp-button/component.ts";
import styles from "./component.css";

export interface DialogOptions {
  okLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  wide?: boolean;
  hideCancel?: boolean;
}

const FOCUSABLE = "a[href], button, input, select, textarea, [tabindex]:not([tabindex='-1'])";

export class DpDialog extends LitElement {
  static properties = {
    heading: {},
    open: { type: Boolean, reflect: true },
    _message: { state: true },
    _options: { state: true },
    _hasContent: { state: true },
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare heading: string;
  declare open: boolean;
  declare _message: string;
  declare _options: DialogOptions;
  declare _hasContent: boolean;

  #resolve: ((confirmed: boolean) => void) | null = null;
  #pending: boolean | null = null;
  #pressStartedOnBackdrop = false;

  constructor() {
    super();
    this.heading = "";
    this.open = false;
    this._message = "";
    this._options = {};
    this._hasContent = false;
  }

  confirm(message: string, options: DialogOptions = {}): Promise<boolean> {
    this.replaceChildren();
    return this.#show(message, options);
  }

  showContent(node: Node, options: DialogOptions = {}): Promise<boolean> {
    this.replaceChildren(node);
    return this.#show("", options);
  }

  close(confirmed = false) {
    if (!this.open) return;
    this.#pending = confirmed;
    this.open = false;
  }

  #show(message: string, options: DialogOptions): Promise<boolean> {
    this.#resolve?.(false);
    this._message = message;
    this._options = options;
    const answer = new Promise<boolean>((resolve) => {
      this.#resolve = resolve;
    });
    this.open = true;
    return answer;
  }

  updated(changed: Map<string, unknown>) {
    if (!changed.has("open")) return;
    const dialog = this.#dialog;
    if (this.open && !dialog.open) {
      dialog.showModal();
      this.#focusDefault();
    } else if (!this.open && dialog.open) {
      dialog.close();
    }
  }

  get #dialog(): HTMLDialogElement {
    return this.renderRoot.querySelector("dialog")!;
  }

  // With content of the page's own, the browser has already put focus on its
  // first control, which is what a form wants. A plain message starts on the
  // safe button: Cancel for a dangerous action, OK otherwise.
  #focusDefault() {
    if (this.querySelector(FOCUSABLE)) return;
    const danger = this._options.danger && !this._options.hideCancel;
    const button = this.renderRoot.querySelector<HTMLElement>(danger ? ".cancel" : ".ok");
    button?.focus();
  }

  // Escape closes the native dialog by itself, so the answer is taken here.
  #onClose = () => {
    const confirmed = this.#pending ?? false;
    this.#pending = null;
    this.open = false;
    const resolve = this.#resolve;
    this.#resolve = null;
    resolve?.(confirmed);
    this.dispatchEvent(new CustomEvent("dp-dialog-close", { detail: { confirmed }, bubbles: true, composed: true }));
  };

  // A click on the backdrop closes it, but not a text selection that began in
  // the box and ended on the backdrop, so the press must start there too.
  #onMouseDown = (event: MouseEvent) => {
    this.#pressStartedOnBackdrop = event.target === event.currentTarget;
  };

  #onClick = (event: MouseEvent) => {
    const onBackdrop = event.target === event.currentTarget && this.#pressStartedOnBackdrop;
    this.#pressStartedOnBackdrop = false;
    if (onBackdrop) this.close(false);
  };

  #onSlotChange = (event: Event) => {
    this._hasContent = (event.target as HTMLSlotElement).assignedNodes({ flatten: true }).length > 0;
  };

  render() {
    const { okLabel = "OK", cancelLabel = "Cancel", danger = false, wide = false, hideCancel = false } = this._options;
    const labelledBy = this.heading ? "heading" : this._message ? "message" : nothing;
    return html`
      <dialog part="dialog" aria-labelledby=${labelledBy} @close=${this.#onClose} @mousedown=${this.#onMouseDown} @click=${this.#onClick}>
        <div class=${wide ? "box wide" : "box"} part="box">
          ${this.heading ? html`<h2 id="heading" class="heading" part="heading">${this.heading}</h2>` : nothing}
          ${this._message ? html`<p id="message" class="message" part="message">${this._message}</p>` : nothing}
          <div class=${this._hasContent ? "content has-content" : "content"} part="content">
            <slot @slotchange=${this.#onSlotChange}></slot>
          </div>
          <div class="actions" part="actions">
            ${hideCancel ? nothing : html`<dp-button class="cancel" variant="secondary" @click=${() => this.close(false)}>${cancelLabel}</dp-button>`}
            <dp-button class="ok" variant=${danger ? "danger" : "primary"} @click=${() => this.close(true)}>${okLabel}</dp-button>
          </div>
        </div>
      </dialog>
    `;
  }
}

customElements.define("dp-dialog", DpDialog);
