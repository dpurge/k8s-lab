// The maps and child rendering Row, Column and List share.
import { html } from "lit";
import { repeat } from "lit/directives/repeat.js";
import type { A2uiChildRef } from "@a2ui/web_core/v1_0";

export const JUSTIFY = {
  start: "flex-start",
  center: "center",
  end: "flex-end",
  spaceBetween: "space-between",
  spaceAround: "space-around",
  spaceEvenly: "space-evenly",
  stretch: "stretch",
} as const;

export const ALIGN = {
  start: "flex-start",
  center: "center",
  end: "flex-end",
  stretch: "stretch",
} as const;

// A template child is one component drawn once per list item, so its id repeats
// and the data path tells the copies apart.
export const childKey = (child: A2uiChildRef) =>
  typeof child === "object" ? `${child.basePath ?? ""}/${child.id}` : String(child);

export const renderChildren = (
  children: unknown,
  renderNode: (child: A2uiChildRef) => unknown,
  wrap: (content: unknown) => unknown = (content) => content,
) => {
  const list = Array.isArray(children) ? (children as A2uiChildRef[]) : [];
  return html`${repeat(list, childKey, (child) => wrap(renderNode(child)))}`;
};
