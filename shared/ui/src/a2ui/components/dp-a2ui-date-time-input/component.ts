// dp-a2ui-date-time-input: the A2UI basic catalog's DateTimeInput, a native
// date, time or datetime-local input. The model holds ISO 8601 text (a date
// "2026-09-04", a time "14:30", or a date and time "2026-09-04T14:30:00");
// the control holds the browser's local form of the same value.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { controlId, inputStyles, isInvalid, renderErrors, type InputProps } from "../../inputs.ts";

type Props = InputProps & {
  label?: string;
  value?: string;
  min?: string;
  max?: string;
  enableDate?: boolean;
  enableTime?: boolean;
  setValue?: (value: string) => void;
};

// The model's ISO text to what the control accepts, and back.
export const toControl = (iso: string | undefined, type: string) => {
  if (!iso) return "";
  if (type === "date") return iso.slice(0, 10);
  if (type === "time") return iso.includes("T") ? iso.slice(11, 16) : iso.slice(0, 5);
  return iso.slice(0, 16);
};
export const fromControl = (text: string, type: string) => {
  if (!text) return "";
  if (type === "datetime-local") return text.length === 16 ? `${text}:00` : text;
  return text;
};

export class DpA2uiDateTimeInput extends DpA2uiElement {
  static tagName = "dp-a2ui-date-time-input";
  static styles = inputStyles;
  #id = controlId();

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const type = props.enableDate && props.enableTime ? "datetime-local" : props.enableTime ? "time" : "date";
    return html`<dp-field label=${props.label ?? ""} for=${this.#id}>
        <input
          id=${this.#id}
          type=${type}
          min=${toControl(props.min, type)}
          max=${toControl(props.max, type)}
          aria-invalid=${isInvalid(props) ? "true" : "false"}
          .value=${toControl(props.value, type)}
          @change=${(event: Event) => props.setValue?.(fromControl((event.target as HTMLInputElement).value, type))}
        />
      </dp-field>
      ${renderErrors(props)}`;
  }
}

customElements.define(DpA2uiDateTimeInput.tagName, DpA2uiDateTimeInput);
