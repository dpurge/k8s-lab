// The base of every A2UI catalog component.
//
// web_core's A2uiLitElement resolves the component's properties (data-model
// bindings, function calls, templates) into `controller.props`, which redraws
// the element when something it depends on changes. This base adds the two
// things all components share:
//
//  - It renders in the light DOM, so native form controls stay in the page's
//    own DOM (the library's IME rule) and page CSS reaches the content.
//    A2uiLitElement scopes `static styles` to the tag name and adopts them on
//    the document, so a component's CSS lives in its component.css as usual.
//  - `weight` (flex-grow inside a Row or Column) is applied to the host.
//  - `accessibility` (label, description, live, hidden) becomes ARIA
//    attributes on the host, bound values included. A component with an
//    implicit label (a Button's title) does not need one; an explicit label
//    replaces it.
import { A2uiLitElement } from "@a2ui/web_core/v1_0";
import { html, type PropertyValues } from "lit";
import type { Catalog, ComponentModel } from "@a2ui/web_core/v1_0";

type Accessibility = { label?: unknown; description?: unknown; live?: unknown; hidden?: unknown };

export const isUnknownType = (model: ComponentModel) => !(model.catalog as Catalog<never>).components.has(model.type);

export const unknownComponent = (type: string) =>
  html`<span class="dp-a2ui-unknown" role="note">Unknown component “${type}”</span>`;

export abstract class DpA2uiElement extends A2uiLitElement {
  createRenderRoot() {
    return this;
  }

  // A child whose type the catalog does not have draws a visible note, not
  // nothing, so a malformed interface shows where it is wrong (the client has
  // already reported it to the agent).
  renderNode(childRef?: Parameters<A2uiLitElement["renderNode"]>[0], customPath?: string) {
    const id = typeof childRef === "object" ? childRef.id : childRef;
    const model = id ? this.context?.dataContext.surface?.componentsModel.get(id) : undefined;
    if (model && isUnknownType(model)) return unknownComponent(model.type);
    return super.renderNode(childRef, customPath);
  }

  willUpdate(changed: PropertyValues) {
    super.willUpdate(changed);
    const props = this.controller?.props as { weight?: unknown; accessibility?: Accessibility } | undefined;
    if (typeof props?.weight === "number") this.style.flex = String(props.weight);
    else this.style.removeProperty("flex");
    this.#applyAccessibility(props?.accessibility);
  }

  #applyAccessibility(a: Accessibility | undefined) {
    const set = (name: string, value: string | undefined) =>
      value === undefined ? this.removeAttribute(name) : this.setAttribute(name, value);
    set("aria-label", typeof a?.label === "string" && a.label ? a.label : undefined);
    set("aria-description", typeof a?.description === "string" && a.description ? a.description : undefined);
    set("aria-live", a?.live === "polite" || a?.live === "assertive" ? a.live : undefined);
    set("aria-hidden", a?.hidden === true ? "true" : undefined);
  }
}
