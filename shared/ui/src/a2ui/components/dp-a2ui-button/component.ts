// dp-a2ui-button: the A2UI basic catalog's Button, drawn with dp-button. A click
// runs the component's action (an event sent to the agent, or a function call
// run here); a failed `checks` rule disables it. `borderless` has no dp-button
// look yet and is drawn as the default one.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import type { InputProps } from "../../inputs.ts";

type Props = InputProps & { child?: Parameters<DpA2uiButton["renderNode"]>[0]; variant?: string; action?: () => void };

export class DpA2uiButton extends DpA2uiElement {
  static tagName = "dp-a2ui-button";

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const disabled = props.isValid === false;
    return html`<dp-button
      variant=${props.variant === "primary" ? "primary" : ""}
      ?disabled=${disabled}
      title=${disabled && props.validationErrors?.length ? props.validationErrors.join(" ") : ""}
      @click=${() => {
        if (!disabled) props.action?.();
      }}
      >${props.child ? this.renderNode(props.child) : nothing}</dp-button
    >`;
  }
}

customElements.define(DpA2uiButton.tagName, DpA2uiButton);
