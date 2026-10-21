// Which URLs an agent-composed interface may load media from.
//
// An interface comes from a model, so a URL in it is untrusted: loading one
// tells its host that the user is looking at this page, and an address with a
// scheme like javascript: or file: must never reach an element. Allowed:
//   - data: URLs of the matching kind (data:image/... for an image),
//   - this page's own origin (http or https),
//   - https URLs on a configured host (the server's ASSISTANT_A2UI_MEDIA_HOSTS,
//     which its Content-Security-Policy also enforces; the page sets them with
//     setMediaHosts). Nothing is allowed by default.
// The check lets the component say why a file is not shown; the policy is what
// actually stops the browser from fetching it.

export type MediaKind = "image" | "video" | "audio";
export type MediaCheck = { ok: true; url: string } | { ok: false; reason: string };

let allowedHosts: string[] = [];

export const setMediaHosts = (hosts: readonly string[]) => {
  allowedHosts = hosts.map((host) => host.toLowerCase());
};

export function checkMediaUrl(raw: unknown, kind: MediaKind, origin: string = globalThis.location?.origin ?? ""): MediaCheck {
  if (typeof raw !== "string" || raw.trim() === "") return { ok: false, reason: "no address" };
  const text = raw.trim();
  if (/^data:/i.test(text)) {
    return new RegExp(`^data:${kind}/`, "i").test(text) ? { ok: true, url: text } : { ok: false, reason: `a data: URL that is not ${kind} data` };
  }
  let url: URL;
  try {
    url = new URL(text, origin || undefined);
  } catch {
    return { ok: false, reason: "not a valid address" };
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return { ok: false, reason: `the ${url.protocol} scheme is not allowed` };
  if (url.username || url.password) return { ok: false, reason: "an address with a user name or password is not allowed" };
  if (origin && url.origin === origin) return { ok: true, url: url.href };
  if (url.protocol === "https:" && allowedHosts.includes(url.host.toLowerCase())) return { ok: true, url: url.href };
  return { ok: false, reason: url.protocol === "http:" ? "only https is allowed from another host" : `the host ${url.host} is not allowed` };
}
