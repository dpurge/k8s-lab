// The message processor wired to the dp-* catalog.
import { MessageProcessor, RELAXED_VALIDATION, type Catalog, type WebComponentImplementation } from "@a2ui/web_core/v1_0";
import { dpCatalog } from "./catalog.ts";
import { DEFAULT_LIMITS } from "./limits.ts";

type Args = ConstructorParameters<typeof MessageProcessor>;

// Orphans, missing roots and dangling references are allowed while a message
// stream is still arriving; the depth is limited.
// `extra` are catalogs an app adds (see define.ts); the basic one is always first.
export const createProcessor = (onAction: Args[1], options?: Args[2], extra: Catalog<WebComponentImplementation>[] = []) =>
  new MessageProcessor([dpCatalog, ...extra], onAction, {
    validationConfig: { ...RELAXED_VALIDATION, maxDepth: DEFAULT_LIMITS.maxDepth },
    ...options,
  });
