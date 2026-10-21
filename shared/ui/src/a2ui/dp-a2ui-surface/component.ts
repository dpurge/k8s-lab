// dp-a2ui-surface: draws one A2UI surface.
//
//   const processor = createProcessor(onMessageToAgent);
//   processor.processMessages(messagesFromTheAgent);
//   surfaceElement.surface = processor.getSurface("my-surface");
//
// It renders the surface's "root" component with the dp-* catalog, in the
// light DOM (see base.ts). The processor owns the data model and the component
// models; this element only follows the root: it draws again when the root is
// created, replaced or deleted, and every component below it follows its own
// bindings.
import { ComponentContext, type Catalog, type SurfaceModel, type WebComponentImplementation, renderA2uiNode } from "@a2ui/web_core/v1_0";
import { isUnknownType, unknownComponent } from "../base.ts";
import { LitElement, html, nothing, type PropertyValues } from "lit";

type Surface = SurfaceModel<WebComponentImplementation>;

export class DpA2uiSurface extends LitElement {
  static properties = {
    surface: { attribute: false },
    hasRoot: { state: true },
  };

  declare surface: Surface | undefined;
  declare hasRoot: boolean;

  #unsubscribe: (() => void) | undefined;

  constructor() {
    super();
    this.hasRoot = false;
  }

  createRenderRoot() {
    return this;
  }

  connectedCallback() {
    super.connectedCallback();
    if (this.surface && !this.#unsubscribe) this.#followRoot();
  }

  disconnectedCallback() {
    this.#unsubscribe?.();
    this.#unsubscribe = undefined;
    super.disconnectedCallback();
  }

  willUpdate(changed: PropertyValues) {
    if (changed.has("surface")) this.#followRoot();
  }

  // The processor replaces a component's model when an update changes its type,
  // so the subscription outlives the first creation of the root.
  #followRoot() {
    this.#unsubscribe?.();
    this.#unsubscribe = undefined;
    const components = this.surface?.componentsModel;
    this.hasRoot = !!components?.get("root");
    if (!components) return;
    const created = components.onCreated.subscribe((component) => {
      if (component.id === "root") this.hasRoot = true;
    });
    const deleted = components.onDeleted.subscribe((id) => {
      if (id === "root") this.hasRoot = !!components.get("root");
    });
    this.#unsubscribe = () => {
      created.unsubscribe();
      deleted.unsubscribe();
    };
  }

  render() {
    if (!this.surface || !this.hasRoot) return nothing;
    try {
      const context = new ComponentContext(this.surface, "root", "/");
      if (isUnknownType(context.componentModel)) return unknownComponent(context.componentModel.type);
      return renderA2uiNode(context, context.componentModel.catalog as Catalog<WebComponentImplementation>);
    } catch (error) {
      return html`<div class="dp-a2ui-error" role="alert">This interface could not be drawn: ${String(error)}</div>`;
    }
  }
}

customElements.define("dp-a2ui-surface", DpA2uiSurface);
