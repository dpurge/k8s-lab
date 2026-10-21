// dp-a2ui-choice-picker: the A2UI basic catalog's ChoicePicker. Radio buttons
// (mutuallyExclusive) or checkboxes (multipleSelection), as a list or as chips,
// optionally with a text filter. The selection is a list of option values,
// bound two ways.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { controlId, inputStyles, isInvalid, renderErrors, type InputProps } from "../../inputs.ts";

type Option = { label?: string; value: string };
type Props = InputProps & {
  label?: string;
  options?: Option[];
  value?: string[];
  variant?: "multipleSelection" | "mutuallyExclusive";
  displayStyle?: "checkbox" | "chips";
  filterable?: boolean;
  setValue?: (value: string[]) => void;
};

export class DpA2uiChoicePicker extends DpA2uiElement {
  static properties = { filter: { state: true } };
  static tagName = "dp-a2ui-choice-picker";
  static styles = inputStyles;
  declare filter: string;
  #id = controlId();

  constructor() {
    super();
    this.filter = "";
  }

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const multiple = props.variant === "multipleSelection";
    const selected = Array.isArray(props.value) ? props.value : [];
    const needle = this.filter.trim().toLowerCase();
    const options = (props.options ?? []).filter((o) => !needle || String(o.label ?? o.value).toLowerCase().includes(needle));
    const choose = (value: string, on: boolean) => {
      if (!multiple) props.setValue?.(on ? [value] : []);
      else props.setValue?.(on ? [...selected.filter((v) => v !== value), value] : selected.filter((v) => v !== value));
    };
    const name = this.#id;
    return html`<fieldset class="picker ${props.displayStyle === "chips" ? "chips" : "list"}" aria-invalid=${isInvalid(props) ? "true" : "false"}>
        ${props.label ? html`<legend>${props.label}</legend>` : nothing}
        ${props.filterable
          ? html`<input
              type="search"
              class="filter"
              aria-label="Filter the options"
              .value=${this.filter}
              @input=${(event: InputEvent) => {
                if (!event.isComposing) this.filter = (event.target as HTMLInputElement).value;
              }}
              @compositionend=${(event: Event) => (this.filter = (event.target as HTMLInputElement).value)}
            />`
          : nothing}
        <div class="options" role=${multiple ? "group" : "radiogroup"}>
          ${options.map(
            (option) => html`<label class="option ${selected.includes(option.value) ? "on" : ""}">
              <input
                type=${multiple ? "checkbox" : "radio"}
                name=${name}
                value=${option.value}
                .checked=${selected.includes(option.value)}
                @change=${(event: Event) => choose(option.value, (event.target as HTMLInputElement).checked)}
              />
              <span>${option.label ?? option.value}</span>
            </label>`,
          )}
        </div>
      </fieldset>
      ${renderErrors(props)}`;
  }
}

customElements.define(DpA2uiChoicePicker.tagName, DpA2uiChoicePicker);
