(function () {
  var KEY = "kb-theme";

  // The toggle shows what clicking switches to, like phraseforge's:
  // crescent moon in light mode, sun in dark mode.
  function setIcon() {
    var icon = document.getElementById("theme-icon");
    if (icon) {
      icon.textContent = document.documentElement.getAttribute("data-theme") === "dark" ? "☀" : "☽";
    }
  }

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    setIcon();
  }

  // Runs in <head>, so the colors are right on first paint; the icon span
  // doesn't exist yet and is filled once the document is parsed.
  var saved = localStorage.getItem(KEY);
  var initial = saved === "dark" || saved === "light" ? saved : matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  apply(initial);
  document.addEventListener("DOMContentLoaded", setIcon);

  window.toggleTheme = function () {
    var next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    apply(next);
    localStorage.setItem(KEY, next);
  };
})();
