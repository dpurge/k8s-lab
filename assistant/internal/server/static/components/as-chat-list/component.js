// as-chat-list: the conversations, the most recently active first, and a form to
// start a new one (who, which workflow, an optional title). Each conversation is
// a link to its page; the one named by the active attribute is marked. It loads
// its own data and refreshes itself, so a reply that arrives shows in the list.
//
// Attributes: active (a conversation id), refresh-ms (default 4000; 0 off).
// Events (bubbling): as-conversation-created {conversation}, as-load {ok,
// message, live, at}.
(function () {
  const STYLE_ID = "as-chat-list-style";
  const DEFAULT_REFRESH_MS = 4000;
  // A conversation can run a YAML workflow, or ask.
  const conversational = (w) => w.kind === "yaml" || w.id === "ask";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-chat-list/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // clip squeezes a message onto one line of at most max characters.
  function clip(text, max) {
    const line = String(text).replace(/\s+/g, " ").trim();
    return line.length > max ? `${line.slice(0, max - 1)}…` : line;
  }

  class AsChatList extends HTMLElement {
    static get observedAttributes() {
      return ["active", "refresh-ms"];
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
      this._loadForm();
      this._load();
    }

    disconnectedCallback() {
      clearTimeout(this._timer);
      this._seq = (this._seq || 0) + 1;
      this._formSeq = (this._formSeq || 0) + 1;
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      if (name === "active") this._mark();
      else {
        clearTimeout(this._timer);
        this._schedule();
      }
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      this._formSeq = 0;
      this._users = new Map();

      const form = el("div", "as-chat-new");
      form.appendChild(el("h3", "", "New conversation"));
      this._user = document.createElement("select");
      this._user.className = "as-chat-new-user";
      this._workflow = document.createElement("select");
      this._workflow.className = "as-chat-new-workflow";
      this._title = document.createElement("input");
      this._title.className = "as-chat-new-title";
      this._title.placeholder = "Title (optional)";
      this._start = el("button", "as-chat-new-start", "Start");
      this._start.type = "button";
      this._start.addEventListener("click", () => this._create());
      this._formError = el("p", "as-chat-new-error");
      this._formError.setAttribute("role", "alert");
      this._formError.hidden = true;
      form.append(this._user, this._workflow, this._title, this._start, this._formError);

      this._items = el("ul", "as-chat-items");
      this._message = el("p", "as-chat-list-message");
      this._message.setAttribute("role", "status");
      this.append(form, this._items, this._message);
    }

    _fail(message) {
      this._formError.textContent = message;
      this._formError.hidden = false;
    }

    async _loadForm() {
      const seq = ++this._formSeq;
      try {
        const [users, workflows] = await Promise.all([window.asApi.loadUsers(), window.asApi.getJSON("/api/v1/workflows")]);
        if (seq !== this._formSeq) return;
        this._users = users;
        this._user.textContent = "";
        for (const [id, name] of users) {
          const option = document.createElement("option");
          option.value = id;
          option.textContent = name;
          this._user.appendChild(option);
        }
        this._workflow.textContent = "";
        const offered = workflows.items.filter(conversational);
        for (const w of offered) {
          const option = document.createElement("option");
          option.value = w.id;
          option.textContent = w.id;
          option.title = w.description;
          this._workflow.appendChild(option);
        }
        if (offered.some((w) => w.id === "hello-live")) this._workflow.value = "hello-live";
        else if (offered.some((w) => w.id === "hello")) this._workflow.value = "hello";
        if (users.size === 0) this._fail("There are no users yet: create one through POST /api/v1/users.");
        else if (offered.length === 0) this._fail("The server offers no workflow a conversation can run.");
        if (this._last) this._render(this._last);
      } catch (err) {
        if (seq !== this._formSeq) return;
        this._fail(`Could not load the form: ${err.message}`);
      }
    }

    async _create() {
      if (this._creating) return;
      this._formError.hidden = true;
      this._formError.textContent = "";
      if (!this._user.value) return this._fail("Pick a user.");
      if (!this._workflow.value) return this._fail("Pick a workflow.");
      this._creating = true;
      this._start.disabled = true;
      try {
        const res = await fetch("/api/v1/conversations", {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ user_id: this._user.value, workflow_id: this._workflow.value, title: this._title.value.trim() }),
        });
        if (!res.ok) return this._fail(`Could not start the conversation: ${await window.asApi.errorMessage(res)}`);
        const conversation = await res.json();
        this._title.value = "";
        this.dispatchEvent(new CustomEvent("as-conversation-created", { detail: { conversation }, bubbles: true }));
        this._load();
      } catch (err) {
        this._fail(`Could not start the conversation: ${err.message}`);
      } finally {
        this._creating = false;
        this._start.disabled = false;
      }
    }

    async _load() {
      clearTimeout(this._timer);
      const seq = ++this._seq;
      let failure = "";
      try {
        const body = await window.asApi.getJSON("/api/v1/conversations");
        if (seq !== this._seq) return;
        this._last = body.items;
        this._render(body.items);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = `Could not load the conversations: ${err.message}`;
        this._message.textContent = failure;
        this._message.classList.add("error");
      }
      this._schedule();
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok: failure === "", message: failure, live: !!this._live, at: new Date() }, bubbles: true }));
    }

    _schedule() {
      clearTimeout(this._timer);
      this._live = false;
      if (this.refreshMs > 0 && this.isConnected) {
        this._live = true;
        this._timer = setTimeout(() => this._load(), this.refreshMs);
      }
    }

    _mark() {
      const active = this.getAttribute("active") || "";
      for (const li of this._items.children) li.classList.toggle("active", li.dataset.conversationId === active);
    }

    _render(items) {
      this._message.classList.remove("error");
      this._message.textContent = items.length === 0 ? "No conversations yet: start one above." : "";
      this._items.textContent = "";
      for (const c of items) {
        const li = document.createElement("li");
        li.className = "as-chat-item";
        li.dataset.conversationId = c.id;
        const link = document.createElement("a");
        link.href = `#/chat/${encodeURIComponent(c.id)}`;
        link.className = "as-chat-item-link";
        const title = el("span", "as-chat-item-title", c.title || c.workflow_id);
        const meta = el("span", "as-chat-item-meta", `${c.title ? `${c.workflow_id} · ` : ""}${this._users.get(c.user_id) || window.asApi.shortId(c.user_id)}`);
        const last = el("span", "as-chat-item-last", c.last_text ? clip(c.last_text, 90) : "(nothing said yet)");
        link.append(title, meta, last);
        if (c.task_status && c.task_status !== "succeeded") link.appendChild(el("span", `as-badge as-status-${c.task_status}`, c.task_status));
        li.appendChild(link);
        this._items.appendChild(li);
      }
      this._mark();
    }
  }

  customElements.define("as-chat-list", AsChatList);
})();
