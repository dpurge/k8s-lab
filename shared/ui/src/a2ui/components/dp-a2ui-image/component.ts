// dp-a2ui-image: the A2UI basic catalog's Image. The address is checked against
// the media policy (media.ts) before it reaches an <img>; a blocked or failed
// image draws a note that says why, never a broken-image icon.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { checkMediaUrl } from "../../media.ts";
import styles from "./component.css";

type Props = { url?: string; description?: string; fit?: string; variant?: string };

const FITS = { contain: "contain", cover: "cover", fill: "fill", none: "none", scaleDown: "scale-down" } as const;
const VARIANTS = ["icon", "avatar", "smallFeature", "mediumFeature", "largeFeature", "header"];

export class DpA2uiImage extends DpA2uiElement {
  static properties = { failedUrl: { state: true } };
  static tagName = "dp-a2ui-image";
  static styles = unsafeCSS(styles);
  declare failedUrl: string;

  constructor() {
    super();
    this.failedUrl = "";
  }

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const variant = VARIANTS.includes(props.variant ?? "") ? props.variant : "mediumFeature";
    const fit = FITS[props.fit as keyof typeof FITS] ?? "fill";
    const checked = checkMediaUrl(props.url, "image");
    const label = props.description ?? "";
    if (!checked.ok) return html`<div class="note ${variant}" role="img" aria-label=${label || "Image not shown"}>Image not shown: ${checked.reason}</div>`;
    if (this.failedUrl === checked.url) return html`<div class="note ${variant}" role="img" aria-label=${label || "Image not shown"}>Image could not be loaded</div>`;
    return html`<img
      class="${variant} fit-${fit}"
      src=${checked.url}
      alt=${label}
      referrerpolicy="no-referrer"
      loading="lazy"
      decoding="async"
      @error=${() => (this.failedUrl = checked.url)}
    />`;
  }
}

customElements.define(DpA2uiImage.tagName, DpA2uiImage);
