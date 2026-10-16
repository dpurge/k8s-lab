// Formatting shared by the components: durations and times. Everything that
// reaches the page goes through textContent, so these return plain strings.
(function () {
  const DASH = "—";

  function pad2(n) {
    return String(n).padStart(2, "0");
  }

  // duration formats a number of seconds for reading: "0.4 s", "12 s",
  // "3 min 05 s", "1 h 02 min". null, undefined and NaN give a dash.
  function duration(seconds) {
    if (seconds === null || seconds === undefined || typeof seconds !== "number" || Number.isNaN(seconds)) return DASH;
    const s = Math.max(0, seconds);
    const tenths = Math.round(s * 10) / 10;
    if (tenths < 10) return `${tenths.toFixed(1)} s`;
    const total = Math.round(s);
    if (total < 60) return `${total} s`;
    if (total < 3600) return `${Math.floor(total / 60)} min ${pad2(total % 60)} s`;
    return `${Math.floor(total / 3600)} h ${pad2(Math.floor((total % 3600) / 60))} min`;
  }

  // time formats an ISO timestamp in the reader's locale. Options (locale,
  // timeZone) exist so that tests need not depend on the machine they run on.
  // With short the seconds are left out, for a table where width is scarce.
  function time(iso, { locale, timeZone, short } = {}) {
    if (!iso) return DASH;
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return DASH;
    return date.toLocaleString(locale, { dateStyle: "medium", timeStyle: short ? "short" : "medium", timeZone });
  }

  // clock formats a Date as HH:MM:SS.
  function clock(date, { timeZone } = {}) {
    return date.toLocaleTimeString("en-GB", { hourCycle: "h23", timeZone });
  }

  // tokens formats a token count with thousands separators; no value is a dash.
  function tokens(n) {
    if (typeof n !== "number" || Number.isNaN(n)) return DASH;
    return Math.round(n).toLocaleString("en-US");
  }

  // cost formats US dollars: cents from a dollar up, four decimals below it so
  // that a small call still shows what it cost. No value is a dash.
  function cost(usd) {
    if (typeof usd !== "number" || Number.isNaN(usd)) return DASH;
    if (usd === 0) return "$0.00";
    if (usd < 0.0001) return "<$0.0001";
    return usd >= 1 ? `$${usd.toFixed(2)}` : `$${usd.toFixed(4)}`;
  }

  window.asFormat = { duration, time, clock, tokens, cost, DASH };
})();
