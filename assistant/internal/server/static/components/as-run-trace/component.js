// as-run-trace: the trace of a workflow run, one row per attempt of a step: the
// state, what the model chose to do, the outcome, tokens, time, and the error of
// a refused reply or a failed tool, in red. A row opens to show the model's raw
// reply, the state patch and the tool result.
//
// Property: steps, the list GET /api/v1/tasks/{id}/steps returns. Attribute
// error: a message to show instead of the table (the trace could not be loaded).
(function () {
  const STYLE_ID = "as-run-trace-style";
  const COLUMNS = ["Step", "State", "Action", "Outcome", "Tokens in / out", "Time", "Error"];

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-run-trace/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // describeAction is one line saying what the model chose to do.
  function describeAction(step) {
    const a = step.action;
    if (!a) return step.outcome === "failed" ? "run failed" : "no valid action";
    if (a.type === "tool") return `tool ${a.tool}${a.args && a.args.query ? ` "${a.args.query}"` : ""}`;
    if (a.type === "goto") return `go to ${a.state}`;
    if (a.type === "ask_user") return "ask the user";
    return String(a.type);
  }

  function detail(step) {
    const box = el("div", "as-trace-detail");
    const part = (title, text) => {
      if (text === null || text === undefined || text === "") return;
      box.append(el("h4", "", title), el("pre", "", text));
    };
    part("Model reply", step.reply);
    part("State patch", step.patch ? JSON.stringify(step.patch, null, 2) : "");
    part("Tool result", step.tool_result);
    part("Action", step.action ? JSON.stringify(step.action, null, 2) : "");
    if (!box.children.length) box.append(el("p", "as-trace-none", "Nothing more was recorded for this attempt."));
    return box;
  }

  class AsRunTrace extends HTMLElement {
    static get observedAttributes() {
      return ["error"];
    }

    get steps() {
      return this._steps || [];
    }

    set steps(value) {
      this._steps = Array.isArray(value) ? value : [];
      if (this._built) this._render();
    }

    connectedCallback() {
      ensureStyle();
      this._build();
    }

    attributeChangedCallback() {
      if (this._built) this._render();
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._message = el("p", "as-trace-message");
      this._message.setAttribute("role", "status");
      this._wrap = el("div", "as-trace-table-wrap");
      const table = el("table", "as-trace-table");
      const head = document.createElement("tr");
      for (const name of COLUMNS) head.appendChild(el("th", "", name));
      const thead = document.createElement("thead");
      thead.appendChild(head);
      this._body = document.createElement("tbody");
      table.append(thead, this._body);
      this._wrap.appendChild(table);
      this.append(this._message, this._wrap);
      this._render();
    }

    _render() {
      const f = window.asFormat;
      const error = this.getAttribute("error");
      this._body.textContent = "";
      this._message.classList.toggle("error", !!error);
      if (error) {
        this._message.textContent = error;
        this._wrap.hidden = true;
        return;
      }
      this._wrap.hidden = this.steps.length === 0;
      this._message.textContent = this.steps.length === 0 ? "No steps yet." : "";
      for (const step of this.steps) {
        const tr = document.createElement("tr");
        tr.className = `as-trace-row as-trace-${step.outcome}`;
        tr.dataset.stepId = String(step.id);
        const label = step.attempt > 1 ? `${step.step}.${step.attempt}` : step.attempt === 0 ? `${step.step}!` : String(step.step);
        const outcome = document.createElement("td");
        outcome.appendChild(el("span", `as-badge as-outcome-${step.outcome}`, step.outcome));
        const tokens = step.attempt === 0 ? f.DASH : `${f.tokens(step.input_tokens)} / ${f.tokens(step.output_tokens)}`;
        const error = el("td", step.error ? "as-trace-error has-error" : "as-trace-error", step.error || f.DASH);
        tr.append(
          el("td", "as-trace-step", label),
          el("td", "as-trace-state", step.state),
          el("td", "as-trace-action", describeAction(step)),
          outcome,
          el("td", "as-trace-tokens", tokens),
          el("td", "as-trace-time", step.attempt === 0 ? f.DASH : f.duration(step.duration_ms / 1000)),
          error,
        );
        const open = el("tr", "as-trace-more");
        open.hidden = true;
        const cell = document.createElement("td");
        cell.colSpan = COLUMNS.length;
        cell.appendChild(detail(step));
        open.appendChild(cell);
        tr.addEventListener("click", () => {
          open.hidden = !open.hidden;
          tr.classList.toggle("as-trace-open", !open.hidden);
        });
        tr.tabIndex = 0;
        tr.addEventListener("keydown", (e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            tr.click();
          }
        });
        // A step that went wrong is shown open: its reply is what one needs.
        if (step.error) {
          open.hidden = false;
          tr.classList.add("as-trace-open");
        }
        this._body.append(tr, open);
      }
    }
  }

  customElements.define("as-run-trace", AsRunTrace);
})();
