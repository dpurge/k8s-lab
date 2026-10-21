// dp-a2ui-text-field: the A2UI basic catalog's TextField. The control is a
// native <input> or <textarea> in the page's own DOM (inside a dp-field), so
// input methods work. The data model is updated on `input`, except while an
// input method is composing: then only when the composition ends, so a
// half-composed character is never written to the model.
import { html, nothing } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { controlId, inputStyles, isInvalid, renderErrors, type InputProps } from "../../inputs.ts";

type Props = InputProps & {
  label?: string;
  value?: string;
  placeholder?: string;
  variant?: "shortText" | "longText" | "number" | "obscured";
  setValue?: (value: string) => void;
};

const TYPES = { shortText: "text", number: "number", obscured: "password" } as const;

export class DpA2uiTextField extends DpA2uiElement {
  static tagName = "dp-a2ui-text-field";
  static styles = inputStyles;
  #id = controlId();

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const onInput = (event: InputEvent) => {
      if (event.isComposing) return;
      props.setValue?.((event.target as HTMLInputElement).value);
    };
    const onCompositionEnd = (event: Event) => props.setValue?.((event.target as HTMLInputElement).value);
    const common = {
      id: this.#id,
      placeholder: props.placeholder ?? "",
      invalid: isInvalid(props) ? "true" : "false",
    };
    return html`<dp-field label=${props.label ?? ""} for=${this.#id}>
        ${props.variant === "longText"
          ? html`<textarea
              id=${common.id}
              placeholder=${common.placeholder}
              aria-invalid=${common.invalid}
              .value=${props.value ?? ""}
              @input=${onInput}
              @compositionend=${onCompositionEnd}
            ></textarea>`
          : html`<input
              id=${common.id}
              type=${TYPES[props.variant as keyof typeof TYPES] ?? "text"}
              placeholder=${common.placeholder}
              aria-invalid=${common.invalid}
              .value=${props.value ?? ""}
              @input=${onInput}
              @compositionend=${onCompositionEnd}
            />`}
      </dp-field>
      ${renderErrors(props)}`;
  }
}

customElements.define(DpA2uiTextField.tagName, DpA2uiTextField);
