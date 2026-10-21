// dp-a2ui-audio-player: the A2UI basic catalog's AudioPlayer, a native <audio>
// with controls and its description. The address is checked against the media
// policy; nothing plays by itself.
import { html, nothing, unsafeCSS } from "lit";
import { DpA2uiElement } from "../../base.ts";
import { checkMediaUrl } from "../../media.ts";
import styles from "./component.css";

type Props = { url?: string; description?: string };

export class DpA2uiAudioPlayer extends DpA2uiElement {
  static tagName = "dp-a2ui-audio-player";
  static styles = unsafeCSS(styles);

  render() {
    const props = this.controller.props as Props | undefined;
    if (!props) return nothing;
    const audio = checkMediaUrl(props.url, "audio");
    return html`${props.description ? html`<div class="description">${props.description}</div>` : nothing}
      ${audio.ok
        ? html`<audio controls preload="metadata" src=${audio.url} referrerpolicy="no-referrer"></audio>`
        : html`<div class="note" role="img" aria-label="Audio not shown">Audio not shown: ${audio.reason}</div>`}`;
  }
}

customElements.define(DpA2uiAudioPlayer.tagName, DpA2uiAudioPlayer);
