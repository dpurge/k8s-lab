// The admin interface's page logic: a hash router that puts the right
// component on the page, and the wiring between components, the browser's
// address and the status bar. Everything else lives in the as-* components.
//
//   #/tasks?status=failed&page=2   the task list (status and page are optional)
//   #/tasks/<id>                   one task
//   #/usage?period=7d              usage by user and model, and the models
//   #/chat, #/chat/<id>            conversations; one open
(function () {
  const STATUSES = ["pending", "running", "waiting", "succeeded", "failed", "cancelled"];
  const PERIODS = ["all", "24h", "7d", "30d"];
  const UUID = /^[0-9a-fA-F-]{36}$/;

  // parseHash turns a location hash into a route. An unknown status or page in
  // the query is ignored rather than trusted.
  function parseHash(hash) {
    const raw = String(hash || "").replace(/^#/, "");
    const [path, query = ""] = raw.split("?");
    const parts = path.split("/").filter(Boolean);
    if (parts.length === 0 || (parts.length === 1 && parts[0] === "tasks")) {
      const params = new URLSearchParams(query);
      const status = STATUSES.includes(params.get("status")) ? params.get("status") : "";
      const page = parseInt(params.get("page"), 10);
      return { view: "tasks", status, page: page >= 1 ? page : 1 };
    }
    if (parts[0] === "chat" && parts.length <= 2) {
      if (parts.length === 1) return { view: "chat", id: "" };
      let id = "";
      try {
        id = decodeURIComponent(parts[1]);
      } catch (e) {
        return { view: "notfound" };
      }
      return UUID.test(id) ? { view: "chat", id } : { view: "notfound" };
    }
    if (parts.length === 1 && parts[0] === "usage") {
      const period = new URLSearchParams(query).get("period");
      return { view: "usage", period: PERIODS.includes(period) ? period : "all" };
    }
    if (parts.length === 2 && parts[0] === "tasks") {
      let id = "";
      try {
        id = decodeURIComponent(parts[1]);
      } catch (e) {
        return { view: "notfound" };
      }
      return UUID.test(id) ? { view: "task", id } : { view: "notfound" };
    }
    return { view: "notfound" };
  }

  // hashFor is the inverse of parseHash; defaults are left out of the address.
  function hashFor(route) {
    if (route.view === "task") return `#/tasks/${encodeURIComponent(route.id)}`;
    if (route.view === "chat") return route.id ? `#/chat/${encodeURIComponent(route.id)}` : "#/chat";
    if (route.view === "usage") return route.period && route.period !== "all" ? `#/usage?period=${route.period}` : "#/usage";
    const params = new URLSearchParams();
    if (route.status) params.set("status", route.status);
    if (route.page && route.page > 1) params.set("page", String(route.page));
    const query = params.toString();
    return `#/tasks${query ? `?${query}` : ""}`;
  }

  function card(heading, child) {
    const c = document.createElement("as-card");
    c.setAttribute("heading", heading);
    c.appendChild(child);
    return c;
  }

  // start wires the page. It returns a function that stops listening, which
  // tests use.
  function start() {
    const view = document.getElementById("view");
    const nav = document.getElementById("mainNav");
    const statusBar = document.getElementById("statusBar");
    let shown = ""; // which kind of view is on the page
    let list = null;
    let detail = null;
    let usage = null;
    let chatList = null;
    let chatThread = null;

    function render() {
      const route = parseHash(window.location.hash);
      if (nav) nav.setAttribute("active", route.view === "usage" ? "usage" : route.view === "chat" ? "chat" : "tasks");
      if (route.view === "chat") {
        if (shown !== "chat") {
          chatList = document.createElement("as-chat-list");
          chatList.setAttribute("active", route.id);
          chatThread = document.createElement("as-chat-thread");
          if (route.id) chatThread.setAttribute("conversation-id", route.id);
          const layout = document.createElement("div");
          layout.className = "as-chat-layout";
          layout.append(card("Conversations", chatList), card("Chat", chatThread));
          view.textContent = "";
          view.appendChild(layout);
          shown = "chat";
        } else {
          chatList.setAttribute("active", route.id);
          if (route.id) chatThread.setAttribute("conversation-id", route.id);
          else chatThread.removeAttribute("conversation-id");
        }
      } else if (route.view === "usage") {
        if (shown !== "usage") {
          usage = document.createElement("as-usage");
          usage.setAttribute("period", route.period);
          const models = document.createElement("as-model-list");
          view.textContent = "";
          view.append(card("Usage", usage), card("Models", models));
          shown = "usage";
        } else {
          usage.setAttribute("period", route.period);
        }
      } else if (route.view === "tasks") {
        if (shown !== "tasks") {
          list = document.createElement("as-task-list");
          // Attributes first: an element that is connected starts loading at
          // once, and would load the defaults before these were set.
          list.setAttribute("status", route.status);
          list.setAttribute("page", String(route.page));
          view.textContent = "";
          view.append(card("New task", document.createElement("as-new-task")), card("Tasks", list));
          shown = "tasks";
        } else {
          list.setAttribute("status", route.status);
          list.setAttribute("page", String(route.page));
        }
      } else if (route.view === "task") {
        if (shown !== "task") {
          detail = document.createElement("as-task-detail");
          detail.setAttribute("task-id", route.id);
          view.textContent = "";
          view.appendChild(card("Task", detail));
          shown = "task";
        } else {
          detail.setAttribute("task-id", route.id);
        }
      } else {
        const p = document.createElement("p");
        p.className = "as-not-found";
        p.append("Page not found. ");
        const a = document.createElement("a");
        a.href = "#/tasks";
        a.textContent = "Back to tasks";
        p.appendChild(a);
        view.textContent = "";
        view.appendChild(card("Not found", p));
        shown = "notfound";
      }
    }

    function onListChange(e) {
      if (e.detail.period !== undefined) {
        window.location.hash = hashFor({ view: "usage", period: e.detail.period });
        return;
      }
      window.location.hash = hashFor({ view: "tasks", status: e.detail.status, page: e.detail.page });
    }

    // Several components can be on a page and load independently: an error
    // stays in the status bar until the component that raised it loads well,
    // whatever the others do in the meantime.
    const failing = new Map();

    // A task that was just started: open its page, where its progress shows.
    function onTaskCreated(e) {
      window.location.hash = hashFor({ view: "task", id: e.detail.task.id });
    }

    // The task page's buttons: a retry opens the new task, a removal leaves the
    // task's page for the list.
    function onTaskRetried(e) {
      window.location.hash = hashFor({ view: "task", id: e.detail.task.id });
    }

    // A new conversation is opened at once; a deleted one leaves the page.
    function onConversationCreated(e) {
      window.location.hash = hashFor({ view: "chat", id: e.detail.conversation.id });
    }

    function onConversationRemoved() {
      window.location.hash = hashFor({ view: "chat", id: "" });
    }

    function onTaskRemoved() {
      window.location.hash = hashFor({ view: "tasks", status: "", page: 1 });
    }

    function onLoad(e) {
      if (!statusBar) return;
      const { ok, message, live, at } = e.detail;
      for (const source of failing.keys()) if (!source.isConnected) failing.delete(source);
      if (ok) {
        failing.delete(e.target);
        statusBar.setJobStatus(`Updated ${window.asFormat.clock(at)}${live ? " · refreshing" : ""}`);
      } else {
        failing.set(e.target, message);
      }
      const messages = [...failing.values()];
      statusBar.setMessage(messages.length ? messages[messages.length - 1] : "", messages.length > 0);
    }

    const toggle = document.getElementById("themeToggle");
    const onToggle = () => window.toggleTheme && window.toggleTheme();
    if (toggle) toggle.addEventListener("click", onToggle);
    view.addEventListener("as-list-change", onListChange);
    view.addEventListener("as-load", onLoad);
    view.addEventListener("as-task-created", onTaskCreated);
    view.addEventListener("as-task-retried", onTaskRetried);
    view.addEventListener("as-task-removed", onTaskRemoved);
    view.addEventListener("as-conversation-created", onConversationCreated);
    view.addEventListener("as-conversation-removed", onConversationRemoved);
    window.addEventListener("hashchange", render);
    render();

    return function stop() {
      if (toggle) toggle.removeEventListener("click", onToggle);
      view.removeEventListener("as-list-change", onListChange);
      view.removeEventListener("as-load", onLoad);
      view.removeEventListener("as-task-created", onTaskCreated);
      view.removeEventListener("as-task-retried", onTaskRetried);
      view.removeEventListener("as-task-removed", onTaskRemoved);
      view.removeEventListener("as-conversation-created", onConversationCreated);
      view.removeEventListener("as-conversation-removed", onConversationRemoved);
      window.removeEventListener("hashchange", render);
    };
  }

  window.AssistantApp = { parseHash, hashFor, start };

  document.addEventListener("DOMContentLoaded", () => {
    if (document.getElementById("view") && !window.__assistantTest) start();
  });
})();
