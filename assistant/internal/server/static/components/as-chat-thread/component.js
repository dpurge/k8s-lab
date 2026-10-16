// as-chat-thread: one conversation: what was said, in order, and a box to say
// more. The assistant's messages come from the workflow run: its questions, its
// result, and its failures (in red). While the run is working the page polls
// for them quickly and the box is closed; when it waits for an answer the next
// message is the answer. Anything that goes wrong is shown, never swallowed.
//
// Attributes: conversation-id, refresh-ms (default 1500 while working, 6000
// otherwise; 0 turns polling off).
// Events (bubbling): as-conversation-removed {id}, as-load {ok, message, live, at}.
(function () {
  const STYLE_ID = "as-chat-thread-style";
  const WORKING = new Set(["pending", "running"]);
  const FAST_MS = 1500;
  const SLOW_MS = 6000;
  const ARM_MS = 4000;
  const ROLE_LABEL = { user: "You", assistant: "Assistant" };
  const STATE_TEXT = {
    pending: "The assistant is starting…",
    running: "The assistant is working…",
    waiting: "The assistant is waiting for your answer.",
    failed: "The last run failed. Your next message starts a new one.",
    cancelled: "The last run was cancelled. Your next message starts a new one.",
    succeeded: "The last run is finished. Your next message starts a new one.",
  };

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/admin/components/as-chat-thread/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  class AsChatThread extends HTMLElement {
    static get observedAttributes() {
      return ["conversation-id", "refresh-ms"];
    }

    get conversationId() {
      return this.getAttribute("conversation-id") || "";
    }

    get refreshOverride() {
      const raw = this.getAttribute("refresh-ms");
      if (raw === null) return null;
      const n = Number(raw);
      return Number.isFinite(n) && n >= 0 ? n : null;
    }

    connectedCallback() {
      ensureStyle();
      this._build();
      this._reset();
      this._load();
    }

    disconnectedCallback() {
      clearTimeout(this._timer);
      clearTimeout(this._armTimer);
      this._seq = (this._seq || 0) + 1;
    }

    attributeChangedCallback(name, oldValue, newValue) {
      if (!this._built || oldValue === newValue || !this.isConnected) return;
      if (name === "conversation-id") {
        this._reset();
        this._load();
      } else {
        clearTimeout(this._timer);
        this._schedule();
      }
    }

    _build() {
      if (this._built) return;
      this._built = true;
      this._seq = 0;
      this._users = new Map();

      this._header = el("div", "as-thread-header");
      this._title = el("h3", "as-thread-title");
      this._meta = el("span", "as-thread-meta");
      this._status = el("span", "as-thread-status");
      this._taskLink = el("a", "as-thread-task", "Open the task");
      this._taskLink.hidden = true;
      this._remove = el("button", "as-thread-remove", "Delete");
      this._remove.type = "button";
      this._remove.addEventListener("click", () => this._removeClicked());
      this._header.append(this._title, this._meta, this._status, this._taskLink, this._remove);

      this._error = el("p", "as-thread-error");
      this._error.setAttribute("role", "alert");
      this._error.hidden = true;
      this._messages = el("div", "as-thread-messages");
      this._messages.setAttribute("aria-live", "polite");

      this._text = document.createElement("textarea");
      this._text.className = "as-thread-text";
      this._text.rows = 2;
      this._text.setAttribute("aria-label", "Your message");
      this._text.placeholder = "Write a message (Enter sends, Shift+Enter adds a line, /help lists commands)";
      this._text.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && !e.shiftKey) {
          e.preventDefault();
          this._send();
        }
      });
      this._send_ = el("button", "as-thread-send", "Send");
      this._send_.type = "button";
      this._send_.addEventListener("click", () => this._send());
      this._sendError = el("p", "as-thread-send-error");
      this._sendError.setAttribute("role", "alert");
      this._sendError.hidden = true;
      const composer = el("div", "as-thread-composer");
      composer.append(this._text, this._send_);

      this.append(this._header, this._error, this._messages, composer, this._sendError);
    }

    // _reset forgets the conversation that was shown.
    _reset() {
      this._lastId = 0;
      this._view = null;
      this._messages.textContent = "";
      this._title.textContent = "";
      this._meta.textContent = "";
      this._status.textContent = "";
      this._taskLink.hidden = true;
      this._error.hidden = true;
      this._sendError.hidden = true;
      this._seq += 1;
      clearTimeout(this._timer);
      const none = !this.conversationId;
      this._text.disabled = none;
      this._send_.disabled = none;
      this._remove.hidden = none;
      if (none) this._messages.appendChild(el("p", "as-thread-none", "Pick a conversation, or start a new one."));
    }

    async _load() {
      clearTimeout(this._timer);
      const id = this.conversationId;
      if (!id) return;
      const seq = ++this._seq;
      this.setAttribute("aria-busy", "true");
      let failure = "";
      try {
        const view = await window.asApi.getJSON(`/api/v1/conversations/${encodeURIComponent(id)}`);
        if (seq !== this._seq) return;
        if (!this._users.has(view.user_id)) {
          try {
            const users = await window.asApi.loadUsers();
            if (seq === this._seq) this._users = users;
          } catch (e) {
            /* the user's id is shown instead of the name */
          }
        }
        const body = await window.asApi.getJSON(`/api/v1/conversations/${encodeURIComponent(id)}/messages?after=${this._lastId}`);
        if (seq !== this._seq) return;
        this._error.hidden = true;
        this._showHeader(view);
        this._append(body.items);
      } catch (err) {
        if (seq !== this._seq) return;
        failure = /not found/i.test(err.message) ? "This conversation does not exist (it may have been deleted)." : `Could not load the conversation: ${err.message}`;
        this._error.textContent = failure;
        this._error.hidden = false;
        this._notFound = /not found/i.test(err.message);
      }
      this.removeAttribute("aria-busy");
      this._schedule();
      this.dispatchEvent(new CustomEvent("as-load", { detail: { ok: failure === "", message: failure, live: !!this._live, at: new Date() }, bubbles: true }));
    }

    _interval() {
      const override = this.refreshOverride;
      if (override !== null) return override;
      return this._view && WORKING.has(this._view.task_status) ? FAST_MS : SLOW_MS;
    }

    _schedule() {
      clearTimeout(this._timer);
      this._live = false;
      const ms = this._interval();
      // A conversation that is gone will not come back by waiting.
      if (ms > 0 && this.isConnected && this.conversationId && !(this._error && !this._error.hidden && this._notFound)) {
        this._live = true;
        this._timer = setTimeout(() => this._load(), ms);
      }
    }

    _showHeader(view) {
      this._view = view;
      this._notFound = false;
      this._title.textContent = view.title || view.workflow_id;
      this._meta.textContent = `${view.title ? `${view.workflow_id} · ` : ""}${this._users.get(view.user_id) || window.asApi.shortId(view.user_id)}`;
      const status = view.task_status;
      this._status.textContent = status ? STATE_TEXT[status] || status : "Write a message to start.";
      this._status.className = `as-thread-status${status ? ` as-state-${status}` : ""}`;
      if (view.task_id) {
        this._taskLink.href = `#/tasks/${encodeURIComponent(view.task_id)}`;
        this._taskLink.hidden = false;
      } else {
        this._taskLink.hidden = true;
      }
      // While the run works a message would be refused.
      const busy = WORKING.has(status);
      this._send_.disabled = busy;
      this._text.placeholder = busy ? "The assistant is working…" : "Write a message (Enter sends, Shift+Enter adds a line, /help lists commands)";
    }

    _append(items) {
      if (items.length === 0) return;
      const wasAtBottom = this._messages.scrollHeight - this._messages.scrollTop - this._messages.clientHeight < 40;
      for (const m of items) {
        if (m.id <= this._lastId) continue;
        this._lastId = m.id;
        const row = el("div", `as-msg as-msg-${m.role} as-kind-${m.kind}`);
        row.dataset.messageId = String(m.id);
        row.append(el("span", "as-msg-who", ROLE_LABEL[m.role] || m.role), el("div", "as-msg-text", m.text));
        row.appendChild(el("span", "as-msg-time", window.asFormat.time(m.created_at, { short: true })));
        this._messages.appendChild(row);
      }
      if (wasAtBottom) this._messages.scrollTop = this._messages.scrollHeight;
    }

    _failSend(message) {
      this._sendError.textContent = message;
      this._sendError.hidden = false;
    }

    async _send() {
      if (this._sending || !this.conversationId) return;
      const text = this._text.value;
      if (text.trim() === "") return this._failSend("Write something first.");
      this._sending = true;
      this._send_.disabled = true;
      this._sendError.hidden = true;
      this._sendError.textContent = "";
      try {
        const res = await fetch(`/api/v1/conversations/${encodeURIComponent(this.conversationId)}/messages`, {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ text }),
        });
        if (!res.ok) {
          this._failSend(`Could not send the message: ${await window.asApi.errorMessage(res)}`);
          // The message may have been stored (a 502): show what the conversation now holds.
          if (res.status === 502) this._load();
          return;
        }
        this._text.value = "";
        await this._load();
      } catch (err) {
        this._failSend(`Could not send the message: ${err.message}`);
      } finally {
        this._sending = false;
        this._send_.disabled = !!(this._view && WORKING.has(this._view.task_status));
      }
    }

    async _removeClicked() {
      if (!this._armed) {
        this._armed = true;
        this._remove.textContent = "Click again to delete";
        this._remove.classList.add("armed");
        this._armTimer = setTimeout(() => this._disarm(), ARM_MS);
        return;
      }
      clearTimeout(this._armTimer);
      this._disarm();
      try {
        const res = await fetch(`/api/v1/conversations/${encodeURIComponent(this.conversationId)}`, { method: "DELETE", headers: { Accept: "application/json" } });
        if (!res.ok) {
          this._failSend(`Could not delete the conversation: ${await window.asApi.errorMessage(res)}`);
          return;
        }
        this.dispatchEvent(new CustomEvent("as-conversation-removed", { detail: { id: this.conversationId }, bubbles: true }));
      } catch (err) {
        this._failSend(`Could not delete the conversation: ${err.message}`);
      }
    }

    _disarm() {
      this._armed = false;
      this._remove.textContent = "Delete";
      this._remove.classList.remove("armed");
    }
  }

  customElements.define("as-chat-thread", AsChatThread);
})();
