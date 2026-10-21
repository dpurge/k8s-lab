// dp-field: a label above a native form control.
//
//   <dp-field label="Language" for="language">
//     <select id="language">...</select>
//   </dp-field>
//
// The control stays in the page's own DOM, where the page, its scripts and its
// styles reach it as before (getElementById, name, value, form submission).
// The label is a real <label> in the page's DOM too, because a <label for>
// inside a shadow root cannot point at a control outside it, and the click to
// focus and the control's accessible name would be lost. dp-field creates that
// <label slot="label"> as a child and shows it above the control.
//
// Attributes
//   label  the label text; no label element is made while it is empty
//   for    the id of the control; when absent, the first input, select or
//          textarea inside is used, and given an id if it has none
//
// Slots
//   (default)  the control
//   label      a label of your own (an element with slot="label"); when one is
//              there, dp-field makes none and only fills in its missing `for`
//
// CSS custom properties: --dp-field-label-size, --dp-field-label-color (falls
//   back to the --muted token), --dp-field-label-gap, --dp-field-margin-bottom
//   (default 0, space below the field).
import { LitElement, html, unsafeCSS } from "lit";
import styles from "./component.css";

const CONTROLS = "input, select, textarea";
let nextId = 0;

export class DpField extends LitElement {
  static properties = {
    label: {},
    htmlFor: { attribute: "for" },
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare label: string;
  declare htmlFor: string;

  #generated: HTMLLabelElement | null = null;

  constructor() {
    super();
    this.label = "";
    this.htmlFor = "";
  }

  updated() {
    this.#sync();
  }

  render() {
    return html`
      <slot name="label" @slotchange=${this.#sync}></slot>
      <slot @slotchange=${this.#sync}></slot>
    `;
  }

  // The control can arrive after the field (the parser calls connectedCallback
  // on the opening tag, before the control is parsed), and the label can be
  // removed by the page, so this runs again whenever either slot changes. It
  // changes nothing when nothing is out of date, so it cannot loop.
  #sync() {
    const control = this.querySelector<HTMLElement>(CONTROLS);
    const targetId = this.#targetId(control);
    const authored = Array.from(this.children).find((c) => c.getAttribute("slot") === "label" && c !== this.#generated);

    if (authored) {
      this.#generated?.remove();
      this.#generated = null;
      if (authored instanceof HTMLLabelElement && targetId && !authored.htmlFor) authored.htmlFor = targetId;
      return;
    }
    if (!this.label) {
      this.#generated?.remove();
      this.#generated = null;
      return;
    }

    if (!this.#generated || !this.contains(this.#generated)) {
      const label = this.ownerDocument.createElement("label");
      label.setAttribute("slot", "label");
      this.prepend(label);
      this.#generated = label;
    }
    if (this.#generated.textContent !== this.label) this.#generated.textContent = this.label;
    if (targetId && this.#generated.htmlFor !== targetId) this.#generated.htmlFor = targetId;
  }

  #targetId(control: HTMLElement | null): string {
    if (this.htmlFor) return this.htmlFor;
    if (!control) return "";
    if (!control.id) control.id = `dp-field-${++nextId}`;
    return control.id;
  }
}

customElements.define("dp-field", DpField);
