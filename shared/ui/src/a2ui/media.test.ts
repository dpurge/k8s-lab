import { test } from "node:test";
import assert from "node:assert/strict";
import { checkMediaUrl, setMediaHosts } from "./media.ts";
import { components, create, data, draw, settle } from "./testkit.ts";
import { dpCatalog } from "./catalog.ts";

const ORIGIN = "https://assistant.example.org";

test("nothing outside this origin and data: is allowed by default", () => {
  setMediaHosts([]);
  assert.deepEqual(checkMediaUrl("https://assistant.example.org/a.png", "image", ORIGIN), { ok: true, url: "https://assistant.example.org/a.png" });
  assert.deepEqual(checkMediaUrl("/static/a.png", "image", ORIGIN), { ok: true, url: "https://assistant.example.org/static/a.png" });
  assert.equal(checkMediaUrl("https://evil.example.net/a.png", "image", ORIGIN).ok, false);
  assert.equal(checkMediaUrl("http://evil.example.net/a.png", "image", ORIGIN).ok, false);
});

test("data: URLs are allowed only for their own kind", () => {
  assert.equal(checkMediaUrl("data:image/png;base64,AAAA", "image", ORIGIN).ok, true);
  assert.equal(checkMediaUrl("data:video/mp4;base64,AAAA", "video", ORIGIN).ok, true);
  assert.equal(checkMediaUrl("data:audio/mpeg;base64,AAAA", "audio", ORIGIN).ok, true);
  for (const [url, kind] of [["data:text/html,<script>alert(1)</script>", "image"], ["data:image/png;base64,AAAA", "video"], ["data:application/javascript,1", "audio"]] as const) {
    assert.equal(checkMediaUrl(url, kind, ORIGIN).ok, false, url);
  }
});

test("a configured host is allowed over https only, with its port, and only that host", () => {
  setMediaHosts(["Images.Example.com", "media.example.com:8443"]);
  assert.equal(checkMediaUrl("https://images.example.com/x.png", "image", ORIGIN).ok, true);
  assert.equal(checkMediaUrl("https://IMAGES.example.com/x.png", "image", ORIGIN).ok, true);
  assert.equal(checkMediaUrl("https://media.example.com:8443/x.mp4", "video", ORIGIN).ok, true);
  assert.equal(checkMediaUrl("https://media.example.com/x.mp4", "video", ORIGIN).ok, false, "another port");
  assert.equal(checkMediaUrl("http://images.example.com/x.png", "image", ORIGIN).ok, false, "plain http");
  assert.equal(checkMediaUrl("https://images.example.com.evil.net/x.png", "image", ORIGIN).ok, false, "a longer host");
  assert.equal(checkMediaUrl("https://sub.images.example.com/x.png", "image", ORIGIN).ok, false, "a subdomain");
  assert.equal(checkMediaUrl("https://images.example.com@evil.net/x.png", "image", ORIGIN).ok, false, "userinfo trick");
  setMediaHosts([]);
});

test("other schemes, empty and malformed addresses are refused with a reason", () => {
  for (const url of ["javascript:alert(1)", "file:///etc/passwd", "blob:https://assistant.example.org/1", "ftp://x/y", "", "   ", "http://", undefined, 42]) {
    const result = checkMediaUrl(url, "image", ORIGIN);
    assert.equal(result.ok, false, String(url));
    assert.ok(!result.ok && result.reason.length > 0);
  }
});

// ── the components ───────────────────────────────────────────────────────────

const one = (c: Record<string, unknown>) => [create("s"), components("s", [{ id: "root", ...c }])];

