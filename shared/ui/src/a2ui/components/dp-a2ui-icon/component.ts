// dp-a2ui-icon: the A2UI basic catalog's Icon. A catalog name draws its
// Material path (icons.ts); `{svgPath}` draws the agent's own path. Path data
// is only ever the `d` attribute of a <path>, which cannot run script. The icon
// is decorative unless the component has an accessibility label.
import { html, nothing, svg, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { ICONS } from "../../icons.ts";
import styles from "./component.css";

type Props = { name?: string | { svgPath?: string } };

export class DpA2uiIcon extends DpA2uiElement {
  static tagName = "dp-a2ui-icon";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const name = props.name;
    const path = typeof name === "string" ? ICONS[name] : typeof name?.svgPath === "string" ? name.svgPath : undefined;
    if (!path) return html`<span class="missing" role="img" aria-label="Unknown icon"></span>`;
    return html`<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">${svg`<path d=${path}></path>`}</svg>`;
  }
}

customElements.define(DpA2uiIcon.tagName, DpA2uiIcon);
