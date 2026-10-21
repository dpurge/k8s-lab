// Number and time formatting the data tables share, so every app shows a token
// count, a cost or a duration the same way. Each returns a plain string; a
// missing or invalid value is a dash.
export const DASH = "—";

// tokens formats a token count with thousands separators.
export function tokens(n: unknown): string {
  if (typeof n !== "number" || Number.isNaN(n)) return DASH;
  return Math.round(n).toLocaleString("en-US");
}

// cost formats US dollars: cents from a dollar up, four decimals below it so
// that a small call still shows what it cost.
export function cost(usd: unknown): string {
  if (typeof usd !== "number" || Number.isNaN(usd)) return DASH;
  if (usd === 0) return "$0.00";
  if (usd < 0.0001) return "<$0.0001";
  return usd >= 1 ? `$${usd.toFixed(2)}` : `$${usd.toFixed(4)}`;
}

// duration formats milliseconds for reading: "0.4 s", "12 s", "3 min 05 s",
// "1 h 02 min".
export function duration(ms: unknown): string {
  if (typeof ms !== "number" || Number.isNaN(ms)) return DASH;
  const seconds = Math.max(0, ms / 1000);
  const tenths = Math.round(seconds * 10) / 10;
  if (tenths < 10) return `${tenths.toFixed(1)} s`;
  const total = Math.round(seconds);
  if (total < 60) return `${total} s`;
  const pad = (n: number) => String(n).padStart(2, "0");
  if (total < 3600) return `${Math.floor(total / 60)} min ${pad(total % 60)} s`;
  return `${Math.floor(total / 3600)} h ${pad(Math.floor((total % 3600) / 60))} min`;
}

// compactTime is a date and a time without the year or the seconds, in the
// reader's locale; a table puts the full timestamp in the cell's tooltip.
export function compactTime(iso: unknown): string {
  const date = new Date(String(iso ?? ""));
  if (!iso || Number.isNaN(date.getTime())) return DASH;
  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

// fullTime is the whole timestamp, for a tooltip or a cell with room.
export function fullTime(iso: unknown): string {
  const date = new Date(String(iso ?? ""));
  if (!iso || Number.isNaN(date.getTime())) return DASH;
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "medium" });
}
