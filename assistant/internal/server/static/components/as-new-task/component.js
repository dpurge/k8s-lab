// as-new-task: a form that queues a task, for trying workflows by hand: pick the
// user, name the workflow, give the payload as JSON. A refused task (unknown
// user, bad payload, a server error) shows its message right under the button.
//
// Event (bubbling): as-task-created {task} after the server accepted the task.
(function () {
  const STYLE_ID = "as-new-task-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-new-task/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function labelled(text, control) {
    const label = el("label", "as-new-task-field");
    label.append(el("span", "", text), control);
    return label;
  }

  class AsNewTask extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      this._build();
      this._loadUsers();
    }

    disconnectedCallback() {
      this._seq = (this._seq || 0) + 1;
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;

      this._user = document.createElement("select");
      this._user.className = "as-new-task-user";
      this._workflow = document.createElement("select");
      this._workflow.className = "as-new-task-workflow";
      this._workflows = new Map();
      // Picking a workflow puts a payload that works into the box.
      this._workflow.addEventListener("change", () => this._showWorkflow());
      this._about = el("p", "as-new-task-about");
      this._payload = document.createElement("textarea");
      this._payload.className = "as-new-task-payload";
      this._payload.rows = 2;
      this._payload.spellcheck = false;

      this._button = el("button", "as-new-task-start", "Start task");
      this._button.type = "button";
      this._button.addEventListener("click", () => this._start());
      this._error = el("p", "as-new-task-error");
      this._error.setAttribute("role", "alert");
      this._error.hidden = true;

      const row = el("div", "as-new-task-row");
      row.append(labelled("User", this._user), labelled("Workflow", this._workflow));
      this.append(row, this._about, labelled("Payload (JSON)", this._payload), this._button, this._error);
    }

    _fail(message) {
      this._error.textContent = message;
      this._error.hidden = false;
    }

    // _showWorkflow describes the chosen workflow and offers a payload for it.
    _showWorkflow() {
      const info = this._workflows.get(this._workflow.value);
      this._about.textContent = info ? info.description : "";
      if (info) this._payload.value = info.payload_hint;
    }

    async _loadWorkflows() {
      const seq = this._seq;
      try {
        const body = await window.asApi.getJSON("/api/v1/workflows");
        if (seq !== this._seq) return;
        this._workflow.textContent = "";
        this._workflows = new Map();
        for (const info of body.items) {
          this._workflows.set(info.id, info);
          const option = document.createElement("option");
          option.value = info.id;
          option.textContent = info.id;
          this._workflow.appendChild(option);
        }
        // hello is the one to try first, if it is there.
        if (this._workflows.has("hello")) this._workflow.value = "hello";
        this._showWorkflow();
        if (body.items.length === 0) this._fail("The server lists no workflows.");
      } catch (err) {
        if (seq !== this._seq) return;
        this._fail(`Could not load the workflows: ${err.message}`);
      }
    }

    async _loadUsers() {
      const seq = ++this._seq;
      this._loadWorkflows();
      try {
        const users = await window.asApi.loadUsers();
        if (seq !== this._seq) return;
        this._user.textContent = "";
        for (const [id, name] of users) {
          const option = document.createElement("option");
          option.value = id;
          option.textContent = name;
          this._user.appendChild(option);
        }
        if (users.size === 0) this._fail("There are no users yet: create one through POST /api/v1/users.");
      } catch (err) {
        if (seq !== this._seq) return;
        this._fail(`Could not load the users: ${err.message}`);
      }
    }

    async _start() {
      if (this._sending) return;
      this._error.hidden = true;
      this._error.textContent = "";
      const workflow = this._workflow.value;
      if (!this._user.value) return this._fail("Pick a user.");
      if (workflow === "") return this._fail("Pick a workflow.");
      let payload;
      try {
        payload = JSON.parse(this._payload.value);
      } catch (err) {
        return this._fail(`The payload is not valid JSON: ${err.message}`);
      }
      if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return this._fail("The payload must be a JSON object.");

      this._sending = true;
      this._button.disabled = true;
      try {
        const res = await fetch("/api/v1/tasks", {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ user_id: this._user.value, workflow_id: workflow, payload }),
        });
        if (!res.ok) return this._fail(`Could not start the task: ${await window.asApi.errorMessage(res)}`);
        const task = await res.json();
        this.dispatchEvent(new CustomEvent("as-task-created", { detail: { task }, bubbles: true }));
      } catch (err) {
        this._fail(`Could not start the task: ${err.message}`);
      } finally {
        this._sending = false;
        this._button.disabled = false;
      }
    }
  }

  customElements.define("as-new-task", AsNewTask);
})();
