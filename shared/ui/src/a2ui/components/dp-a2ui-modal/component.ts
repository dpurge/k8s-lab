// dp-a2ui-modal: the A2UI basic catalog's Modal. The trigger component is drawn
// in place; activating it opens the content in a native <dialog> (focus trap,
// Escape, inert page behind it).
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import styles from "./component.css";

type Child = Parameters<DpA2uiModal["renderNode"]>[0];
type Props = { trigger?: Child; content?: Child };

export class DpA2uiModal extends DpA2uiElement {
  static tagName = "dp-a2ui-modal";
  static styles = unsafeCSS(styles);

  #dialog() {
    return this.querySelector("dialog");
  }

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    return html`<span class="trigger" @click=${() => this.#dialog()?.showModal()}>${props.trigger ? this.renderNode(props.trigger) : nothing}</span>
      <dialog
        @click=${(event: Event) => {
          if (event.target === event.currentTarget) this.#dialog()?.close();
        }}
      >
        <div class="box">
          <dp-button class="close" variant="icon" aria-label="Close" @click=${() => this.#dialog()?.close()}>×</dp-button>
          ${props.content ? this.renderNode(props.content) : nothing}
        </div>
      </dialog>`;
  }
}

customElements.define(DpA2uiModal.tagName, DpA2uiModal);
