// dp-attempt-table: every attempt to call a model, as the host lists them
// (a page at a time, newest first): when, who, which provider and model, how it
// ended, tokens, cost and how long it took. It only draws the rows it is given.
//
//   table.rows = [{ startedAt: "2026-10-16T12:00:00Z", user: "Ada", provider: "gpu",
//                   model: "gemma3:12b", outcome: "ok", error: null, inputTokens: 900,
//                   outputTokens: 120, costUsd: 0, durationMs: 1500 }];
//
// An outcome other than "ok" is drawn as a failure; the error text, if any, is
// the badge's tooltip.
//
// Properties
//   rows       the rows
//   emptyText  shown instead of the table when there are no rows
//   labels     column headings to replace, by key: started, user, provider, model,
//              outcome, inputTokens, outputTokens, cost, duration
//
// Parts: table, empty
import { LitElement, html, nothing, unsafeCSS } from "lit";
import tableStyles from "../../table.css";
import { compactTime, cost, duration, tokens } from "../../format.ts";

export interface AttemptRow {
  startedAt: string;
  user: string;
  userId?: string;
  provider: string;
  model: string;
  outcome: string;
  error: string | null;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  durationMs: number;
}

export const ATTEMPT_LABELS = {
  started: "Started",
  user: "User",
  provider: "Provider",
  model: "Model",
  outcome: "Outcome",
  inputTokens: "Input tokens",
  outputTokens: "Output tokens",
  cost: "Cost",
  duration: "Duration",
};

export class DpAttemptTable extends LitElement {
  static properties = {
    rows: { attribute: false },
    emptyText: { attribute: "empty-text" },
    labels: { attribute: false },
  };

  // Narrower than the usage table: nine columns share the room, so names are shorter.
  static styles = unsafeCSS(tableStyles + ".name { max-width: 11rem; }");

  declare rows: AttemptRow[];
  declare emptyText: string;
  declare labels: Partial<typeof ATTEMPT_LABELS>;

  constructor() {
    super();
    this.rows = [];
    this.emptyText = "";
    this.labels = {};
  }

  render() {
    if (!this.rows.length) return this.emptyText ? html`<p class="empty" part="empty" role="status">${this.emptyText}</p>` : nothing;
    const l = { ...ATTEMPT_LABELS, ...this.labels };
    return html`<div class="wrap">
      <table part="table">
        <thead>
          <tr>
            <th>${l.started}</th><th>${l.user}</th><th>${l.provider}</th><th>${l.model}</th><th>${l.outcome}</th>
            <th class="num">${l.inputTokens}</th><th class="num">${l.outputTokens}</th><th class="num">${l.cost}</th><th class="num">${l.duration}</th>
          </tr>
        </thead>
        <tbody>
          ${this.rows.map(
            (r) => html`<tr>
              <td title=${r.startedAt}>${compactTime(r.startedAt)}</td>
              <td class="name" title=${r.userId ?? nothing}>${r.user}</td>
              <td>${r.provider}</td>
              <td class="name" title=${r.model}>${r.model}</td>
              <td><span class="badge ${r.outcome === "ok" ? "good" : "bad"}" title=${r.error ?? nothing}>${r.outcome}</span></td>
              <td class="num">${tokens(r.inputTokens)}</td>
              <td class="num">${tokens(r.outputTokens)}</td>
              <td class="num">${cost(r.costUsd)}</td>
              <td class="num">${duration(r.durationMs)}</td>
            </tr>`,
          )}
        </tbody>
      </table>
    </div>`;
  }
}

customElements.define("dp-attempt-table", DpAttemptTable);
