// as-task-actions: the buttons that tidy the queue, for one task: cancel (a task
// that is pending, waiting or running), retry (a failed or cancelled one: queues
// a copy) and remove (any but a running one; two clicks, the first only arms it).
// A refused action shows the server's message right next to the buttons.
//
// Property: task. Events (bubbling): as-task-changed {task} after a cancel,
// as-task-retried {task} with the new task, as-task-removed {id}.
(function () {
  const STYLE_ID = "as-task-actions-style";
  const CANCELLABLE = new Set(["pending", "waiting", "running"]);
  const RETRYABLE = new Set(["failed", "cancelled"]);
  const ARM_MS = 4000;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-task-actions/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsTaskActions extends HTMLElement {
    get task() {
      return this._task;
    }

    set task(value) {
      this._task = value;
      this._armed = false;
      clearTimeout(this._armTimer);
      if (this._built) this._render();
    }

    connectedCallback() {
      ensureStyle();
      this._build();
    }

    disconnectedCallback() {
      clearTimeout(this._armTimer);
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._buttons = el("div", "as-actions-buttons");
      this._error = el("p", "as-actions-error");
      this._error.setAttribute("role", "alert");
      this._error.hidden = true;
      this.append(this._buttons, this._error);
      this._render();
    }

    _button(label, className, handler) {
      const b = el("button", `as-actions-button ${className}`, label);
      b.type = "button";
      b.addEventListener("click", handler);
      return b;
    }

    _render() {
      this._buttons.textContent = "";
      const t = this._task;
      if (!t) return;
      if (CANCELLABLE.has(t.status)) this._buttons.appendChild(this._button(t.status === "running" ? "Stop" : "Cancel", "as-actions-cancel", () => this._cancel()));
      if (RETRYABLE.has(t.status)) this._buttons.appendChild(this._button("Retry", "as-actions-retry", () => this._retry()));
      if (t.status !== "running") {
        this._remove = this._button(this._armed ? "Click again to remove" : "Remove", `as-actions-remove${this._armed ? " armed" : ""}`, () => this._removeClicked());
        this._buttons.appendChild(this._remove);
      }
    }

    _fail(verb, message) {
      this._error.textContent = `Could not ${verb} the task: ${message}`;
      this._error.hidden = false;
    }

    // _call makes one request and reports a refusal; it returns the response or null.
    async _call(verb, method, path) {
      this._error.hidden = true;
      this._error.textContent = "";
      this._buttons.querySelectorAll("button").forEach((b) => (b.disabled = true));
      try {
        const res = await fetch(path, { method, headers: { Accept: "application/json" } });
        if (!res.ok) {
          this._fail(verb, await window.asApi.errorMessage(res));
          return null;
        }
        return res;
      } catch (err) {
        this._fail(verb, err.message);
        return null;
      } finally {
        this._buttons.querySelectorAll("button").forEach((b) => (b.disabled = false));
      }
    }

    _emit(name, detail) {
      this.dispatchEvent(new CustomEvent(name, { detail, bubbles: true }));
    }

    async _cancel() {
      const res = await this._call("stop", "POST", `/api/v1/tasks/${encodeURIComponent(this._task.id)}/cancel`);
      if (res) this._emit("as-task-changed", { task: await res.json() });
    }

    async _retry() {
      const res = await this._call("retry", "POST", `/api/v1/tasks/${encodeURIComponent(this._task.id)}/retry`);
      if (res) this._emit("as-task-retried", { task: await res.json() });
    }

    async _removeClicked() {
      if (!this._armed) {
        this._armed = true;
        this._render();
        this._armTimer = setTimeout(() => {
          this._armed = false;
          this._render();
        }, ARM_MS);
        return;
      }
      clearTimeout(this._armTimer);
      this._armed = false;
      const res = await this._call("remove", "DELETE", `/api/v1/tasks/${encodeURIComponent(this._task.id)}`);
      this._render();
      if (res) this._emit("as-task-removed", { id: this._task.id });
    }
  }

  customElements.define("as-task-actions", AsTaskActions);
})();
