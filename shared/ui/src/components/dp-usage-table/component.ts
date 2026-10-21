// dp-usage-table: what was used of each model: who, which provider and model,
// calls, tokens and cost. It only draws the rows it is given; the host loads
// and groups them.
//
//   table.rows = [{ user: "Ada", userId: "…", provider: "gpu", model: "gemma3:12b",
//                   calls: 12, inputTokens: 3400, outputTokens: 560, costUsd: 0 }];
//
// A user, provider or model of null shows a dash.
//
// Properties
//   rows        the rows: one per user, provider and model, the finest grain
//   groupBy     "user", "provider" or "model" sums the rows over the other two
//               (highest cost first); anything else, the default, shows the
//               rows as they are. Rows are told apart by userId when there is
//               one, else by user.
//   emptyText   shown instead of the table when there are no rows
//   labels      column headings to replace, by key: user, provider, model,
//               calls, inputTokens, outputTokens, cost
//
// Parts: table, empty
import { LitElement, html, nothing, unsafeCSS } from "lit";
import tableStyles from "../../table.css";
import { DASH, cost, tokens } from "../../format.ts";

export interface UsageRow {
  user: string | null;
  userId?: string;
  provider: string | null;
  model: string | null;
  calls: number;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

export type UsageGroup = "" | "user" | "provider" | "model";

// grouped sums rows over the fields the grouping leaves out, which become null.
export function grouped(rows: UsageRow[], groupBy: UsageGroup): UsageRow[] {
  if (groupBy !== "user" && groupBy !== "provider" && groupBy !== "model") return rows;
  const sums = new Map<string, UsageRow>();
  for (const r of rows) {
    const key = groupBy === "user" ? String(r.userId ?? r.user ?? "") : groupBy === "provider" ? String(r.provider ?? "") : String(r.model ?? "");
    let sum = sums.get(key);
    if (!sum) {
      sum = {
        user: groupBy === "user" ? r.user : null,
        userId: groupBy === "user" ? r.userId : undefined,
        provider: groupBy === "provider" ? r.provider : null,
        model: groupBy === "model" ? r.model : null,
        calls: 0,
        inputTokens: 0,
        outputTokens: 0,
        costUsd: 0,
      };
      sums.set(key, sum);
    }
    sum.calls += r.calls;
    sum.inputTokens += r.inputTokens;
    sum.outputTokens += r.outputTokens;
    sum.costUsd += r.costUsd;
  }
  return [...sums.values()].sort((a, b) => b.costUsd - a.costUsd);
}

export const USAGE_LABELS = {
  user: "User",
  provider: "Provider",
  model: "Model",
  calls: "Calls",
  inputTokens: "Input tokens",
  outputTokens: "Output tokens",
  cost: "Cost",
};

export class DpUsageTable extends LitElement {
  static properties = {
    rows: { attribute: false },
    groupBy: { attribute: "group-by" },
    emptyText: { attribute: "empty-text" },
    labels: { attribute: false },
  };

  static styles = unsafeCSS(tableStyles);

  declare rows: UsageRow[];
  declare groupBy: UsageGroup;
  declare emptyText: string;
  declare labels: Partial<typeof USAGE_LABELS>;

  constructor() {
    super();
    this.rows = [];
    this.groupBy = "";
    this.emptyText = "";
    this.labels = {};
  }

  render() {
    const rows = grouped(this.rows, this.groupBy);
    if (!rows.length) return this.emptyText ? html`<p class="empty" part="empty" role="status">${this.emptyText}</p>` : nothing;
    const l = { ...USAGE_LABELS, ...this.labels };
    const text = (value: string | null) => (value === null ? DASH : value);
    return html`<div class="wrap">
      <table part="table">
        <thead>
          <tr>
            <th>${l.user}</th><th>${l.provider}</th><th>${l.model}</th>
            <th class="num">${l.calls}</th><th class="num">${l.inputTokens}</th><th class="num">${l.outputTokens}</th><th class="num">${l.cost}</th>
          </tr>
        </thead>
        <tbody>
          ${rows.map(
            (r) => html`<tr>
              <td class="name" title=${r.userId ?? nothing}>${text(r.user)}</td>
              <td>${text(r.provider)}</td>
              <td class="name" title=${r.model ?? nothing}>${text(r.model)}</td>
              <td class="num">${tokens(r.calls)}</td>
              <td class="num">${tokens(r.inputTokens)}</td>
              <td class="num">${tokens(r.outputTokens)}</td>
              <td class="num">${cost(r.costUsd)}</td>
            </tr>`,
          )}
        </tbody>
      </table>
    </div>`;
  }
}

customElements.define("dp-usage-table", DpUsageTable);
