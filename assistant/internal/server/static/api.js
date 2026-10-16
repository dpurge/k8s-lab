// The admin interface's only contact with the server: the JSON API. Errors
// carry the server's own message ({"error": {"message"}}) when there is one.
(function () {
  async function errorMessage(res) {
    try {
      const body = await res.json();
      if (body && body.error && typeof body.error.message === "string") return body.error.message;
    } catch (e) {
      /* the body was not the API's error shape */
    }
    return `HTTP ${res.status}`;
  }

  async function getJSON(url) {
    const res = await fetch(url, { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error(await errorMessage(res));
    return res.json();
  }

  // loadUsers returns a Map from user id to display name.
  async function loadUsers() {
    const body = await getJSON("/api/v1/users");
    return new Map((body.items || []).map((u) => [u.id, u.display_name]));
  }

  function shortId(id) {
    return String(id).slice(0, 8);
  }

  window.asApi = { getJSON, loadUsers, errorMessage, shortId };
})();
