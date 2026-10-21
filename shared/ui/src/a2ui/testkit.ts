// Helpers for the renderer tests: build A2UI messages, draw them in a surface
// element, and wait for the nested elements to finish rendering.
import { mount } from "../testenv.ts";
import "../components/dp-card/component.ts";
import "../components/dp-button/component.ts";
import "../components/dp-field/component.ts";
import "../components/dp-tabs/component.ts";
import { createProcessor, type DpA2uiSurface } from "./index.ts";

export const CATALOG = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json";

export type Message = Record<string, unknown>;

export const create = (surfaceId: string): Message => ({ version: "v1.0", createSurface: { surfaceId, catalogId: CATALOG } });
export const components = (surfaceId: string, list: Record<string, unknown>[]): Message => ({
  version: "v1.0",
  updateComponents: { surfaceId, components: list },
});
export const data = (surfaceId: string, path: string, value: unknown): Message => ({
  version: "v1.0",
  updateDataModel: { surfaceId, path, value },
});

export async function draw(messages: Message[], surfaceId: string) {
  const sent: unknown[] = [];
  const processor = createProcessor((message: unknown) => {
    sent.push(message);
  });
  processor.processMessages(messages as never);
  const el = await mount<DpA2uiSurface>("<dp-a2ui-surface></dp-a2ui-surface>");
  el.surface = processor.getSurface(surfaceId) as never;
  await el.updateComplete;
  return { el, processor, sent };
}

// Lit renders child elements on their own update cycle.
export const settle = async (el: HTMLElement) => {
  for (let i = 0; i < 4; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    await Promise.all([...el.querySelectorAll("*")].map((child) => (child as { updateComplete?: Promise<unknown> }).updateComplete));
  }
};

export const text = (el: HTMLElement) => [...el.querySelectorAll(".text")].map((n) => n.textContent);
