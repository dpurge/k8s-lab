// as-task-list: the task queue as a table, newest first, with a status filter
// and paging. It loads its own data from the JSON API and refreshes itself
// while any task shown is pending or running, so waiting and running tasks
// count up.
//
// Attributes: status (filter; empty means all), page (1-based), page-size
// (default 25), refresh-ms (default 3000; 0 turns refreshing off).
// Events (bubbling): as-list-change {status, page} when the user picks a
// filter or a page, as-load {ok, message, live} after every load. It never
// changes its own attributes: the host decides what a change means.
(function () {
  const STYLE_ID = "as-task-list-style";
  const STATUSES = ["pending", "running", "waiting", "succeeded", "failed", "cancelled"];
  const ACTIVE = new Set(["pending", "running"]);
  const DEFAULT_PAGE_SIZE = 25;
  const DEFAULT_REFRESH_MS = 3000;
  const COLUMNS = ["ID", "User", "Workflow", "Source", "Status", "Created", "Queue time", "Execution time", "Model", "Tokens in / out", "Cost"];

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-task-list/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // clip shortens a text to its first line and at most max characters.
  function clip(text, max) {
    const first = String(text).split("\n")[0];
    return first.length > max ? `${first.slice(0, max - 1)}…` : first;
  }

  // questionOf is the readable part of an ask_user payload.
  function questionOf(ask) {
    for (const key of ["question", "text", "prompt", "message"]) {
      if (typeof ask[key] === "string" && ask[key].trim() !== "") return ask[key];
    }
    return "waiting for an answer";
  }

  class AsTaskList extends HTMLElement {
    static get observedAttributes() {
      return ["status", "page", "page-size", "refresh-ms"];
    }

    get status() {
      const v = this.getAttribute("status") || "";
      return STATUSES.includes(v) ? v : "";
    }

    get page() {
      const n = parseInt(this.getAttribute("page"), 10);
      return n >= 1 ? n : 1;
    }

    get pageSize() {
      const n = parseInt(this.getAttribute("page-size"), 10);
      return n >= 1 && n <= 100 ? n : DEFAULT_PAGE_SIZE;
    }

    get refreshMs() {
      const raw = this.getAttribute("refresh-ms");
      if (raw === null) return DEFAULT_REFRESH_MS;
      const n = Number(raw);
      return Number.isFinite(n) && n >= 0 ? n : DEFAULT_REFRESH_MS;
    }

    connectedCallback() {
      ensureStyle();
      this._build();
      this._load();
    }

    disconnectedCallback() {
      clearTimeout(this._timer);
      clearTimeout(this._armTimer);
      this._seq = (this._seq || 0) + 1; // a response arriving later is ignored
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      if (name === "status") this._select.value = this.status;
      if (name === "refresh-ms") {
        clearTimeout(this._timer);
        this._schedule(this._lastActive);
      } else {
        this._load();
      }
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      this._users = new Map();
      this._lastActive = false;

      const controls = el("div", "as-task-list-controls");
      const label = el("label", "as-task-list-filter");
      label.append(el("span", "", "Status "));
      const select = document.createElement("select");
      select.className = "as-task-list-status";
      for (const value of [""].concat(STATUSES)) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = value === "" ? "All" : value;
        select.appendChild(option);
      }
      select.addEventListener("change", () => {
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { status: select.value, page: 1 }, bubbles: true }));
      });
      label.append(select);
      this._summary = el("span", "as-task-list-summary");
      // Removing all the tasks of one finished status: two clicks, the first arms.
      this._clear = el("button", "as-task-list-clear");
      this._clear.type = "button";
      this._clear.hidden = true;
      this._clear.addEventListener("click", () => this._clearClicked());
      controls.append(label, this._summary, this._clear);

      const wrap = el("div", "as-task-list-table-wrap");
      const table = document.createElement("table");
      table.className = "as-task-list-table";
      const head = document.createElement("thead");
      const headRow = document.createElement("tr");
      for (const name of COLUMNS) headRow.appendChild(el("th", "", name));
      head.appendChild(headRow);
      this._body = document.createElement("tbody");
      table.append(head, this._body);
      wrap.appendChild(table);
      this._wrap = wrap;

      this._message = el("p", "as-task-list-message");
      this._message.setAttribute("role", "status");
      this._pager = document.createElement("dp-pager");
      this._pager.addEventListener("dp-pager-select", (e) => {
        e.stopPropagation();
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { status: this.status, page: e.detail.page }, bubbles: true }));
      });

      this._select = select;
      this.append(controls, wrap, this._message, this._pager);
      select.value = this.status;
    }

    // _updateClear shows the bulk remove button for a finished status filter.
    _updateClear(total) {
      const finished = ["succeeded", "failed", "cancelled"].includes(this.status);
      this._clear.hidden = !finished || total === 0;
      this._total = total;
      this._clear.classList.toggle("armed", !!this._armed);
      this._clear.textContent = this._armed ? `Click again to remove ${total} ${this.status} tasks` : `Remove all ${total} ${this.status} tasks`;
    }

    async _clearClicked() {
      if (!this._armed) {
        this._armed = true;
        this._updateClear(this._total);
        this._armTimer = setTimeout(() => {
          this._armed = false;
          this._updateClear(this._total);
        }, 4000);
        return;
      }
      clearTimeout(this._armTimer);
      this._armed = false;
      this._clear.disabled = true;
      try {
        const res = await fetch(`/api/v1/tasks?status=${encodeURIComponent(this.status)}`, { method: "DELETE", headers: { Accept: "application/json" } });
        if (!res.ok) {
          this._message.textContent = `Could not remove the tasks: ${await window.asApi.errorMessage(res)}`;
          this._message.classList.add("error");
          return;
        }
        await this._load();
      } catch (err) {
        this._message.textContent = `Could not remove the tasks: ${err.message}`;
        this._message.classList.add("error");
      } finally {
        this._clear.disabled = false;
      }
    }

    async _load() {
      clearTimeout(this._timer);
      const seq = ++this._seq;
      this.setAttribute("aria-busy", "true");
      let active = false;
      let failure = "";
      try {
        const query = new URLSearchParams({ limit: String(this.pageSize), offset: String((this.page - 1) * this.pageSize) });
        if (this.status) query.set("status", this.status);
        const list = await window.asApi.getJSON(`/api/v1/tasks?${query}`);
        if (seq !== this._seq) return;
        await this._resolveUsers(list.items, seq);
        if (seq !== this._seq) return;
        active = this._render(list);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = `Could not load tasks: ${err.message}`;
        this._message.textContent = failure;
        this._message.classList.add("error");
      }
      this.removeAttribute("aria-busy");
      this._lastActive = active;
      this._schedule(active, failure === "");
      // After scheduling, so the event can say whether a refresh is coming.
      this._emit(failure === "", failure);
    }

    // _resolveUsers fetches the users once, and again only when a task names a
    // user not seen yet. A failure leaves short ids in place of names.
    async _resolveUsers(tasks, seq) {
      if (!tasks.some((t) => !this._users.has(t.user_id))) return;
      try {
        const users = await window.asApi.loadUsers();
        if (seq === this._seq) this._users = users;
      } catch (e) {
        /* tasks are still shown, with the user's id */
      }
    }

    _schedule(active, ok = true) {
      clearTimeout(this._timer);
      this._live = false;
      if (this.refreshMs > 0 && this.isConnected && (active || !ok)) {
        this._live = true;
        this._timer = setTimeout(() => this._load(), this.refreshMs);
      }
    }

    _emit(ok, message) {
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok, message, live: !!this._live, at: new Date() }, bubbles: true }));
    }

    // _render shows one page and returns whether any task on it is pending or
    // running.
    _render(list) {
      const { items, total } = list;
      this._message.classList.remove("error");
      this._message.textContent = "";
      this._body.textContent = "";
      const pageCount = Math.max(1, Math.ceil(total / this.pageSize));

      if (items.length === 0 && total > 0 && this.page > 1) {
        // The page asked for is beyond the end (rows were removed, or a filter
        // narrowed the list): ask the host for the last page.
        this.dispatchEvent(new CustomEvent("as-list-change", { detail: { status: this.status, page: pageCount }, bubbles: true }));
      }

      this._wrap.hidden = items.length === 0;
      if (items.length === 0) {
        this._message.textContent = this.status ? `No tasks with status ${this.status}.` : "No tasks yet.";
      }
      this._summary.textContent = total === 1 ? "1 task" : `${total} tasks`;
      this._updateClear(total);
      this._pager.setAttribute("page", String(this.page));
      this._pager.setAttribute("page-count", String(pageCount));

      let active = false;
      for (const task of items) {
        if (ACTIVE.has(task.status)) active = true;
        this._body.appendChild(this._row(task));
      }
      return active;
    }

    _row(task) {
      const f = window.asFormat;
      const tr = document.createElement("tr");
      tr.dataset.taskId = task.id;

      const id = document.createElement("td");
      const link = document.createElement("a");
      link.className = "as-task-id";
      link.href = `#/tasks/${encodeURIComponent(task.id)}`;
      link.title = task.id;
      link.textContent = window.asApi.shortId(task.id);
      id.appendChild(link);

      const user = el("td", "as-task-user", this._users.get(task.user_id) || window.asApi.shortId(task.user_id));
      user.title = task.user_id;

      const status = document.createElement("td");
      status.appendChild(el("span", `as-badge as-status-${task.status}`, task.status));
      // A failure and a question are shown where they are seen, not behind a click.
      if (task.status === "failed" && task.error) {
        const error = el("span", "as-task-error", clip(task.error, 120));
        error.title = task.error;
        status.appendChild(error);
      }
      if (task.status === "waiting" && task.ask) {
        const asked = el("span", "as-task-ask", clip(questionOf(task.ask), 120));
        asked.title = JSON.stringify(task.ask);
        status.appendChild(asked);
      }

      const created = el("td", "as-task-created", f.time(task.created_at, { short: true }));
      created.title = task.created_at;

      // Times still counting up end in an ellipsis.
      const queue = el("td", "as-task-queue", f.duration(task.queue_seconds) + (task.status === "pending" ? "…" : ""));
      const exec = el("td", "as-task-exec", f.duration(task.execution_seconds) + (task.status === "running" ? "…" : ""));
      if (task.status === "pending") queue.classList.add("as-live");
      if (task.status === "running") exec.classList.add("as-live");

      // A task that made no model call has nothing to show for them.
      const usage = task.usage || {};
      const called = usage.calls > 0;
      const model = el("td", "as-task-model", called ? (usage.models || []).join(", ") : f.DASH);
      const tokens = el("td", "as-task-tokens", called ? `${f.tokens(usage.input_tokens)} / ${f.tokens(usage.output_tokens)}` : f.DASH);
      const cost = el("td", "as-task-cost", called ? f.cost(usage.cost_usd) : f.DASH);

      tr.append(id, user, el("td", "as-task-workflow", task.workflow_id), el("td", "as-task-source", task.source), status, created, queue, exec, model, tokens, cost);
      return tr;
    }
  }

  customElements.define("as-task-list", AsTaskList);
})();
