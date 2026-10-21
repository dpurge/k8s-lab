// How much interface one agent may draw. A model can write a message of any
// size; the page must not freeze or run out of memory drawing it. A message
// over a limit is rejected whole, with an error naming the limit, and the
// surface stays as it was.
export type Limits = {
  // Bytes of one message as JSON.
  maxMessageBytes: number;
  // Components one updateComponents (or createSurface) may carry.
  maxComponentsPerMessage: number;
  // Components one surface may hold in all.
  maxComponentsPerSurface: number;
  // Levels of nesting from the root; web_core enforces it as `maxDepth`.
  maxDepth: number;
};

export const DEFAULT_LIMITS: Limits = {
  maxMessageBytes: 256 * 1024,
  maxComponentsPerMessage: 500,
  maxComponentsPerSurface: 1000,
  maxDepth: 20,
};

type Message = {
  createSurface?: { surfaceId?: string; components?: { id?: unknown }[] };
  updateComponents?: { surfaceId?: string; components?: { id?: unknown }[] };
};

const encoder = new TextEncoder();

// The reason a message is over a limit, or undefined. `existing` holds the ids
// the surface already has, so an update that replaces a component does not
// count it twice.
export function overLimit(message: unknown, limits: Limits, existing: (surfaceId: string) => ReadonlySet<string>): string | undefined {
  const bytes = encoder.encode(JSON.stringify(message) ?? "").length;
  if (bytes > limits.maxMessageBytes) return `The message is ${bytes} bytes; the limit is ${limits.maxMessageBytes}.`;
  const body = (message as Message).updateComponents ?? (message as Message).createSurface;
  const list = body?.components;
  if (!Array.isArray(list)) return undefined;
  if (list.length > limits.maxComponentsPerMessage) {
    return `The message has ${list.length} components; the limit is ${limits.maxComponentsPerMessage}.`;
  }
  const known = existing(body?.surfaceId ?? "");
  const total = new Set([...known, ...list.map((c) => String(c?.id))]).size;
  if (total > limits.maxComponentsPerSurface) {
    return `The surface would have ${total} components; the limit is ${limits.maxComponentsPerSurface}.`;
  }
  return undefined;
}
