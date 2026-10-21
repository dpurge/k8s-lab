// as-chat-thread: one conversation: what was said, in order, and a box to say
// more. The assistant's messages come from the workflow run: its questions, its
// result, and its failures (in red). While the run is working the page polls
// for them quickly and the box is closed; when it waits for an answer the next
// message is the answer. Anything that goes wrong is shown, never swallowed.
//
// Attributes: conversation-id, refresh-ms (default 1500 while working, 6000
// otherwise; 0 turns polling off).
// Events (bubbling): as-conversation-removed {id}, as-load {ok, message, live, at}.
//
// An assistant message of kind "ui" carries A2UI messages: the thread draws each
// surface they create with <dp-a2ui-surface> (the renderer is loaded from
// /ui/a2ui.js the first time one is needed, so a conversation without one never
// downloads it). What the user does in it (a button, say) goes back through
// POST /ui-events as the user's turn. An interface that a later action of the
// user has answered stays visible but can no longer be used. The values the user
// had entered when they acted travel with the action (in the action's metadata,
// as the extension assistant_data_model) and are put back when the conversation
// is opened again, so a submitted form still shows what was submitted. A surface
// with a password field is never included: such a value is not stored.
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
    link.href = "/static/components/as-chat-thread/component.css";
    document.head.appendChild(link);
  }

  // The key, among an action's metadata extensions, of the values entered.
  const DATA_MODEL = "assistant_data_model";
  const MAX_DATA_MODEL_BYTES = 64 * 1024;

  // The id of the assistant's own catalog of functions; the server's
  // schemas/a2ui/assistant_catalog.json has the same (a test keeps them equal).
  const ASSISTANT_CATALOG = "https://assistant.k8s-lab.local/a2ui/catalog.json";

  // The renderer is its own chunk (the browser keeps the module once loaded);
  // window.asA2uiLoader lets a test stand in for it. The page's media hosts are
  // asked for once per conversation opened.
  async function loadA2ui() {
    const mod = await (window.asA2uiLoader ? window.asA2uiLoader() : import("/ui/a2ui.js"));
    let hosts = [];
    try {
      const config = await window.asApi.getJSON("/a2ui-config.json");
      if (Array.isArray(config.media_hosts)) hosts = config.media_hosts;
    } catch (e) {
      /* no extra media hosts: only this server and data: URLs */
    }
    mod.setMediaHosts(hosts);
    return mod;
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
      this._dropInterfaces();
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

    _dropInterfaces() {
      this._gen = (this._gen || 0) + 1;
      this._client = null;
      this._surfaces = new Map();
      this._historic = new Set();
      this._chain = Promise.resolve();
    }

    // _reset forgets the conversation that was shown.
    _reset() {
      this._dropInterfaces();
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
      const text = status ? STATE_TEXT[status] || status : "Write a message to start.";
      // When the run handed the request to another workflow, it is that one that
      // takes the turn.
      this._status.textContent = view.handled_by ? `${text} Handled by ${view.handled_by}.` : text;
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
      const historic = this._lastId === 0;
      for (const m of items) {
        if (m.id <= this._lastId) continue;
        this._lastId = m.id;
        const row = el("div", `as-msg as-msg-${m.role} as-kind-${m.kind}`);
        row.dataset.messageId = String(m.id);
        if (m.kind === "ui") {
          row.append(el("span", "as-msg-who", ROLE_LABEL[m.role] || m.role));
          const body = el("div", "as-msg-ui");
          row.append(body);
          this._messages.appendChild(row);
          this._drawInterface(m, body, row, historic);
          continue;
        }
        if (m.kind === "ui_action") {
          // Function calls and their answers are the page's and the assistant's
          // business, not something the user said.
          const traffic = m.ui && (m.ui.callAgentFunction || m.ui.rendererFunctionResponse);
          if (traffic) continue;
          this._answered();
          this._restoreValues(m.ui);
        }
        row.append(el("span", "as-msg-who", ROLE_LABEL[m.role] || m.role), el("div", "as-msg-text", m.text));
        row.appendChild(el("span", "as-msg-time", window.asFormat.time(m.created_at, { short: true })));
        this._messages.appendChild(row);
      }
      if (wasAtBottom) this._messages.scrollTop = this._messages.scrollHeight;
    }

    // _answered makes the interfaces shown so far read-only: the user has acted
    // on one, so the run moved on.
    _answered() {
      for (const row of this._messages.querySelectorAll(".as-kind-ui:not([inert])")) {
        row.setAttribute("inert", "");
        row.classList.add("as-answered");
      }
    }

    // _restoreValues puts back the values an action carried, once the surfaces
    // before it are drawn.
    _restoreValues(event) {
      const extensions = event && event.action && event.action.metadata && event.action.metadata.extensions;
      const saved = extensions && extensions[DATA_MODEL];
      if (!saved || typeof saved !== "object") return;
      const gen = this._gen;
      this._chain = this._chain.then(async () => {
        if (gen !== this._gen || !this._client) return;
        for (const [surfaceId, value] of Object.entries(saved)) {
          if (!this._surfaces.has(surfaceId)) continue;
          try {
            await this._client.receive([{ version: "v1.0", updateDataModel: { surfaceId, path: "/", value } }]);
          } catch (err) {
            console.warn(`Could not put back the values of ${surfaceId}: ${err.message}`);
          }
        }
      });
    }

    // _dataModels are the values now in the surfaces, for an action to carry; a
    // surface with a password field, or a very large one, is left out.
    _dataModels() {
      const out = {};
      let size = 0;
      for (const [surfaceId, node] of this._surfaces) {
        const surface = node.surface;
        if (!surface) continue;
        const components = [...surface.componentsModel.values];
        if (components.some((c) => c.properties && c.properties.variant === "obscured")) continue;
        let value;
        try {
          value = JSON.parse(JSON.stringify(surface.dataModel.get("/") ?? {}));
        } catch (e) {
          continue;
        }
        if (!value || typeof value !== "object" || Object.keys(value).length === 0) continue;
        size += JSON.stringify(value).length;
        if (size > MAX_DATA_MODEL_BYTES) break;
        out[surfaceId] = value;
      }
      return out;
    }

    // _drawInterface hands the message's A2UI messages to the renderer, in order
    // (one message at a time, so a slow load never reorders them), and shows the
    // surfaces they create.
    _drawInterface(m, body, row, historic) {
      const gen = this._gen;
      this._chain = this._chain.then(async () => {
        try {
          const mod = await loadA2ui();
          if (gen !== this._gen) return;
          if (!this._client) this._client = this._newClient(mod);
          const list = typeof m.ui === "string" ? JSON.parse(m.ui) : m.ui;
          for (const msg of list) {
            // A function call, or the answer to one, is an event of the moment, not
            // state: replaying it from the history would run the function again
            // and answer a call that was answered long ago.
            if (historic && (msg.callRendererFunction || msg.agentFunctionResponse)) continue;
            const created = msg.createSurface && msg.createSurface.surfaceId;
            if (created && historic) this._historic.add(created);
            await this._client.receive([msg]);
            if (gen !== this._gen) return;
            if (created) {
              const surface = this._client.processor.getSurface(created);
              if (!surface) continue;
              const node = document.createElement("dp-a2ui-surface");
              node.surface = surface;
              this._surfaces.set(created, node);
              body.appendChild(node);
            }
            const deleted = msg.deleteSurface && msg.deleteSurface.surfaceId;
            if (deleted && this._surfaces.has(deleted)) {
              this._surfaces.get(deleted).remove();
              this._surfaces.delete(deleted);
            }
          }
          if (!body.firstChild) row.hidden = true;
        } catch (err) {
          body.appendChild(el("p", "as-thread-error", `The interface could not be drawn: ${err.message}`));
        }
      });
    }

    // _newClient makes the renderer's client for this conversation, with the
    // assistant's functions: viewport (the page runs it, and an agent may ask for
    // it) and listWorkflows (the page asks the assistant, for the surface a
    // binding is on or, failing that, the first one there is).
    _newClient(mod) {
      let client = null;
      const catalog = mod.defineCatalog({
        id: ASSISTANT_CATALOG,
        functions: [
          {
            name: "viewport",
            returnType: "object",
            allowedCallers: "rendererOrAgent",
            description: "The size of the window and its colour scheme.",
            execute: () => ({
              width: window.innerWidth,
              height: window.innerHeight,
              pixelRatio: window.devicePixelRatio || 1,
              colorScheme: document.documentElement.getAttribute("data-theme") === "dark" || (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches && document.documentElement.getAttribute("data-theme") !== "light") ? "dark" : "light",
            }),
          },
          {
            name: "listWorkflows",
            returnType: "string",
            description: "The workflows a user can start, asked of the assistant.",
            execute: () => {
              const surface = this._surfaces.keys().next().value;
              if (!surface) return Promise.reject(new Error("there is no interface to ask from"));
              return client.callAgentFunction(surface, { "@call": "listWorkflows", catalogId: ASSISTANT_CATALOG, args: {} });
            },
          },
        ],
      });
      client = mod.createClient({ send: (message) => this._sendEvent(message), catalogs: [catalog] });
      return client;
    }

    // _sendEvent sends what the renderer reports to the server. An action is the
    // user's turn; an error from an interface that was already in the history
    // when the conversation was opened was reported then, so it is not reported
    // again at every reload.
    async _sendEvent(message) {
      const kind = message.action ? "action" : message.error ? "error" : message.callAgentFunction ? "call" : "reply";
      if (kind === "error" && this._historic.has(message.error.surfaceId)) return;
      if (kind === "action") {
        this._sendError.hidden = true;
        const models = this._dataModels();
        if (Object.keys(models).length > 0) {
          message = { ...message, action: { ...message.action, metadata: { extensions: { ...(message.action.metadata && message.action.metadata.extensions), [DATA_MODEL]: models } } } };
        }
      }
      try {
        const res = await fetch(`/api/v1/conversations/${encodeURIComponent(this.conversationId)}/ui-events`, {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify(message),
        });
        if (!res.ok) {
          const reason = await window.asApi.errorMessage(res);
          if (kind === "action") this._failSend(`Could not send that: ${reason}`);
          else console.warn(`The server did not take the interface's ${kind}: ${reason}`);
          if (res.status === 502) this._load();
          return;
        }
        // The answer to a call, or the next step of the run, is on the server now.
        if (kind === "action" || kind === "call" || kind === "reply") await this._load();
      } catch (err) {
        if (kind === "action") this._failSend(`Could not send that: ${err.message}`);
      }
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
