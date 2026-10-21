// dp-status-bar: a status line and a message line with a fixed minimum height,
// so a footer does not jump between empty and populated.
//
//   <dp-status-bar id="statusBar"></dp-status-bar>
//   statusBar.setJobStatus("Updated 10:42");
//   statusBar.setMessage("Saved");
//   statusBar.setMessage("Could not save", true);   // an error
//
// Methods (the API the apps' own status bars already had)
//   setJobStatus(text)            the quiet status on the left
//   setMessage(text, isError)     the message; isError colours it as an error
//                                 and announces it as an alert; both clear when
//                                 text is empty
//
// Attributes and properties (the methods set these, so a page can too)
//   status   the status text
//   message  the message text
//   error    the message is an error (reflected as an attribute)
//
// Accessibility: the message is a live region (role status, or alert for an
// error), so a screen reader announces a change without moving focus.
//
// Parts: status, message
//
// CSS custom properties: --dp-status-bar-min-height (2.4rem), --dp-status-bar-gap,
//   --dp-status-bar-status-color (falls back to the --muted token),
//   --dp-status-bar-status-size, --dp-status-bar-message-color,
//   --dp-status-bar-error-color (falls back to the --danger token).
//
// A page script that calls the methods must run after this element is defined:
// load /ui/dp-core.js (a module) before it and give the script `defer`, or wait
// for customElements.whenDefined("dp-status-bar").
import { LitElement, html, unsafeCSS } from "lit";
import styles from "./component.css";

export class DpStatusBar extends LitElement {
  static properties = {
    status: {},
    message: {},
    error: { type: Boolean, reflect: true },
  };

  static styles = unsafeCSS(styles);

  // Declared, not initialised, so the class fields do not shadow Lit's
  // reactive accessors; the defaults are set in the constructor.
  declare status: string;
  declare message: string;
  declare error: boolean;

  constructor() {
    super();
    this.status = "";
    this.message = "";
    this.error = false;
  }

  setJobStatus(text?: string | null) {
    this.status = text || "";
  }

  setMessage(text?: string | null, isError?: boolean) {
    this.message = text || "";
    this.error = !!isError;
  }

  render() {
    return html`
      <span class="status" part="status">${this.status}</span>
      <span class="message" part="message" role=${this.error ? "alert" : "status"}>${this.message}</span>
    `;
  }
}

customElements.define("dp-status-bar", DpStatusBar);
