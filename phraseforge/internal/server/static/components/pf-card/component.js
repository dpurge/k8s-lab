// pf-card: a light-DOM wrapper that contributes only CSS chrome
// (background/border/radius/padding/shadow) around whatever real content
// the Go template already rendered inside it — a title link, badges, tag
// links, a meta line. Deliberately NOT a port of knowledge's kb-card:
// kb-card wraps its entire card as a click target and dispatches a
// kb-card-select event because knowledge's items have no real per-item
// URL (a client-side SPA). Phraseforge's cards already have a real
// <h2><a href="..."> inside them — only the title is the click target, and
// it already works with JS disabled. Layering a whole-card JS click
// interceptor on top would be a regression, not an improvement (same
// reasoning as pf-nav vs kb-nav in phraseforge-pf-components-foundation).
// pf-card never reads, moves, or listens on its children — no parse-order
// hazard to guard against (unlike pf-button/pf-field).
(function () {
  const STYLE_ID = "pf-card-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/pf-card/component.css";
    document.head.appendChild(link);
  }

  class PfCard extends HTMLElement {
    connectedCallback() {
      ensureStyle();
    }
  }

  customElements.define("pf-card", PfCard);
})();
