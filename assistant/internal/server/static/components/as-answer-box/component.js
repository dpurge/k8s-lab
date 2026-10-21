// as-answer-box: what a workflow run asked the user, and a box to answer it.
// Sending posts the answer to the task (POST /api/v1/tasks/{id}/messages),
// which queues the run to go on. A refused or failed answer shows the server's
// message right next to the box and keeps what was typed.
//
// Attribute: task-id. Property: ask, the ask_user payload (an object).
// Event (bubbling): as-answered {task} after the server accepted the answer.
(function () {
  const STYLE_ID = "as-answer-box-style";

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    const link = document.createElement("link");
    link.id = STYLE_ID;
    link.rel = "stylesheet";
    link.href = "/static/components/as-answer-box/component.css";
    document.head.appendChild(link);
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // questionText is the part of the payload a person should read first.
  function questionText(ask) {
    if (ask && typeof ask === "object") {
      for (const key of ["question", "text", "prompt", "message"]) {
        if (typeof ask[key] === "string" && ask[key].trim() !== "") return ask[key];
      }
    }
    return "";
  }

  class AsAnswerBox extends HTMLElement {
    get taskId() {
      return this.getAttribute("task-id") || "";
    }

    get ask() {
      return this._ask;
    }

    set ask(value) {
      this._ask = value;
      if (this._built) this._renderAsk();
    }

    connectedCallback() {
      ensureStyle();
      this._build();
    }

    _build() {
      if (this._built) return;
      this._built = true;

      this._heading = el("h3", "as-answer-heading", "Waiting for your answer");
      this._question = el("p", "as-answer-question");
      this._raw = document.createElement("details");
      this._raw.className = "as-answer-raw";
      this._raw.append(el("summary", "", "The full question"), el("pre", "as-answer-raw-json"));

      this._text = document.createElement("textarea");
      this._text.className = "as-answer-text";
      this._text.rows = 3;
      this._text.setAttribute("aria-label", "Your answer");
      this._text.placeholder = "Type your answer";
      this._text.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          this._send();
        }
      });

      this._button = el("button", "as-answer-send", "Send");
      this._button.type = "button";
      this._button.addEventListener("click", () => this._send());
      this._error = el("p", "as-answer-error");
      this._error.setAttribute("role", "alert");

      const row = el("div", "as-answer-row");
      row.append(this._button, this._error);
      this.append(this._heading, this._question, this._raw, this._text, row);
      this._renderAsk();
    }

    _renderAsk() {
      const ask = this._ask;
      const text = questionText(ask);
      this._question.textContent = text;
      this._question.hidden = text === "";
      this._raw.querySelector("pre").textContent = ask === undefined || ask === null ? "" : JSON.stringify(ask, null, 2);
      this._raw.hidden = ask === undefined || ask === null;
      // With no readable question the raw one is the question: show it open.
      this._raw.open = text === "" && !this._raw.hidden;
    }

    _fail(message) {
      this._error.textContent = message;
      this._error.hidden = false;
    }

    async _send() {
      if (this._sending) return;
      const answer = this._text.value;
      if (answer.trim() === "") {
        this._fail("Type an answer first.");
        return;
      }
      this._sending = true;
      this._button.disabled = true;
      this._error.textContent = "";
      this._error.hidden = true;
      try {
        const res = await fetch(`/api/v1/tasks/${encodeURIComponent(this.taskId)}/messages`, {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ message: answer }),
        });
        if (!res.ok) {
          this._fail(`Could not send the answer: ${await window.asApi.errorMessage(res)}`);
          return;
        }
        const task = await res.json();
        this._text.value = "";
        this.dispatchEvent(new CustomEvent("as-answered", { detail: { task }, bubbles: true }));
      } catch (err) {
        this._fail(`Could not send the answer: ${err.message}`);
      } finally {
        this._sending = false;
        this._button.disabled = false;
      }
    }
  }

  customElements.define("as-answer-box", AsAnswerBox);
})();
