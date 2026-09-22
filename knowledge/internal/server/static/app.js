const $ = (id) => document.getElementById(id);
let currentChat = "";
// Tracks whether the most recently observed current job was an
// "ingest" job, so pollStatus can detect the running -> finished
// transition and refresh the draft list (see pollStatus below).
let ingestJobRunning = false;

const TAB_IDS = ["knowledgeTab", "chatTab", "ingestTab"];
const mainNav = $("mainNav");
const confirmDialog = $("confirmDialog");
mainNav.items = [
  { id: "knowledge", label: "Knowledge" },
  { id: "chat", label: "Chat" },
  { id: "ingest", label: "Ingest" },
];
mainNav.active = "knowledge"; // matches the markup's default "tab active" on knowledgeTab
mainNav.addEventListener("kb-nav-select", (event) => showTab(event.detail.id));

// One delegated kb-card-select listener per list container, added
// once here, instead of an inline onclick handler re-created on every
// card each time #list/#drafts/#chats gets re-rendered.
$("list").addEventListener("kb-card-select", (event) => run(() => openItem(event.detail.id)));
$("drafts").addEventListener("kb-card-select", (event) => run(() => openDraft(event.detail.id)));
$("chats").addEventListener("kb-card-select", (event) => run(() => openChat(event.detail.id)));

function showKnowledgeView(view) {
  $("knowledgeListView").classList.toggle("active", view === "list");
  $("knowledgeDetailView").classList.toggle("active", view === "detail");
}

function showIngestView(view) {
  $("ingestListView").classList.toggle("active", view === "list");
  $("ingestDetailView").classList.toggle("active", view === "detail");
}

function showTab(tab) {
  for (const id of TAB_IDS) $(id).classList.toggle("active", id === tab + "Tab");
  mainNav.active = tab;
  // Entering a tab always lands on its list view for predictability;
  // openItem()/openDraft() call showTab() themselves and then switch
  // to the detail view immediately after, overriding this.
  if (tab === "knowledge") showKnowledgeView("list");
  if (tab === "ingest") showIngestView("list");
  if (tab === "chat") run(loadChats);
  if (tab === "ingest") run(async () => { await loadDrafts(); await loadJobs(); });
}

function parseTags(value) {
  return (value || "")
    .split(",")
    .map((tag) => tag.trim().toLowerCase())
    .filter(Boolean);
}

function toRFC(id) {
  const value = $(id).value;
  return value ? new Date(value).toISOString() : "";
}

// One shared footer message, used by every tab — success/error
// feedback from any action lands here, not in a per-tab element.
function message(text, error = false) {
  $("statusBar").setMessage(text, error);
}

async function run(fn) {
  try {
    message("");
    await fn();
  } catch (error) {
    message(error.message || String(error), true);
  }
}

async function api(path, options = {}) {
  const response = await fetch("/api/v1" + path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });

  if (!response.ok) {
    let body = await response.text();
    try {
      const parsed = JSON.parse(body);
      body = parsed.error?.message || body;
    } catch {}
    throw new Error(body);
  }

  return response.status === 204 ? null : response.json();
}

function esc(value) {
  return (value || "").replace(/[&<>"']/g, (char) => {
    return {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    }[char];
  });
}

// listOffset is this page's starting offset — reset to 0 by any fresh
// search (loadList) and moved by loadListPrev/loadListNext. Kept as
// simple module state, matching currentChat's existing pattern, rather
// than threading it through every call site.
let listOffset = 0;
const listPageSize = 20;

async function loadList(offset = 0) {
  listOffset = Math.max(0, offset);
  const params = new URLSearchParams();

  if ($("q").value) params.set("q", $("q").value);
  for (const tag of parseTags($("filterTags").value)) params.append("tag", tag);
  if (toRFC("start")) params.set("start", toRFC("start"));
  if (toRFC("end")) params.set("end", toRFC("end"));
  params.set("limit", listPageSize);
  params.set("offset", listOffset);

  const data = await api("/knowledge?" + params);
  const items = data.items || [];

  $("list").innerHTML = items.length
    ? items.map(renderItem).join("")
    : '<p class="muted">No knowledge items found.</p>';

  $("listPrev").disabled = listOffset === 0;
  $("listNext").disabled = !data.has_more;
}

