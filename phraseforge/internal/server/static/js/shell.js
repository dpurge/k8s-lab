// shell.js: owns section switching for the unified SPA shell — see
// specs/features/phraseforge-spa-unified-shell.md. Loaded LAST (after all
// six section scripts, in app.html) so every window.pfSections.x
// registration has already happened by the time this runs.
//
// Real risk found during implementation, not anticipated in the spec: all
// six sections' DOM subtrees now coexist in one page (each hidden via
// style.display when inactive), and several sections' own forms reuse the
// same element ids (#language, #script, #field-source, #translateBtn,
// #translation-target, ...) — editor.js and layout.html's own
// phraseforgeGenerate() all use plain document.getElementById() for these,
// which is only unambiguous if at most ONE section's markup contains that
// id at a time. Left unaddressed, visiting two sections' edit forms in one
// session would leave two elements with the same id in the document
// (one hidden, one visible), and getElementById would silently return
// whichever is FIRST in document order — not necessarily the visible one.
// Fixed by clearing a section's container's innerHTML the moment you
// switch away from it: every section already re-renders itself fully via
// root.innerHTML on its own show(), so nothing is lost by doing this, and
// it keeps at most one section's markup in the DOM at any time.
(function () {
  let chrome = window.__PF_APP_BOOTSTRAP__.chrome;
  const T = (key) => (chrome.i18n && chrome.i18n[key]) || key;

  const SECTION_IDS = ["texts", "dialogs", "vocabulary", "models", "tasks", "usage", "config", "profile"];
  // The main menu (in the top bar) is Documents, then Tasks, Usage and Config for
  // an admin — Profile is deliberately not one of its items (it's the header's
  // username quick-link only). Documents groups the four content sections, which
  // are the pages of the sidebar's list; the language filter above that list
  // narrows them.
  const DOCUMENT_SECTIONS = ["texts", "dialogs", "vocabulary", "models"];
  const MENU_SECTIONS = ["tasks", "usage", "config"];

  // navIdFor is the menu item a section belongs to ("" for Profile).
  function navIdFor(section) {
    if (DOCUMENT_SECTIONS.includes(section)) return "documents";
    return MENU_SECTIONS.includes(section) ? section : "";
  }

  // The content section the Documents item opens: the one the reader was last on.
  let lastDocumentSection = "texts";
  let activeSection = null;
  let languageFilter = localStorage.getItem("phraseforge-language") || "";
  let mainNav = null;

  function isKnownSection(id) {
    return (
      SECTION_IDS.includes(id) &&
      (id !== "config" || chrome.navFlags.isAdmin) &&
      (id !== "tasks" || chrome.navFlags.isAdmin) &&
      (id !== "usage" || chrome.navFlags.isAdmin) &&
      !!window.pfSections[id]
    );
  }

  // Sections whose show() takes the whole route (specs/features/phraseforge-
  // hash-navigation.md). The others (admin, profile) are called bare.
  const ROUTED_SECTIONS = ["texts", "dialogs", "vocabulary", "models", "tasks", "usage", "config"];

  function sectionOpts(route) {
    return ROUTED_SECTIONS.includes(route.section) ? route : undefined;
  }

  // renderRoute is the router's single render callback: the URL hash is the
  // source of truth for the active section, so there is no sessionStorage
  // copy of it any more. An empty or unknown hash is rewritten to #/texts.
  function renderRoute(route) {
    currentRoute = route;
    if (!isKnownSection(route.section)) {
      window.pfRouter.replace({ section: "texts" });
      return;
    }
    showSection(route.section, sectionOpts(route));
  }

  // The route being shown: a section that lists its own pages in the sidebar
  // needs to know which one is current.
  let currentRoute = { section: "" };

  // opts (optional) is forwarded verbatim to the target section's own
  // show(opts). Renders only — URL changes go through pfRouter.navigate.
  function showSection(id, opts) {
    if (activeSection && activeSection !== id) {
      const prevEl = document.getElementById(activeSection + "-app");
      if (prevEl) prevEl.innerHTML = "";
    }
    activeSection = id;
    for (const s of SECTION_IDS) {
      const el = document.getElementById(s + "-app");
      if (el) el.style.display = s === id ? "" : "none";
    }
    if (DOCUMENT_SECTIONS.includes(id)) lastDocumentSection = id;
    if (mainNav) mainNav.active = navIdFor(id);
    buildSidebar();
    const mod = window.pfSections[id];
    if (mod && mod.show) mod.show(opts);
  }
  // Kept for the existing cross-section links (texts/dialogs -> vocabulary/
  // models); now a navigation to #/<id>/<viewId> rather than a direct render.
  window.pfShowSection = (id, opts) =>
    window.pfRouter.navigate({ section: id, id: opts && opts.viewId != null ? opts.viewId : null });
  window.pfGetLanguageFilter = () => languageFilter;

  function setLanguageFilter(value) {
    languageFilter = value;
    localStorage.setItem("phraseforge-language", value);
    // Back to the section's first list page: a page/tag from the old filter
    // wouldn't mean the same thing under the new one.
    window.pfRouter.navigate({ section: activeSection });
  }

  // The sidebar element is kept here because it leaves the page while it is
  // empty (see buildSidebar).
  const sidebar = document.getElementById("sidebar");

  function buildSidebar() {
    if (!sidebar) return;
    sidebar.innerHTML = "";

    // The Usage and Config sections list their own pages here.
    const mod = window.pfSections[activeSection];
    if (mod && mod.sidebarNav) sidebar.appendChild(mod.sidebarNav(currentRoute));

    // The content sections: the language filter, then the four of them.
    if (DOCUMENT_SECTIONS.includes(activeSection) && chrome.navFlags.viewableLanguages && chrome.navFlags.viewableLanguages.length) {
      const select = document.createElement("select");
      select.id = "language-filter";
      const allOption = document.createElement("option");
      allOption.value = "";
      allOption.textContent = T("nav.all_languages");
      select.appendChild(allOption);
      for (const l of chrome.navFlags.viewableLanguages) {
        const option = document.createElement("option");
        option.value = l.Code;
        option.textContent = l.Name;
        if (l.Code === languageFilter) option.selected = true;
        select.appendChild(option);
      }
      select.addEventListener("change", (e) => setLanguageFilter(e.target.value));
      sidebar.appendChild(select);
    }
    if (DOCUMENT_SECTIONS.includes(activeSection)) {
      const pages = document.createElement("dp-tabs");
      pages.setAttribute("orientation", "vertical");
      pages.setAttribute("label", T("nav.documents"));
      pages.items = DOCUMENT_SECTIONS.map((id) => ({ id, label: T("nav." + id) }));
      pages.active = activeSection;
      pages.addEventListener("dp-tabs-select", (e) => window.pfRouter.navigate({ section: e.detail.id }));
      sidebar.appendChild(pages);
    }

    // dp-shell draws a sidebar for as long as it has a child in that slot, so
    // an empty one is taken out and put back when there is something to show.
    const shell = document.querySelector("dp-shell");
    if (sidebar.childElementCount && !sidebar.isConnected) shell.appendChild(sidebar);
    else if (!sidebar.childElementCount) sidebar.remove();
  }

  // buildMainNav fills the menu in the top bar: Documents for every user, then
  // Tasks, Usage and Config for an admin.
  function buildMainNav() {
    mainNav = document.getElementById("mainNav");
    if (!mainNav) return;
    const items = [{ id: "documents", label: T("nav.documents") }];
    if (chrome.navFlags.isAdmin) items.push({ id: "tasks", label: T("nav.tasks") }, { id: "usage", label: T("nav.usage") }, { id: "config", label: T("nav.config") });
    mainNav.items = items;
    mainNav.setAttribute("label", T("nav.main"));
    mainNav.active = navIdFor(activeSection);
    if (!mainNav.dataset.wired) {
      mainNav.dataset.wired = "1";
      mainNav.addEventListener("dp-tabs-select", (e) =>
        window.pfRouter.navigate({ section: e.detail.id === "documents" ? lastDocumentSection : e.detail.id }),
      );
    }
  }

  function buildHeader() {
    const shell = document.querySelector("dp-shell");
    if (shell) shell.setAttribute("sidebar-label", T("sidebar.toggle"));
    const userLink = document.getElementById("userLink");
    if (userLink) userLink.textContent = chrome.username;
    const logoutLink = document.getElementById("logoutLink");
    if (logoutLink) logoutLink.textContent = T("nav.logout");
  }

  const userLinkEl = document.getElementById("userLink");
  if (userLinkEl) {
    userLinkEl.addEventListener("click", (e) => {
      e.preventDefault();
      window.pfRouter.navigate({ section: "profile" });
    });
  }

  // Called by profile-app.js after a successful locale change — see
  // specs/features/phraseforge-spa-unified-shell.md's rationale for why
  // this refreshes the whole shell (chrome plus every section's own
  // bootstrap/i18n) instead of doing a page reload the way
  // phraseforge-spa-profile originally did.
  window.pfRefreshAppBootstrap = async function (newBootstrap) {
    window.__PF_APP_BOOTSTRAP__ = newBootstrap;
    chrome = newBootstrap.chrome;
    for (const s of SECTION_IDS) {
      const mod = window.pfSections[s];
      if (mod && mod.setBootstrap && newBootstrap[s]) mod.setBootstrap(newBootstrap[s]);
    }
    buildHeader();
    buildMainNav();
    buildSidebar();
    renderRoute(window.pfRouter.parse(window.location.hash));
  };

  buildHeader();
  buildMainNav();
  buildSidebar();
  window.pfRouter.start(renderRoute);
})();
