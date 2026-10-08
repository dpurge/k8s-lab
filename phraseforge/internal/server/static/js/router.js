// router.js: hash routing for the unified SPA shell — see
// specs/features/phraseforge-hash-navigation.md. A route is
// {section, id, page, tag}, written as #/<section>[/<id>][?page=N&tag=T].
// On a view route, page/tag are the list context the item was opened from,
// so a view's back link can return to the same list page. parse/build are
// pure (no window access) so they are unit-tested directly; only
// navigate/replace/start touch the browser.
(function (root) {
  function parse(hash) {
    const route = { section: "", id: null, page: 1, tag: "" };
    const text = String(hash || "").replace(/^#\/?/, "");
    const [pathPart, queryPart = ""] = text.split("?", 2);
    const [section, idPart] = pathPart.split("/");
    route.section = section || "";
    if (idPart && /^\d+$/.test(idPart)) route.id = Number(idPart);
    const query = new URLSearchParams(queryPart);
    const page = Number(query.get("page"));
    if (Number.isInteger(page) && page > 1) route.page = page;
    route.tag = query.get("tag") || "";
    return route;
  }

  function build(route) {
    let hash = "#/" + route.section;
    if (route.id != null) hash += "/" + route.id;
    const query = new URLSearchParams();
    if (route.page && route.page > 1) query.set("page", String(route.page));
    if (route.tag) query.set("tag", route.tag);
    const queryText = query.toString();
    return queryText ? hash + "?" + queryText : hash;
  }

  const listeners = [];

  function notify() {
    const route = parse(root.location.hash);
    for (const fn of listeners) fn(route);
  }

  // navigate adds a history entry; the hashchange listener renders it.
  // Assigning the hash it already has fires no hashchange, so that case
  // renders directly — e.g. saving an item whose view URL is already shown.
  function navigate(route) {
    const hash = build(route);
    if (root.location.hash === hash) notify();
    else root.location.hash = hash;
  }

  // replace rewrites the current entry (e.g. clamping an out-of-range page)
  // and renders it directly, since replaceState fires no hashchange.
  function replace(route) {
    root.history.replaceState(null, "", build(route));
    notify();
  }

  // setUrl rewrites the current entry without rendering, for a screen that
  // is already showing and only needs its URL to name it (e.g. a new list's
  // Manage form, which has no URL of its own, shown under the new item's).
  function setUrl(route) {
    root.history.replaceState(null, "", build(route));
  }

  // start registers the single render callback and renders the current hash.
  function start(fn) {
    listeners.push(fn);
    root.addEventListener("hashchange", notify);
    notify();
  }

  root.pfRouter = { parse, build, navigate, replace, setUrl, start };
})(typeof window !== "undefined" ? window : globalThis);
