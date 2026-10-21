import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync } from "node:fs";
import { mount } from "../../testenv.ts";
import "../../components/dp-card/component.ts";
import { examples } from "../examples.ts";
import { DpA2uiGallery } from "./component.ts";

test("every fixture file is in the gallery's list", () => {
  const files = readdirSync(new URL("../fixtures/", import.meta.url)).filter((name) => name.endsWith(".json"));
  assert.equal(examples.length, files.length);
});

test("the gallery draws each example with its messages beside it", async () => {
  const el = await mount<DpA2uiGallery>("<dp-a2ui-gallery></dp-a2ui-gallery>");
  await new Promise((resolve) => setTimeout(resolve, 20));
  await el.updateComplete;
  const cards = el.shadowRoot!.querySelectorAll(":host > dp-card, dp-card[heading]");
  assert.equal(cards.length, examples.length);
  assert.equal(el.shadowRoot!.querySelectorAll("dp-a2ui-surface").length, examples.length);
  const messages = el.shadowRoot!.querySelector("pre")!.textContent!;
  assert.match(messages, /createSurface/);
  assert.equal(el.shadowRoot!.querySelectorAll(".sent").length, examples.length);
});