function loadListPrev() {
  return loadList(Math.max(0, listOffset - listPageSize));
}

function loadListNext() {
  return loadList(listOffset + listPageSize);
}

function renderItem(item) {
  const score = item.score === undefined ? "" : "Score " + item.score.toFixed(3) + " · ";
  const tags = (item.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`).join("");
  // A blank title/summary means an import left it for the background
  // generate queue to fill in (see knowledge-background-queue) — show a
  // placeholder instead of a blank line so the item still reads sensibly
  // in the list until that finishes.
  const title = item.title ? esc(item.title) : '<span class="muted">(generating title…)</span>';
  const summary = item.summary ? esc(item.summary) : '<span class="muted">(generating summary…)</span>';

  return `
    <kb-card data-id="${item.id}">
      <b>${title}</b>
      <div>${summary}</div>
      <div>${tags}</div>
      <div class="muted">${score}Updated ${new Date(item.updated_at).toLocaleString()}</div>
    </kb-card>
  `;
}

async function openItem(id) {
  showTab("knowledge");
  const item = await api("/knowledge/" + id);
  $("id").value = item.id;
  $("title").value = item.title;
  $("tags").value = (item.tags || []).join(", ");
  $("summary").value = item.summary;
  $("body").value = item.body;
  showKnowledgeView("detail");
}

function clearItemForm() {
  $("id").value = "";
  $("title").value = "";
  $("tags").value = "";
  $("summary").value = "";
  $("body").value = "";
}

function newItem() {
  clearItemForm();
  showKnowledgeView("detail");
}

async function generateTitle() {
  const body = $("body").value.trim();
  if (!body) {
    message("Enter a body first.");
    return;
  }
  message("Generating title…");
  const res = await api("/knowledge/generate/title", { method: "POST", body: JSON.stringify({ body }) });
  $("title").value = res.title;
  message("");
}

async function generateSummary() {
  const body = $("body").value.trim();
  if (!body) {
    message("Enter a body first.");
    return;
  }
  message("Generating summary…");
  const res = await api("/knowledge/generate/summary", { method: "POST", body: JSON.stringify({ body }) });
  $("summary").value = res.summary;
  message("");
}

async function save() {
  const payload = JSON.stringify({
    title: $("title").value,
    summary: $("summary").value,
    body: $("body").value,
    tags: parseTags($("tags").value),
  });
  const id = $("id").value;
  const item = await api(id ? "/knowledge/" + id : "/knowledge", {
    method: id ? "PUT" : "POST",
    body: payload,
  });

  $("id").value = item.id;
  message("Saved " + new Date().toLocaleString());
  await loadList();
}

function exportItems() {
  const params = new URLSearchParams();
  for (const tag of parseTags($("exportTags").value)) params.append("tag", tag);
  location.href = "/api/v1/knowledge/export" + (params.toString() ? "?" + params : "");
}

async function importFile() {
  const file = $("importFile").files[0];
  if (!file) return;
  if (!(await confirmDialog.confirm("Import " + file.name + "? Rows marked delete: true are removed permanently.", { danger: true }))) return;

  // The server parses the YAML; the browser never does — this is an
  // opaque text blob out (export) and in (import), no client-side
  // YAML library needed.
  const content = await file.text();
  const result = await api("/knowledge/import", {
    method: "POST",
    headers: { "Content-Type": "application/yaml" },
    body: content,
  });
  $("importFile").value = "";
  message(
    `Imported ${result.imported} · deleted ${result.deleted} · unchanged ${result.unchanged}` +
      (result.errors.length ? `; ${result.errors.length} failed — see browser console` : ""),
    result.errors.length > 0,
  );
  if (result.errors.length) console.warn("Import errors:", result.errors);
  await loadList();
}

async function removeItem() {
  const id = $("id").value;
  if (!id || !(await confirmDialog.confirm("Delete this item?", { danger: true }))) return;
  await api("/knowledge/" + id, { method: "DELETE" });
  clearItemForm();
  await loadList();
  showKnowledgeView("list");
}

async function createChat() {
  const chat = await api("/chats", {
    method: "POST",
    body: JSON.stringify({
      title: $("newChatTitle").value,
      tags: parseTags($("newChatTags").value),
    }),
  });

  currentChat = chat.id;
  await loadChats();
  await openChat(chat.id);
}

async function loadChats() {
  const data = await api("/chats");
  $("chats").innerHTML = (data.chats || []).map(renderChat).join("");
}

function renderChat(chat) {
  const tags = (chat.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`).join("");
  const scope = tags || '<span class="muted">all knowledge</span>';

  return `
    <kb-card data-id="${chat.id}">
      <b>${esc(chat.title)}</b>
      <div>${scope}</div>
    </kb-card>
  `;
}

