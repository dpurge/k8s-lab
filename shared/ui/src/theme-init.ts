// A classic script for <head>, run before the first paint so there is no flash
// of the wrong theme: sets the data-theme attribute on <html> that tokens.css
// reads, from the remembered choice or else the system preference.
//
//   <script src="/ui/theme-init.js" data-key="myapp"></script>
//
// data-key is dp-shell's storage-key (default "dp"): the choice is read from
// localStorage under `<key>-theme`.
(function () {
  const key = (document.currentScript as HTMLScriptElement | null)?.dataset.key || "dp";
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(`${key}-theme`);
  } catch {
    // A browser that refuses storage has nothing remembered.
  }
  // Only without a remembered choice does the system preference decide.
  const dark = !stored && (window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false);
  document.documentElement.setAttribute("data-theme", stored || (dark ? "dark" : "light"));
})();
