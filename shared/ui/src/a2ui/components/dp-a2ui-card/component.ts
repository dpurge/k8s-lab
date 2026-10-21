// dp-a2ui-card: the A2UI basic catalog's Card, drawn with the library's dp-card.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";

export class DpA2uiCard extends DpA2uiElement {
  static tagName = "dp-a2ui-card";

  render() {
    const props = this.controller.props as { child?: Parameters<DpA2uiCard["renderNode"]>[0] } | undefined;
    if (!props) return nothing;
    return html`<dp-card>${props.child ? this.renderNode(props.child) : nothing}</dp-card>`;
  }
}

customElements.define(DpA2uiCard.tagName, DpA2uiCard);