async function openChat(id) {
  currentChat = id;
  const chat = await api("/chats/" + id);
  const tags = (chat.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`).join("");

  $("chatTitle").textContent = chat.title;
  $("chatScope").innerHTML = "Scope: " + (tags || "all knowledge");
  $("chatMessages").innerHTML = (chat.messages || []).map(renderMsg).join("");
}

// markdownit() defaults to html:false (raw HTML is escaped, not passed
// through) and rejects dangerous link protocols (e.g. javascript:) via
// its built-in link validator; DOMPurify.sanitize() is a second,
// independent safety layer on the resulting HTML before it ever
// reaches innerHTML.
const markdownRenderer = markdownit({ linkify: true, breaks: true });

function md(value) {
  return DOMPurify.sanitize(markdownRenderer.render(value || ""));
}

function renderMsg(message) {
  const sources = message.sources?.length
    ? `
      <div class="sources">
        <b>Retrieved knowledge items</b>
        <ul>
          ${message.sources.map(renderSource).join("")}
        </ul>
      </div>
    `
    : "";

  return `
    <div class="msg">
      <div class="role">${esc(message.role)}</div>
      <div>${md(message.content)}</div>
      ${sources}
    </div>
  `;
}

function renderSource(source) {
  return `
    <li>
      <a href="#" onclick="run(() => openItem('${source.id}')); return false">${esc(source.title)}</a>
      <span class="muted">${source.score.toFixed(3)}</span><br />
      ${esc(source.summary)}
    </li>
  `;
}

async function deleteChat() {
  if (!currentChat) throw new Error("Select a chat first");
  if (!(await confirmDialog.confirm("Delete this chat and all of its messages?", { danger: true }))) return;

  await api("/chats/" + currentChat, { method: "DELETE" });
  currentChat = "";
  $("chatTitle").textContent = "Select or create a chat";
  $("chatScope").textContent = "";
  $("chatMessages").innerHTML = "";
  $("chatInput").value = "";
  await loadChats();
}

async function sendChat() {
  if (!currentChat) throw new Error("Create or select a chat first");

  const text = $("chatInput").value.trim();
  if (!text) return;

  $("chatInput").value = "";

  // Show the user's own message immediately, before the round trip —
  // openChat's server-truth re-render below replaces this exact markup
  // with the persisted version, so this is purely a perceived-latency
  // improvement, not a second source of truth.
  $("chatMessages").insertAdjacentHTML("beforeend", renderMsg({ role: "user", content: text }));
  $("chatMessages").scrollTop = $("chatMessages").scrollHeight;

  message("Waiting for reply…");
  await api("/chats/" + currentChat + "/messages", {
    method: "POST",
    body: JSON.stringify({ message: text }),
  });
  await openChat(currentChat);

  // The reply is produced asynchronously by the background queue (see
  // specs/features/knowledge-background-queue.md), so poll until it shows
  // up rather than assuming it's already there. The LLM can genuinely
  // take a couple of minutes on a resource-constrained machine (see
  // specs/memory.md), so this keeps checking well past a first glance —
  // and says so plainly rather than going silent — instead of quietly
  // giving up.
  const before = $("chatMessages").querySelectorAll(".msg").length;
  for (let i = 0; i < 60 && $("chatMessages").querySelectorAll(".msg").length < before + 1; i++) {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    await openChat(currentChat);
  }
  message($("chatMessages").querySelectorAll(".msg").length < before + 1 ? "Still waiting on a reply — it will appear here once ready." : "");
}

$("chatInput").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    run(sendChat);
  }
});

async function logout() {
  await api("/auth/logout", { method: "POST" });
  location.href = "/login.html";
}

async function startIngestUrl() {
  const url = $("ingestUrl").value.trim();
  if (!url) {
    message("Enter a URL first.");
    return;
  }
  const tags = parseTags($("ingestTags").value);
  const job = await api("/ingest/url", { method: "POST", body: JSON.stringify({ url, tags }) });
  message(`Started: ${job.kind} (job ${job.id})`);
}

async function startIngestFile() {
  const file = $("ingestFile").files[0];
  if (!file) return;

  const content = await file.text();
  const tags = parseTags($("ingestTags").value);
  const job = await api("/ingest/text", {
    method: "POST",
    body: JSON.stringify({ filename: file.name, content, tags }),
  });
  $("ingestFile").value = "";
  message(`Started: ${job.kind} (job ${job.id})`);
}

async function loadDrafts() {
  const data = await api("/ingest/drafts");
  const drafts = data.drafts || [];
  $("drafts").innerHTML = drafts.length
    ? drafts.map(renderDraft).join("")
    : '<p class="muted">No drafts pending review.</p>';
}

function renderDraft(d) {
  const tags = (d.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`).join("");

  return `
    <kb-card data-id="${esc(d.id)}">
      <b>${esc(d.title)}</b>
      <div>${esc(d.summary)}</div>
      <div>${tags}</div>
      <div class="muted">${esc(d.source_kind)}: ${esc(d.source_ref)} · chunk ${d.chunk_index + 1} · Updated ${new Date(d.updated_at).toLocaleString()}</div>
    </kb-card>
  `;
}