test("an Image from data: or this origin draws an img with its description, and the policy attributes", async () => {
  const { el } = await draw(one({ component: "Image", url: "data:image/png;base64,iVBORw0KGgo=", description: "A dot", fit: "cover", variant: "avatar" }), "s");
  await settle(el);
  const img = el.querySelector("img")!;
  assert.equal(img.getAttribute("alt"), "A dot");
  assert.equal(img.getAttribute("referrerpolicy"), "no-referrer");
  assert.ok(img.classList.contains("avatar") && img.classList.contains("fit-cover"));
  const same = await draw(one({ component: "Image", url: "/static/x.png" }), "s");
  await settle(same.el);
  assert.equal(same.el.querySelector("img")!.getAttribute("src"), `${location.origin}/static/x.png`);
});

test("an Image from a host that is not allowed draws a note with the reason, not an img", async () => {
  setMediaHosts([]);
  const { el } = await draw(one({ component: "Image", url: "https://tracker.example.net/pixel.png", description: "Logo" }), "s");
  await settle(el);
  assert.equal(el.querySelector("img"), null);
  assert.match(el.querySelector(".note")!.textContent!, /Image not shown: the host tracker\.example\.net is not allowed/);
  assert.equal(el.querySelector(".note")!.getAttribute("aria-label"), "Logo");
  setMediaHosts(["tracker.example.net"]);
  const allowed = await draw(one({ component: "Image", url: "https://tracker.example.net/pixel.png" }), "s");
  await settle(allowed.el);
  assert.ok(allowed.el.querySelector("img"));
  setMediaHosts([]);
});

test("an Image that fails to load turns into a note", async () => {
  const { el } = await draw(one({ component: "Image", url: "/static/missing.png" }), "s");
  await settle(el);
  el.querySelector("img")!.dispatchEvent(new Event("error"));
  await settle(el);
  assert.equal(el.querySelector("img"), null);
  assert.match(el.querySelector(".note")!.textContent!, /could not be loaded/);
});

test("a javascript: address never reaches an element", async () => {
  const { el } = await draw(one({ component: "Image", url: "javascript:alert(1)" }), "s");
  await settle(el);
  assert.equal(el.querySelector("img"), null);
  assert.match(el.querySelector(".note")!.textContent!, /scheme is not allowed/);
});

test("a Video has controls and never autoplays; a blocked poster is explained and not used", async () => {
  const { el } = await draw(one({ component: "Video", url: "data:video/mp4;base64,AAAA", posterUrl: "https://evil.example.net/p.png" }), "s");
  await settle(el);
  const video = el.querySelector("video")!;
  assert.equal(video.hasAttribute("controls"), true);
  assert.equal(video.hasAttribute("autoplay"), false);
  assert.equal(video.hasAttribute("poster"), false);
  assert.match(el.querySelector(".caption")!.textContent!, /Poster image not shown/);
  const blocked = await draw(one({ component: "Video", url: "https://evil.example.net/v.mp4" }), "s");
  await settle(blocked.el);
  assert.equal(blocked.el.querySelector("video"), null);
  assert.match(blocked.el.querySelector(".note")!.textContent!, /Video not shown/);
});

test("an AudioPlayer shows its description and a native player, or a note", async () => {
  const { el } = await draw(one({ component: "AudioPlayer", url: "/static/a.mp3", description: "Episode 1" }), "s");
  await settle(el);
  assert.equal(el.querySelector(".description")!.textContent, "Episode 1");
  assert.equal(el.querySelector("audio")!.hasAttribute("autoplay"), false);
  const blocked = await draw(one({ component: "AudioPlayer", url: "https://evil.example.net/a.mp3" }), "s");
  await settle(blocked.el);
  assert.equal(blocked.el.querySelector("audio"), null);
  assert.match(blocked.el.querySelector(".note")!.textContent!, /Audio not shown/);
});

test("the catalog now implements all 18 basic components and none more", () => {
  assert.deepEqual(
    [...dpCatalog.components.keys()].sort(),
    ["AudioPlayer", "Button", "Card", "CheckBox", "ChoicePicker", "Column", "DateTimeInput", "Divider", "Icon", "Image", "List", "Modal", "Row", "Slider", "Tabs", "Text", "TextField", "Video"],
  );
  void data;
});
