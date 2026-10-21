// dp-a2ui-slider: the A2UI basic catalog's Slider, a native range input bound
// two ways to a number. `steps` divides the range into that many equal steps.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { controlId, inputStyles, isInvalid, renderErrors, type InputProps } from "../../inputs.ts";

type Props = InputProps & {
  label?: string;
  min?: number;
  max?: number;
  steps?: number;
  value?: number;
  setValue?: (value: number) => void;
};

export class DpA2uiSlider extends DpA2uiElement {
  static tagName = "dp-a2ui-slider";
  static styles = inputStyles;
  #id = controlId();

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const min = props.min ?? 0;
    const max = props.max ?? 100;
    const value = typeof props.value === "number" ? props.value : min;
    const step = props.steps && props.steps > 0 ? (max - min) / props.steps : "any";
    return html`<dp-field label=${props.label ?? ""} for=${this.#id}>
        <div class="slider">
          <input
            id=${this.#id}
            type="range"
            min=${min}
            max=${max}
            step=${step}
            aria-invalid=${isInvalid(props) ? "true" : "false"}
            .value=${String(value)}
            @input=${(event: Event) => props.setValue?.(Number((event.target as HTMLInputElement).value))}
          />
          <output for=${this.#id}>${value}</output>
        </div>
      </dp-field>
      ${renderErrors(props)}`;
  }
}

customElements.define(DpA2uiSlider.tagName, DpA2uiSlider);
