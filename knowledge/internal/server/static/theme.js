(function () {
  var KEY = "kb-theme";

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
  }

  var saved = localStorage.getItem(KEY);
  var initial = saved === "dark" || saved === "light" ? saved : matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  apply(initial);

  window.toggleTheme = function () {
    var next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    apply(next);
    localStorage.setItem(KEY, next);
  };
})();
