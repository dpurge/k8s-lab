// Entry of the on-demand A2UI chunk: a page imports it only when it has an
// A2UI surface to draw, so it is never part of dp-core.
import { dpCatalog } from "./catalog.ts";
import "./dp-a2ui-surface/component.ts";

export { DpA2uiSurface } from "./dp-a2ui-surface/component.ts";
export { dpCatalog };

export { createProcessor } from "./processor.ts";
export { createClient, type ClientMessage, type ClientOptions } from "./client.ts";
export { setMediaHosts, checkMediaUrl } from "./media.ts";
export { defineCatalog, type FunctionSpec, type ArgKind } from "./define.ts";
