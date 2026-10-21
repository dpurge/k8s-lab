// dp-a2ui-divider: the A2UI basic catalog's Divider.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import styles from "./component.css";

export class DpA2uiDivider extends DpA2uiElement {
  static tagName = "dp-a2ui-divider";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as { axis?: string } | undefined;
    if (!props) return nothing;
    const vertical = props.axis === "vertical";
    return html`<hr class=${vertical ? "vertical" : "horizontal"} aria-orientation=${vertical ? "vertical" : "horizontal"} />`;
  }
}

customElements.define(DpA2uiDivider.tagName, DpA2uiDivider);
