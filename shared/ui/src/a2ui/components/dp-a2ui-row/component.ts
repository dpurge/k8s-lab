// dp-a2ui-row: the A2UI basic catalog's Row (children side by side).
import { nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { ALIGN, JUSTIFY, renderChildren } from "../../layout.ts";
import styles from "./component.css";

type Props = { children?: unknown; justify?: keyof typeof JUSTIFY; align?: keyof typeof ALIGN };

export class DpA2uiRow extends DpA2uiElement {
  static tagName = "dp-a2ui-row";
  static styles = unsafeCSS(styles);

  updated(changed: Map<PropertyKey, unknown>) {
    super.updated(changed);
    const props = this.controller.props as Props | undefined;
    this.style.justifyContent = JUSTIFY[props?.justify ?? "start"] ?? "flex-start";
    this.style.alignItems = ALIGN[props?.align ?? "stretch"] ?? "stretch";
  }

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    return renderChildren(props.children, (child) => this.renderNode(child));
  }
}

customElements.define(DpA2uiRow.tagName, DpA2uiRow);