async function openDraft(id) {
  showTab("ingest");
  const d = await api("/ingest/drafts/" + id);
  $("draftId").value = d.id;
  $("draftSource").textContent = `${d.source_kind}: ${d.source_ref} (chunk ${d.chunk_index + 1})`;
  $("draftTitle").value = d.title;
  $("draftTags").value = (d.tags || []).join(", ");
  $("draftSummary").value = d.summary;
  $("draftBody").value = d.body;
  showIngestView("detail");
}

function clearDraftForm() {
  $("draftId").value = "";
  $("draftSource").textContent = "";
  $("draftTitle").value = "";
  $("draftTags").value = "";
  $("draftSummary").value = "";
  $("draftBody").value = "";
}

async function saveDraft() {
  const id = $("draftId").value;
  if (!id) return;
  const body = {
    title: $("draftTitle").value,
    summary: $("draftSummary").value,
    body: $("draftBody").value,
    tags: parseTags($("draftTags").value),
  };
  const d = await api("/ingest/drafts/" + id, { method: "PUT", body: JSON.stringify(body) });
  $("draftTitle").value = d.title;
  $("draftTags").value = (d.tags || []).join(", ");
  $("draftSummary").value = d.summary;
  $("draftBody").value = d.body;
  message("Saved.");
}

// Approving can take several seconds (one embedding call). This flag
// makes a second, overlapping approveDraft call a no-op instead of
// racing the in-flight request into the double-approve guard's 404.
let approveInFlight = false;

async function approveDraft() {
  const id = $("draftId").value;
  if (!id || approveInFlight) return;
  if (!(await confirmDialog.confirm("Approve this draft? It will become a real, searchable knowledge item."))) return;
  message("Approving…");
  approveInFlight = true;
  try {
    const result = await api("/ingest/drafts/" + id + "/approve", { method: "POST" });
    clearDraftForm();
    message(`Approved — created knowledge item ${result.id}.`);
    await loadDrafts();
    showIngestView("list");
  } finally {
    approveInFlight = false;
  }
}

