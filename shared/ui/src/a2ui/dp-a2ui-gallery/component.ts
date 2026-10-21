// dp-a2ui-gallery: every bundled reference example drawn by the renderer, with
// the messages that produced it and what the renderer sent back to the agent.
// A development page: it shows how each component looks in the page's theme and
// at the page's width, and the examples are live (type, click, check).
//
//   <dp-a2ui-gallery></dp-a2ui-gallery>
import { LitElement, html, nothing, unsafeCSS } from "lit";
import { createClient, type ClientMessage } from "../index.ts";
import { examples, type Example } from "../examples.ts";
import styles from "./component.css";

type Entry = { example: Example; client: ReturnType<typeof createClient>; sent: ClientMessage[]; surfaceId: string };

export class DpA2uiGallery extends LitElement {
  static styles = unsafeCSS(styles);

  #entries: Entry[] = [];

  connectedCallback() {
    super.connectedCallback();
    this.#entries = examples.map((example) => {
      const entry: Entry = {
        example,
        sent: [],
        surfaceId: (example.messages[0] as { createSurface?: { surfaceId?: string } }).createSurface?.surfaceId ?? "",
        client: createClient({
          send: (message) => {
            entry.sent = [...entry.sent, message];
            this.requestUpdate();
          },
        }),
      };
      void entry.client.receive(example.messages).then(() => this.requestUpdate());
      return entry;
    });
  }

  render() {
    return html`${this.#entries.map((entry) => this.#renderEntry(entry))}`;
  }

  #renderEntry({ example, client, sent, surfaceId }: Entry) {
    return html`
      <dp-card heading=${example.name}>
        <p class="description">${example.description}</p>
        <div class="pair">
          <div class="drawn">
            <dp-a2ui-surface .surface=${client.processor.getSurface(surfaceId)}></dp-a2ui-surface>
          </div>
          <div class="side">
            <details class="messages">
              <summary>Messages (${example.messages.length})</summary>
              <pre>${JSON.stringify(example.messages, null, 2)}</pre>
            </details>
            <details class="sent" ?open=${sent.length > 0}>
              <summary>Sent to the agent (${sent.length})</summary>
              ${sent.length ? html`<pre>${JSON.stringify(sent, null, 2)}</pre>` : nothing}
            </details>
          </div>
        </div>
      </dp-card>
    `;
  }
}

customElements.define("dp-a2ui-gallery", DpA2uiGallery);
