// What the input components share: a unique id for the native control (so the
// label points at it), and the validation messages `checks` produce.
import { html, nothing, unsafeCSS } from "lit";
import styles from "./inputs.css";

export const inputStyles = unsafeCSS(styles);

let nextId = 0;
export const controlId = () => `dp-a2ui-control-${++nextId}`;

export type InputProps = { isValid?: boolean; validationErrors?: string[] };

// A failed check shows its message under the control; the control itself is
// marked invalid for assistive technologies and for the stylesheet.
export const renderErrors = (props: InputProps) =>
  props.isValid === false && props.validationErrors?.length
    ? html`<div class="errors" role="alert">${props.validationErrors.map((message) => html`<div class="error">${message}</div>`)}</div>`
    : nothing;

export const isInvalid = (props: InputProps) => props.isValid === false;
