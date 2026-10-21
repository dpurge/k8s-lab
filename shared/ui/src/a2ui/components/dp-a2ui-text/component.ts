// dp-a2ui-text: the A2UI basic catalog's Text, with the small Markdown the
// catalog allows (markdown.ts). It is drawn as DOM, never parsed as HTML, so
// text from a model cannot add markup or script.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { renderMarkdown } from "../../markdown.ts";
import styles from "./component.css";

export class DpA2uiText extends DpA2uiElement {
  static tagName = "dp-a2ui-text";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as { text?: unknown; variant?: string } | undefined;
    if (!props) return nothing;
    const text = typeof props.text === "string" ? props.text : String(props.text ?? "");
    return html`<div class="text ${props.variant === "caption" ? "caption" : "body"}">${renderMarkdown(text)}</div>`;
  }
}

customElements.define(DpA2uiText.tagName, DpA2uiText);
