// dp-model-table: the configured models and whether each can take calls now:
// provider, model, tags, priority and availability. It only draws the rows it
// is given, in the order given; the host sorts them.
//
//   table.rows = [{ provider: "gpu", model: "gemma3:12b", tags: ["chat"], priority: 20,
//                   state: "available" }];
//
// state is "available", "held_out" (the circuit breaker holds it out; until is
// when it is tried again) or "no_credentials".
//
// Properties
//   rows       the rows
//   emptyText  shown instead of the table when there are no rows
//   labels     column headings and availability words to replace, by key:
//              provider, model, tags, priority, availability, available,
//              heldOut, noCredentials, until (the word before the return time)
//
// Parts: table, empty
import { LitElement, html, nothing, unsafeCSS } from "lit";
import tableStyles from "../../table.css";
import { DASH, fullTime } from "../../format.ts";

export type ModelState = "available" | "held_out" | "no_credentials";

export interface ModelRow {
  provider: string;
  model: string;
  tags: string[];
  priority: number;
  state: ModelState;
  until?: string;
}

export const MODEL_LABELS = {
  provider: "Provider",
  model: "Model",
  tags: "Tags",
  priority: "Priority",
  availability: "Availability",
  available: "available",
  heldOut: "held out",
  noCredentials: "no credentials",
  until: "until",
};

export class DpModelTable extends LitElement {
  static properties = {
    rows: { attribute: false },
    emptyText: { attribute: "empty-text" },
    labels: { attribute: false },
  };

  static styles = unsafeCSS(tableStyles + "td.wrap-cell { white-space: normal; } .until { color: var(--muted, #6b7280); }");

  declare rows: ModelRow[];
  declare emptyText: string;
  declare labels: Partial<typeof MODEL_LABELS>;

  constructor() {
    super();
    this.rows = [];
    this.emptyText = "";
    this.labels = {};
  }

  render() {
    if (!this.rows.length) return this.emptyText ? html`<p class="empty" part="empty" role="status">${this.emptyText}</p>` : nothing;
    const l = { ...MODEL_LABELS, ...this.labels };
    const badge = (r: ModelRow) => {
      const word = r.state === "available" ? l.available : r.state === "held_out" ? l.heldOut : l.noCredentials;
      return html`<span class="badge ${r.state === "available" ? "good" : "bad"}" data-state=${r.state}>${word}</span>${r.state === "held_out" && r.until
        ? html` <span class="until" title=${r.until}>${l.until} ${fullTime(r.until)}</span>`
        : nothing}`;
    };
    return html`<div class="wrap">
      <table part="table">
        <thead>
          <tr><th>${l.provider}</th><th>${l.model}</th><th>${l.tags}</th><th class="num">${l.priority}</th><th>${l.availability}</th></tr>
        </thead>
        <tbody>
          ${this.rows.map(
            (r) => html`<tr>
              <td>${r.provider}</td>
              <td class="name" title=${r.model}>${r.model}</td>
              <td class="wrap-cell">${r.tags.length ? r.tags.join(", ") : DASH}</td>
              <td class="num">${r.priority}</td>
              <td class="wrap-cell">${badge(r)}</td>
            </tr>`,
          )}
        </tbody>
      </table>
    </div>`;
  }
}

customElements.define("dp-model-table", DpModelTable);
