// The admin interface's page logic: a hash router that puts the right
// component on the page, and the wiring between components, the browser's
// address and the status bar. Everything else lives in the as-* components.
//
//   #/tasks?status=failed&page=2   the task list (status and page are optional)
//   #/tasks/<id>                   one task
//   #/usage?period=7d&group=model  usage per user, provider and model (group is optional)
//   #/models                       the models and whether each can take calls
//   #/attempts?period=7d&page=2    every model call, newest first, paged
//   #/chat, #/chat/<id>            conversations; one open
//   #/gallery                     the A2UI renderer's reference examples (development)
(function () {
  const STATUSES = ["pending", "running", "waiting", "succeeded", "failed", "cancelled"];
  const PERIODS = ["all", "24h", "7d", "30d"];
  const GROUPS = ["user", "provider", "model"];
  // The pages of the Usage section, in the order the side panel lists them.
  const USAGE_PAGES = [["usage", "Usage", "#/usage"], ["models", "Models", "#/models"], ["attempts", "Attempts", "#/attempts"]];
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
    if (parts.length === 1 && parts[0] === "gallery") return { view: "gallery" };
    if (parts.length === 1 && parts[0] === "usage") {
      const params = new URLSearchParams(query);
      const period = params.get("period");
      const group = params.get("group");
      return { view: "usage", period: PERIODS.includes(period) ? period : "all", group: GROUPS.includes(group) ? group : "" };
    }
    if (parts.length === 1 && parts[0] === "models") return { view: "models" };
    if (parts.length === 1 && parts[0] === "attempts") {
      const params = new URLSearchParams(query);
      const period = params.get("period");
      const page = parseInt(params.get("page"), 10);
      return { view: "attempts", period: PERIODS.includes(period) ? period : "all", page: page >= 1 ? page : 1 };
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
    if (route.view === "gallery") return "#/gallery";
    if (route.view === "usage") {
      const params = new URLSearchParams();
      if (route.period && route.period !== "all") params.set("period", route.period);
      if (route.group) params.set("group", route.group);
      const query = params.toString();
      return `#/usage${query ? `?${query}` : ""}`;
    }
    if (route.view === "models") return "#/models";
    if (route.view === "attempts") {
      const params = new URLSearchParams();
      if (route.period && route.period !== "all") params.set("period", route.period);
      if (route.page && route.page > 1) params.set("page", String(route.page));
      const query = params.toString();
      return `#/attempts${query ? `?${query}` : ""}`;
    }
    const params = new URLSearchParams();
    if (route.status) params.set("status", route.status);
    if (route.page && route.page > 1) params.set("page", String(route.page));
    const query = params.toString();
    return `#/tasks${query ? `?${query}` : ""}`;
  }

  function card(heading, child) {
    const c = document.createElement("dp-card");
    c.setAttribute("heading", heading);
    c.appendChild(child);
    return c;
  }

  // sectionNav is the side panel of the Usage section: its pages, the current
  // one marked.
  function sectionNav(current) {
    const nav = document.createElement("nav");
    nav.className = "as-section-nav";
    nav.setAttribute("aria-label", "Usage");
    for (const [view, label, href] of USAGE_PAGES) {
      const a = document.createElement("a");
      a.href = href;
      a.textContent = label;
      if (view === current) a.setAttribute("aria-current", "page");
      nav.appendChild(a);
    }
    return nav;
  }

  // start wires the page. It returns a function that stops listening, which
  // tests use.
  function start() {
    const view = document.getElementById("view");
    const nav = document.getElementById("mainNav");
    const statusBar = document.getElementById("statusBar");
    const shell = document.querySelector("dp-shell");
    // The conversation list lives in the shell's collapsible sidebar, only on
    // the chat page; a drawer on a phone. Without a shell (a bare test page) it
    // goes in front of the view.
    let sidebar = null;
    function setSidebar(content) {
      if (!content) {
        if (sidebar) sidebar.remove();
        sidebar = null;
        return;
      }
      if (!sidebar) {
        sidebar = document.createElement("div");
        sidebar.id = "chatSidebar";
        sidebar.slot = "sidebar";
        (shell || view.parentNode).insertBefore(sidebar, shell ? null : view);
      }
      sidebar.replaceChildren(content);
    }
    let shown = ""; // which kind of view is on the page
    let list = null;
    let detail = null;
    let usage = null;
    let attempts = null;
    let chatList = null;
    let chatThread = null;

    function render() {
      const route = parseHash(window.location.hash);
      if (route.view === "usage" || route.view === "models" || route.view === "attempts") setSidebar(sectionNav(route.view));
      else if (route.view !== "chat") setSidebar(null);
      if (shell) shell.toggleAttribute("data-chat", route.view === "chat");
      view.classList.toggle("as-chat-page", route.view === "chat");
      const inUsage = route.view === "usage" || route.view === "models" || route.view === "attempts";
      if (nav) nav.setAttribute("active", inUsage ? "usage" : route.view === "chat" ? "chat" : route.view === "gallery" ? "" : "tasks");
      if (route.view === "chat") {
        if (shown !== "chat") {
          chatList = document.createElement("as-chat-list");
          chatList.setAttribute("active", route.id);
          chatThread = document.createElement("as-chat-thread");
          if (route.id) chatThread.setAttribute("conversation-id", route.id);
          setSidebar(chatList);
          view.textContent = "";
          view.appendChild(chatThread);
          shown = "chat";
        } else {
          chatList.setAttribute("active", route.id);
          if (route.id) chatThread.setAttribute("conversation-id", route.id);
          else chatThread.removeAttribute("conversation-id");
        }
      } else if (route.view === "gallery") {
        if (shown !== "gallery") {
          view.textContent = "";
          shown = "gallery";
          // The renderer is its own chunk: no other page downloads it. It
          // learns which media hosts the page's policy allows before it draws.
          Promise.all([
            import("/ui/a2ui.js"),
            fetch("/a2ui-config.json").then((r) => r.json()).catch(() => ({ media_hosts: [] })),
            import("/ui/a2ui-gallery.js"),
          ]).then(([a2ui, config]) => {
            a2ui.setMediaHosts(Array.isArray(config.media_hosts) ? config.media_hosts : []);
            if (shown !== "gallery") return;
            view.append(document.createElement("dp-a2ui-gallery"));
          });
        }
      } else if (route.view === "usage") {
        if (shown !== "usage") {
          usage = document.createElement("as-usage");
          // Attributes first, as for the task list below.
          usage.setAttribute("period", route.period);
          if (route.group) usage.setAttribute("group", route.group);
          view.textContent = "";
          view.append(card("Usage", usage));
          shown = "usage";
        } else {
          usage.setAttribute("period", route.period);
          if (route.group) usage.setAttribute("group", route.group);
          else usage.removeAttribute("group");
        }
      } else if (route.view === "models") {
        if (shown !== "models") {
          view.textContent = "";
          view.append(card("Models", document.createElement("as-model-list")));
          shown = "models";
        }
      } else if (route.view === "attempts") {
        if (shown !== "attempts") {
          attempts = document.createElement("as-attempts");
          attempts.setAttribute("period", route.period);
          attempts.setAttribute("page", String(route.page));
          view.textContent = "";
          view.append(card("Attempts", attempts));
          shown = "attempts";
        } else {
          attempts.setAttribute("period", route.period);
          attempts.setAttribute("page", String(route.page));
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
      if (e.target.localName === "as-attempts") {
        window.location.hash = hashFor({ view: "attempts", period: e.detail.period, page: e.detail.page });
        return;
      }
      if (e.detail.period !== undefined) {
        window.location.hash = hashFor({ view: "usage", period: e.detail.period, group: e.detail.group });
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

    document.addEventListener("as-list-change", onListChange);
    document.addEventListener("as-load", onLoad);
    document.addEventListener("as-task-created", onTaskCreated);
    document.addEventListener("as-task-retried", onTaskRetried);
    document.addEventListener("as-task-removed", onTaskRemoved);
    document.addEventListener("as-conversation-created", onConversationCreated);
    document.addEventListener("as-conversation-removed", onConversationRemoved);
    window.addEventListener("hashchange", render);
    render();

    return function stop() {
      setSidebar(null);
      document.removeEventListener("as-list-change", onListChange);
      document.removeEventListener("as-load", onLoad);
      document.removeEventListener("as-task-created", onTaskCreated);
      document.removeEventListener("as-task-retried", onTaskRetried);
      document.removeEventListener("as-task-removed", onTaskRemoved);
      document.removeEventListener("as-conversation-created", onConversationCreated);
      document.removeEventListener("as-conversation-removed", onConversationRemoved);
      window.removeEventListener("hashchange", render);
    };
  }

  window.AssistantApp = { parseHash, hashFor, start };

  document.addEventListener("DOMContentLoaded", () => {
    if (document.getElementById("view") && !window.__assistantTest) start();
  });
})();
