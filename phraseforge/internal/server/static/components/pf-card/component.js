// pf-card: a light-DOM wrapper that contributes CSS chrome
// (background/border/radius/padding/shadow) around whatever real content
// the page already rendered inside it — a title link, badges, tag links, a
// meta line. Deliberately NOT a port of knowledge's kb-card, which has no
// real per-item URL and dispatches a select event.
//
// One behavior: a click on the card body that is not on a link, button or
// form control is forwarded to the card's [data-open] link, so on a touch
// screen the whole tile opens the item and not just the title text. The
// real title link stays in place (keyboard, middle-click, screen readers),
// and a card without [data-open] does nothing. pf-card never reads, moves,
// or rewrites its children — no parse-order hazard to guard against
// (unlike pf-button/pf-field).
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
      if (this.hasOpenListener) return;
      this.hasOpenListener = true;
      this.addEventListener("click", (e) => {
        if (e.target.closest("a, button, input, select, textarea, label")) return;
        const selection = window.getSelection && window.getSelection().toString();
        if (selection) return;
        const link = this.querySelector("[data-open]");
        if (link) link.click();
      });
    }
  }

  customElements.define("pf-card", PfCard);
})();
