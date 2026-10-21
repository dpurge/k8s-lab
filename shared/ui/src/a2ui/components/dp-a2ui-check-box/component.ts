// dp-a2ui-check-box: the A2UI basic catalog's CheckBox, a native checkbox with
// its label, bound two ways to a boolean.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { controlId, inputStyles, isInvalid, renderErrors, type InputProps } from "../../inputs.ts";

type Props = InputProps & { label?: string; value?: boolean; setValue?: (value: boolean) => void };

export class DpA2uiCheckBox extends DpA2uiElement {
  static tagName = "dp-a2ui-check-box";
  static styles = inputStyles;
  #id = controlId();

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    return html`<label class="row" for=${this.#id}>
        <input
          id=${this.#id}
          type="checkbox"
          aria-invalid=${isInvalid(props) ? "true" : "false"}
          .checked=${props.value === true}
          @change=${(event: Event) => props.setValue?.((event.target as HTMLInputElement).checked)}
        />
        <span>${props.label ?? ""}</span>
      </label>
      ${renderErrors(props)}`;
  }
}

customElements.define(DpA2uiCheckBox.tagName, DpA2uiCheckBox);
