// kb-card: the clickable card wrapper duplicated across renderItem(),
// renderDraft(), and renderChat() as a ".item"/".chat" CSS class plus an
// inline onclick="run(() => open...(id))" handler on each rendered div.
// kb-card owns the styling and dispatches one event; the list container
// (e.g. #list) gets a single delegated listener instead of one inline
// handler per card.
(function () {
  const STYLE_ID = "kb-card-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/components/kb-card/component.css";
    document.head.appendChild(link);
  }

  class KbCard extends HTMLElement {
    connectedCallback() {
      ensureStyle();
      if (this._wired) return;
      this._wired = true;
      this.addEventListener("click", () => {
        this.dispatchEvent(new CustomEvent("kb-card-select", { detail: { id: this.dataset.id }, bubbles: true }));
      });
    }
  }

  customElements.define("kb-card", KbCard);
})();
