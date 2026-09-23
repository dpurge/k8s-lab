// kb-message: replaces renderMsg()/renderSource()'s string-built markup for
// a single chat transcript entry. Takes a `message` property
// `{role, contentNode, sources}` where contentNode is a DOM node/fragment
// already produced by sanitization (see app.js's md()) — never an HTML
// string — so this component only ever appendChild()s or sets textContent
// and can never innerHTML untrusted content. Uses `data-role`, not the
// `role` attribute, since `role` is a real ARIA attribute and
// role="user"/role="assistant" are not valid ARIA roles.
(function () {
  const STYLE_ID = "kb-message-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-message/component.css";
    document.head.appendChild(link);
  }

  function renderSources(sources) {
    const wrap = document.createElement("div");
    wrap.className = "kb-message-sources";

    const heading = document.createElement("b");
    heading.textContent = "Retrieved knowledge items";
    wrap.appendChild(heading);

    const list = document.createElement("ul");
    for (const source of sources) {
      const item = document.createElement("li");

      const link = document.createElement("a");
      link.href = "#";
      link.className = "kb-message-source";
      link.textContent = source.title;
      link.addEventListener("click", (event) => {
        event.preventDefault();
        link.dispatchEvent(new CustomEvent("kb-source-select", { detail: { id: source.id }, bubbles: true }));
      });
      item.appendChild(link);

      if (typeof source.score === "number") {
        const score = document.createElement("span");
        score.className = "kb-message-source-score";
        score.textContent = source.score.toFixed(3);
        item.appendChild(document.createTextNode(" "));
        item.appendChild(score);
      }

      item.appendChild(document.createElement("br"));
      item.appendChild(document.createTextNode(source.summary || ""));
      list.appendChild(item);
    }
    wrap.appendChild(list);
    return wrap;
  }

  class KbMessage extends HTMLElement {
    connectedCallback() {
      ensureStyle();
    }

    get message() {
      return this._message;
    }

    set message(value) {
      this._message = value || {};
      this._render();
    }

    _render() {
      const { role, contentNode, sources } = this._message || {};

      // Clear any previously rendered content before re-rendering — matches
      // kb-nav's own re-render pattern (textContent = "" rather than
      // innerHTML = "").
      this.textContent = "";
      this.dataset.role = role || "";

      const roleLabel = document.createElement("div");
      roleLabel.className = "kb-message-role";
      roleLabel.textContent = role || "";
      this.appendChild(roleLabel);

      if (contentNode) {
        const body = document.createElement("div");
        body.className = "kb-message-content";
        // Cloned rather than appended directly: appendChild on a
        // DocumentFragment empties it, so re-setting the same message
        // object a second time would otherwise render with no content.
        body.appendChild(contentNode.cloneNode(true));
        this.appendChild(body);
      }

      if (Array.isArray(sources) && sources.length > 0) {
        this.appendChild(renderSources(sources));
      }
    }
  }

  customElements.define("kb-message", KbMessage);
})();