async function discardDraft() {
  const id = $("draftId").value;
  if (!id) return;
  if (!(await confirmDialog.confirm("Discard this draft permanently? This cannot be undone.", { danger: true }))) return;
  await api("/ingest/drafts/" + id, { method: "DELETE" });
  clearDraftForm();
  message("Discarded.");
  await loadDrafts();
  showIngestView("list");
}

async function loadJobs() {
  const data = await api("/jobs");
  const list = data.jobs || [];
  $("jobs").innerHTML = list.length ? list.map(renderJob).join("") : '<p class="muted">No jobs yet.</p>';
}

// jobStatusLabel distinguishes a job that's running but hasn't been
// claimed by the queue's worker yet (no step set) — genuinely "pending",
// not yet doing anything — from one actively being worked on, since
// jobs.Status itself only has running/done/failed (see
// specs/features/knowledge-background-queue.md).
function jobStatusLabel(j) {
  if (j.status === "running") return j.step ? "active" : "pending";
  return j.status;
}

function renderJob(j) {
  const source = j.source_ref ? `${esc(j.source_kind)}: ${esc(j.source_ref)}` : "";
  const error = j.error ? `<div class="error">${esc(j.error)}</div>` : "";
  const label = jobStatusLabel(j);
  const step = j.step ? `<div class="muted">${esc(j.step)}</div>` : "";
  const actions =
    j.status === "failed"
      ? `<div class="actions">
           <kb-button onclick="run(() => retryJob('${esc(j.id)}'))">Retry</kb-button>
           <kb-button variant="danger" onclick="run(() => deleteJob('${esc(j.id)}'))">Delete</kb-button>
         </div>`
      : j.status !== "running"
        ? `<div class="actions">
             <kb-button variant="danger" onclick="run(() => deleteJob('${esc(j.id)}'))">Delete</kb-button>
           </div>`
        : "";

  return `
    <div class="item">
      <b>${esc(j.kind)} <span class="job-status job-status-${label}">${label}</span></b>
      <div class="muted">${source}</div>
      ${step}
      ${error}
      <div class="muted">Updated ${new Date(j.updated_at).toLocaleString()}</div>
      ${actions}
    </div>
  `;
}

async function retryJob(id) {
  const job = await api("/jobs/" + id + "/retry", { method: "POST" });
  message(`Retrying as new ${job.kind} job (${job.id}).`);
  await loadJobs();
}

async function deleteJob(id) {
  if (!(await confirmDialog.confirm("Delete this job's history permanently? This cannot be undone.", { danger: true }))) return;
  await api("/jobs/" + id, { method: "DELETE" });
  message("Job deleted.");
  await loadJobs();
}

// Polls for a currently-running server-side job (e.g. ingestion) and
// shows it in the footer, regardless of active tab. Failures are
// swallowed silently — this is a nice-to-have indicator, never
// something that should interrupt the user.
async function pollStatus() {
  try {
    const job = await api("/jobs/current");
    $("statusBar").setJobStatus(job ? `${job.kind}: ${job.step || "running"}` : "");

    // Detect an "ingest" job's running -> finished transition and
    // refresh the draft list + job history so newly created drafts
    // and the just-finished/failed job appear without a manual
    // reload, but only while the Ingest tab is open.
    const isIngestRunning = job?.kind === "ingest";
    if (ingestJobRunning && !isIngestRunning && $("ingestTab").classList.contains("active")) {
      // Called outside run(), so a rejection here can't be caught by
      // this function's own try/catch below — ignore it the same way
      // as the /jobs/current call itself: this is a nice-to-have
      // refresh, and the next poll will retry.
      loadDrafts().catch(() => {});
      loadJobs().catch(() => {});
    }
    ingestJobRunning = isIngestRunning;
  } catch {
    // ignore — next poll will retry
  }
}

run(async () => {
  const me = await api("/auth/me");
  $("userEmail").textContent = me.email;
});
run(loadList);
pollStatus();
setInterval(pollStatus, 3000);

const knowledgeID = new URLSearchParams(location.search).get("knowledge");
if (knowledgeID) run(() => openItem(knowledgeID));
