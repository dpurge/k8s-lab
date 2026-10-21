// dp-a2ui-video: the A2UI basic catalog's Video, a native <video> with controls.
// The address and the poster image are checked against the media policy; a
// blocked one draws a note that says why. Nothing plays by itself.
import { html, nothing, unsafeCSS } from "lit";
import { ifDefined } from "lit/directives/if-defined.js";
import { DpA2uiElement } from "../../base.ts";
import { checkMediaUrl } from "../../media.ts";
import styles from "./component.css";

type Props = { url?: string; posterUrl?: string };

export class DpA2uiVideo extends DpA2uiElement {
  static tagName = "dp-a2ui-video";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const video = checkMediaUrl(props.url, "video");
    if (!video.ok) return html`<div class="note" role="img" aria-label="Video not shown">Video not shown: ${video.reason}</div>`;
    const poster = props.posterUrl ? checkMediaUrl(props.posterUrl, "image") : undefined;
    return html`<video
        controls
        preload="metadata"
        playsinline
        src=${video.url}
        poster=${ifDefined(poster?.ok ? poster.url : undefined)}
        referrerpolicy="no-referrer"
      ></video>
      ${poster && !poster.ok ? html`<div class="caption">Poster image not shown: ${poster.reason}</div>` : nothing}`;
  }
}

customElements.define(DpA2uiVideo.tagName, DpA2uiVideo);
