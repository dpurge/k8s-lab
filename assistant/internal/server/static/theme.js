(function () {
  var KEY = "as-theme";

  // The toggle shows what clicking switches to: a crescent moon in light mode,
  // a sun in dark mode.
  function setIcon() {
    var icon = document.getElementById("theme-icon");
    if (icon) icon.textContent = document.documentElement.getAttribute("data-theme") === "dark" ? "☀" : "☽";
  }

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    setIcon();
  }

  function saved() {
    try {
      var v = localStorage.getItem(KEY);
      return v === "dark" || v === "light" ? v : null;
    } catch (e) {
      return null; // storage can be blocked; the theme then follows the system
    }
  }

  // Runs in <head>, so the colors are right on first paint.
  apply(saved() || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
  document.addEventListener("DOMContentLoaded", setIcon);

  window.toggleTheme = function () {
    var next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    apply(next);
    try {
      localStorage.setItem(KEY, next);
    } catch (e) {
      /* not remembered, still applied */
    }
  };
})();
