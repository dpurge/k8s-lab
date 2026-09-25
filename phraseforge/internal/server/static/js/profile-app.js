// profile-app.js: client-side rendering for the Profile section of the
// unified SPA shell (app.html) — the simplest section, one read-only roles
// list, a locale switcher, and a change-password form, no items, no tabs.
// Registers itself on window.pfSections.profile for shell.js to call into.
//
// The one real design point, changed by phraseforge-spa-unified-shell:
// phraseforge-spa-profile originally did a full window.location.reload()
// after a locale change, since layout.html's chrome was server-rendered
// per request with no bootstrap of its own. Now that the whole shell
// (chrome included) has one combined bootstrap, a locale change instead
// re-fetches it (GET /api/v1/app-bootstrap) and calls
// window.pfRefreshAppBootstrap — which updates every section's own
// bootstrap/i18n and re-renders the sidebar/header and whichever section is
// open (itself, here) — with zero reload. See
// specs/features/phraseforge-spa-unified-shell.md.
(function () {
  window.pfSections = window.pfSections || {};
  let BOOT = window.__PF_APP_BOOTSTRAP__.profile;
  const T = (key) => (BOOT.i18n && BOOT.i18n[key]) || key;
  const root = document.getElementById("profile-app");
  const statusBar = document.getElementById("statusBar");

  function esc(s) {
    return String(s == null ? "" : s).replace(
      /[&<>"']/g,
      (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c],
    );
  }

  async function apiFetch(path, options) {
    const res = await fetch(path, options);
    if (!res.ok) {
      let message = res.statusText;
      try {
        const body = await res.json();
        if (body && body.error && body.error.message) message = body.error.message;
      } catch (_e) {
        // response wasn't JSON (e.g. a 302 to /login on session expiry) — fall through to statusText
      }
      const err = new Error(message);
      err.status = res.status;
      throw err;
    }
    if (res.status === 204) return null;
    return res.json();
  }

  function renderGrants(grants) {
    if (!grants || grants.length === 0) {
      return `<p class="empty-state">${esc(T("profile.role_none"))}</p>`;
    }
    const items = grants
      .map((g) =>
        g.role === "admin"
          ? `<li>${esc(T("profile.role_admin"))}</li>`
          : `<li>${esc(T("role." + g.role))} — ${esc(g.languageName)} (${esc(g.language)})</li>`,
      )
      .join("");
    return `<ul style="margin:0; padding-left:1.25rem;">${items}</ul>`;
  }

  async function render() {
    statusBar.setMessage("");
    let profile;
    try {
      profile = await apiFetch("/api/v1/profile");
    } catch (e) {
      statusBar.setMessage(e.message, true);
      return;
    }

    root.innerHTML = `
      <h1>${esc(T("profile.title"))}</h1>

      <div class="card" style="margin-bottom:1.5rem;">
        <p class="meta" style="margin-bottom:1rem;">${esc(T("profile.signed_in_as"))} <strong>${esc(BOOT.username)}</strong></p>
        <h2 style="margin-top:0;">${esc(T("profile.roles_heading"))}</h2>
        ${renderGrants(profile.grants)}
      </div>

      <div class="card" style="margin-bottom:1.5rem;">
        <h2 style="margin-top:0;">${esc(T("profile.language_heading"))}</h2>
        <form id="localeForm" style="display:flex; gap:1rem; align-items:flex-end;">
          <div style="flex:1;">
            <select id="locale" style="margin-bottom:0;">
              ${BOOT.locales.map((l) => `<option value="${esc(l.Code)}"${l.Code === profile.locale ? " selected" : ""}>${esc(l.Name)}</option>`).join("")}
            </select>
          </div>
          <pf-button variant="primary" type="submit">${esc(T("profile.language_save"))}</pf-button>
        </form>
      </div>

      <div class="form-card">
        <h2 style="margin-top:0;">${esc(T("profile.password_heading"))}</h2>
        <form id="passwordForm">
          <input type="password" id="current-password" placeholder="${esc(T("profile.current_password"))}" required>
          <input type="password" id="new-password" placeholder="${esc(T("profile.new_password"))}" required minlength="8">
          <input type="password" id="confirm-password" placeholder="${esc(T("profile.confirm_password"))}" required minlength="8">
          <pf-button variant="primary" type="submit">${esc(T("profile.change_password"))}</pf-button>
        </form>
      </div>
    `;

    document.getElementById("localeForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const locale = document.getElementById("locale").value;
      try {
        await apiFetch("/api/v1/profile/locale", { method: "POST", body: JSON.stringify({ locale }) });
        // No reload — re-fetch the whole shell's bootstrap and let shell.js
        // refresh the chrome and re-render the open section (this one).
        // See this file's header comment and the spec's rationale.
        const newBootstrap = await apiFetch("/api/v1/app-bootstrap");
        await window.pfRefreshAppBootstrap(newBootstrap);
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });

    document.getElementById("passwordForm").addEventListener("submit", async (e) => {
      e.preventDefault();
      const payload = {
        currentPassword: document.getElementById("current-password").value,
        newPassword: document.getElementById("new-password").value,
        confirmPassword: document.getElementById("confirm-password").value,
      };
      // Never re-populate password fields, success or failure — matches the
      // original server-rendered form, which also always started blank.
      document.getElementById("passwordForm").reset();
      try {
        await apiFetch("/api/v1/profile/password", { method: "POST", body: JSON.stringify(payload) });
        statusBar.setMessage(T("profile.password_changed"));
      } catch (err) {
        statusBar.setMessage(err.message, true);
      }
    });
  }

  window.pfSections.profile = {
    setBootstrap(b) { BOOT = b; },
    show: render,
  };
})();
