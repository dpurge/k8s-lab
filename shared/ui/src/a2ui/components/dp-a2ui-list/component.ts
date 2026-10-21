// dp-a2ui-list: the A2UI basic catalog's List. Children come from a fixed
// array or from a template over a data-model list; web_core expands both into
// child references.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { ALIGN, renderChildren } from "../../layout.ts";
import styles from "./component.css";

type Props = {
  children?: unknown;
  direction?: "vertical" | "horizontal";
  align?: keyof typeof ALIGN;
};

export class DpA2uiList extends DpA2uiElement {
  static tagName = "dp-a2ui-list";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const direction = props.direction === "horizontal" ? "horizontal" : "vertical";
    const align = props.align && props.align in ALIGN ? props.align : "stretch";
    const cell = (content: unknown) => html`<div class="item">${content}</div>`;
    return html`<div class="plain ${direction} align-${align}">${renderChildren(props.children, (child) => this.renderNode(child), cell)}</div>`;
  }
}

customElements.define(DpA2uiList.tagName, DpA2uiList);
