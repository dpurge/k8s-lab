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

  const SECTION_IDS = ["texts", "dialogs", "vocabulary", "models", "admin", "jobs", "profile"];
  // Sidebar tabs — Profile is deliberately not one of them (it's the
  // header's username quick-link only, matching today's layout, where
  // Profile isn't in the sidebar either).
  const NAV_SECTIONS = ["texts", "dialogs", "vocabulary", "models", "admin", "jobs"];

  let activeSection = null;
  let languageFilter = localStorage.getItem("phraseforge-language") || "";
  let sidebarTabs = null;

  function isKnownSection(id) {
    return (
      SECTION_IDS.includes(id) &&
      (id !== "admin" || chrome.navFlags.isAdmin) &&
      (id !== "jobs" || chrome.navFlags.isAdmin) &&
      !!window.pfSections[id]
    );
  }

  function restoreActiveSection() {
    const stored = sessionStorage.getItem("phraseforge-active-section");
    return isKnownSection(stored) ? stored : "texts";
  }

  function showSection(id) {
    if (!isKnownSection(id)) id = "texts";
    if (activeSection && activeSection !== id) {
      const prevEl = document.getElementById(activeSection + "-app");
      if (prevEl) prevEl.innerHTML = "";
    }
    activeSection = id;
    sessionStorage.setItem("phraseforge-active-section", id);
    for (const s of SECTION_IDS) {
      const el = document.getElementById(s + "-app");
      if (el) el.style.display = s === id ? "" : "none";
    }
    if (sidebarTabs) sidebarTabs.active = NAV_SECTIONS.includes(id) ? id : "";
    const mod = window.pfSections[id];
    if (mod && mod.show) mod.show();
  }
  window.pfShowSection = showSection;
  window.pfGetLanguageFilter = () => languageFilter;

  function setLanguageFilter(value) {
    languageFilter = value;
    localStorage.setItem("phraseforge-language", value);
    const mod = window.pfSections[activeSection];
    if (mod && mod.show) mod.show();
  }

  function buildSidebar() {
    const sidebar = document.getElementById("sidebar");
    if (!sidebar) return;
    sidebar.innerHTML = "";

    if (chrome.navFlags.viewableLanguages && chrome.navFlags.viewableLanguages.length) {
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

    sidebarTabs = document.createElement("pf-tabs");
    sidebarTabs.setAttribute("orientation", "vertical");
    const items = [
      { id: "texts", label: T("nav.texts") },
      { id: "dialogs", label: T("nav.dialogs") },
      { id: "vocabulary", label: T("nav.vocabulary") },
      { id: "models", label: T("nav.models") },
    ];
    if (chrome.navFlags.isAdmin) items.push({ id: "admin", label: T("nav.admin") });
    if (chrome.navFlags.isAdmin) items.push({ id: "jobs", label: T("nav.jobs") });
    sidebarTabs.items = items;
    sidebarTabs.active = NAV_SECTIONS.includes(activeSection) ? activeSection : "";
    sidebarTabs.addEventListener("pf-tabs-select", (e) => showSection(e.detail.id));
    sidebar.appendChild(sidebarTabs);
  }

  function buildHeader() {
    const sidebarToggle = document.getElementById("sidebarToggleBtn");
    if (sidebarToggle) {
      sidebarToggle.setAttribute("aria-label", T("sidebar.toggle"));
      sidebarToggle.setAttribute("title", T("sidebar.toggle"));
    }
    const userLink = document.getElementById("userLink");
    if (userLink) userLink.textContent = chrome.username;
    const logoutLink = document.getElementById("logoutLink");
    if (logoutLink) logoutLink.textContent = T("nav.logout");
  }

  const userLinkEl = document.getElementById("userLink");
  if (userLinkEl) {
    userLinkEl.addEventListener("click", (e) => {
      e.preventDefault();
      showSection("profile");
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
    buildSidebar();
    const mod = window.pfSections[activeSection];
    if (mod && mod.show) mod.show();
  };

  buildHeader();
  buildSidebar();
  showSection(restoreActiveSection());
})();
