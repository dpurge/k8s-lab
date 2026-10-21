// as-task-detail: one task with its payload, result and error. It loads the
// task by the task-id attribute and refreshes itself while the task is pending
// or running.
//
// Attributes: task-id, refresh-ms (default 3000; 0 turns refreshing off).
// Event (bubbling): as-load {ok, message, live} after every load.
(function () {
  const STYLE_ID = "as-task-detail-style";
  const ACTIVE = new Set(["pending", "running"]);
  const DEFAULT_REFRESH_MS = 3000;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-task-detail/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsTaskDetail extends HTMLElement {
    static get observedAttributes() {
      return ["task-id", "refresh-ms"];
    }

    get taskId() {
      return this.getAttribute("task-id") || "";
    }

    get refreshMs() {
      const raw = this.getAttribute("refresh-ms");
      if (raw === null) return DEFAULT_REFRESH_MS;
      const n = Number(raw);
      return Number.isFinite(n) && n >= 0 ? n : DEFAULT_REFRESH_MS;
    }

    connectedCallback() {
      ensureStyle();
      this._seq = this._seq || 0;
      this._users = this._users || new Map();
      this._built = true;
      if (!this._onAnswered) {
        // An accepted answer queued the run again: look at the task at once.
        this._onAnswered = () => this._load();
        this.addEventListener("as-answered", this._onAnswered);
        // A cancel changed the task too.
        this.addEventListener("as-task-changed", this._onAnswered);
      }
      this._load();
    }

    disconnectedCallback() {
      clearTimeout(this._timer);
      this._seq = (this._seq || 0) + 1;
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      if (name === "refresh-ms") {
        clearTimeout(this._timer);
        this._schedule(this._lastActive, true);
      } else {
        this._load();
      }
    }

    async _load() {
      clearTimeout(this._timer);
      const seq = ++this._seq;
      if (!this.taskId) {
        // Nothing to ask for yet; the host sets task-id.
        this.textContent = "";
        return;
      }
      this.setAttribute("aria-busy", "true");
      let active = false;
      let ok = true;
      try {
        const task = await window.asApi.getJSON(`/api/v1/tasks/${encodeURIComponent(this.taskId)}`);
        if (seq !== this._seq) return;
        if (!this._users.has(task.user_id)) {
          try {
            const users = await window.asApi.loadUsers();
            if (seq === this._seq) this._users = users;
          } catch (e) {
            /* the user's id is shown instead of the name */
          }
        }
        if (seq !== this._seq) return;
        const [calls, steps] = await Promise.all([this._loadCalls(task), this._loadSteps(task)]);
        if (seq !== this._seq) return;
        this._render(task, calls, steps);
        active = ACTIVE.has(task.status);
      } catch (err) {
        if (seq !== this._seq) return;
        ok = false;
        this._renderError(err.message);
      }
      this.removeAttribute("aria-busy");
      this._lastActive = active;
      this._schedule(active, ok);
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok, message: ok ? "" : this._errorText, live: !!this._live, at: new Date() }, bubbles: true }));
    }

    // _loadCalls fetches the task's model calls, once it has started: a task
    // still waiting in the queue has made none. A failure is shown in the
    // section, not as a failed page.
    async _loadCalls(task) {
      if (!task.started_at) return { items: [] };
      try {
        return await window.asApi.getJSON(`/api/v1/tasks/${encodeURIComponent(task.id)}/llm-calls`);
      } catch (err) {
        return { error: err.message };
      }
    }

    // _loadSteps fetches the trace of the task's run, once it has started. As
    // with the calls, a failure is shown where the trace would be.
    async _loadSteps(task) {
      if (!task.started_at) return { items: [] };
      try {
        return await window.asApi.getJSON(`/api/v1/tasks/${encodeURIComponent(task.id)}/steps`);
      } catch (err) {
        return { error: err.message };
      }
    }

    _schedule(active, ok) {
      clearTimeout(this._timer);
      this._live = false;
      // A task that cannot be found will not appear by waiting; any other
      // failure may be temporary.
      if (this.refreshMs > 0 && this.isConnected && (active || (!ok && !this._notFound))) {
        this._live = true;
        this._timer = setTimeout(() => this._load(), this.refreshMs);
      }
    }

    _renderError(message) {
      this._notFound = /not found/i.test(message);
      this._errorText = this._notFound ? "Task not found." : `Could not load task: ${message}`;
      this.textContent = "";
      this.append(this._back(), el("p", "as-task-detail-error", this._errorText));
    }

    _back() {
      const back = document.createElement("a");
      back.className = "as-task-detail-back";
      back.href = "#/tasks";
      back.textContent = "← Back to tasks";
      return back;
    }

    _render(task, calls, steps) {
      this._notFound = false;
      const f = window.asFormat;
      this.textContent = "";

      const list = document.createElement("dl");
      list.className = "as-task-detail-fields";
      const add = (name, value, className, title) => {
        const dt = el("dt", "", name);
        const dd = el("dd", className || "", value);
        if (title) dd.title = title;
        list.append(dt, dd);
      };
      add("ID", task.id, "as-task-detail-id");
      add("User", this._users.get(task.user_id) || window.asApi.shortId(task.user_id), "as-task-detail-user", task.user_id);
      add("Workflow", task.workflow_id);
      add("Source", task.source);
      const statusDd = el("dd", "");
      statusDd.appendChild(el("span", `as-badge as-status-${task.status}`, task.status));
      list.append(el("dt", "", "Status"), statusDd);
      add("Created", f.time(task.created_at), "", task.created_at);
      add("Started", f.time(task.started_at), "", task.started_at || "");
      add("Finished", f.time(task.finished_at), "", task.finished_at || "");
      add("Queue time", f.duration(task.queue_seconds) + (task.status === "pending" ? "…" : ""), task.status === "pending" ? "as-live" : "");
      add("Execution time", f.duration(task.execution_seconds) + (task.status === "running" ? "…" : ""), task.status === "running" ? "as-live" : "");

      const usage = task.usage || {};
      if (usage.calls > 0) {
        add("Model", (usage.models || []).join(", "), "as-task-detail-model");
        add("Tokens", `${f.tokens(usage.input_tokens)} in / ${f.tokens(usage.output_tokens)} out`, "as-task-detail-tokens");
        add("Cost", f.cost(usage.cost_usd), "as-task-detail-cost");
      }

      this.append(this._back());
      const actions = document.createElement("as-task-actions");
      actions.task = task;
      this.append(actions);
      // What went wrong, or what is wanted from the user, comes before the details.
      if (task.status === "failed" && task.error !== null && task.error !== undefined) {
        const banner = el("section", "as-task-detail-failure");
        banner.setAttribute("role", "alert");
        banner.append(el("h3", "", "This task failed"), el("pre", "as-task-detail-error", String(task.error)));
        this.append(banner);
      }
      if (task.status === "waiting") {
        const box = document.createElement("as-answer-box");
        box.setAttribute("task-id", task.id);
        box.ask = task.ask;
        this.append(box);
      }
      this.append(list);
      if (task.run) this.append(this._runSection(task.run));
      this.append(this._section("Payload", task.payload));
      this.append(this._section("Result", task.result));
      if ((task.run && task.run !== null) || (steps && (steps.error || (steps.items || []).length > 0))) {
        this.append(this._traceSection(steps));
      }
      this.append(this._callsSection(calls));
    }

    // _runSection shows where the run is and what it has recorded.
    _runSection(run) {
      const section = el("section", "as-task-detail-section as-task-detail-run");
      section.appendChild(el("h3", "", "Run"));
      const facts = [`State: ${run.state_name}`, `Run status: ${run.status}`, `Steps: ${run.steps}`, `Tokens: ${window.asFormat.tokens(run.tokens)}`];
      section.appendChild(el("p", "as-task-detail-run-facts", facts.join(" · ")));
      section.appendChild(el("pre", "as-task-detail-json", JSON.stringify(run.state, null, 2)));
      return section;
    }

    // _traceSection shows every step attempt of the run.
    _traceSection(steps) {
      const section = el("section", "as-task-detail-section as-task-detail-trace");
      section.appendChild(el("h3", "", "Trace"));
      const trace = document.createElement("as-run-trace");
      if (steps && steps.error) trace.setAttribute("error", `Could not load the trace: ${steps.error}`);
      trace.steps = (steps && steps.items) || [];
      section.appendChild(trace);
      return section;
    }

    // _callsSection lists every attempt to call a model, so a fallback shows
    // the failed attempt next to the one that answered.
    _callsSection(calls) {
      const f = window.asFormat;
      const section = el("section", "as-task-detail-section as-task-detail-calls");
      section.appendChild(el("h3", "", "Model calls"));
      if (calls && calls.error) {
        section.appendChild(el("p", "as-task-detail-error", `Could not load model calls: ${calls.error}`));
        return section;
      }
      const items = (calls && calls.items) || [];
      if (items.length === 0) {
        section.appendChild(el("p", "as-task-detail-none", "No model calls."));
        return section;
      }
      const wrap = el("div", "as-task-detail-table-wrap");
      const table = document.createElement("table");
      table.className = "as-task-detail-calls-table";
      const head = document.createElement("tr");
      for (const name of ["Model", "Tags", "Input", "Output", "Cost", "Duration", "Outcome", "Error"]) head.appendChild(el("th", "", name));
      const thead = document.createElement("thead");
      thead.appendChild(head);
      const body = document.createElement("tbody");
      for (const c of items) {
        const tr = document.createElement("tr");
        const model = el("td", "as-call-model", c.model_id);
        model.title = c.model_name || "";
        const outcome = document.createElement("td");
        outcome.appendChild(el("span", `as-badge as-outcome-${c.outcome}`, c.outcome));
        tr.append(
          model,
          el("td", "as-call-tags", (c.tags || []).join(", ") || f.DASH),
          el("td", "as-call-input", f.tokens(c.input_tokens)),
          el("td", "as-call-output", f.tokens(c.output_tokens)),
          el("td", "as-call-cost", f.cost(c.cost_usd)),
          el("td", "as-call-duration", f.duration(c.duration_ms / 1000)),
          outcome,
          el("td", c.error ? "as-call-error has-error" : "as-call-error", c.error || f.DASH),
        );
        body.appendChild(tr);
      }
      table.append(thead, body);
      wrap.appendChild(table);
      section.appendChild(wrap);
      return section;
    }

    _section(title, value) {
      const section = el("section", "as-task-detail-section");
      section.appendChild(el("h3", "", title));
      if (value === null || value === undefined) {
        section.appendChild(el("p", "as-task-detail-none", window.asFormat.DASH));
      } else {
        section.appendChild(el("pre", "as-task-detail-json", JSON.stringify(value, null, 2)));
      }
      return section;
    }
  }

  customElements.define("as-task-detail", AsTaskDetail);
})();
