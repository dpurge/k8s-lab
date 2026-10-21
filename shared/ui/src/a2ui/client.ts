// The renderer's side of the conversation with the agent.
//
//   const client = createClient({ send: (message) => post(message) });
//   await client.receive(messagesFromTheAgent);       // draw, update, answer calls
//   const value = await client.callAgentFunction(surfaceId, { call: "..." });
//
// Everything the renderer sends back to the agent goes through `send` as a
// renderer-to-agent message ({version: "v1.0", action | error |
// callAgentFunction | rendererFunctionResponse}). The processor already
// answers `callRendererFunction` (refusing what the catalog forbids); this
// adds what a page needs around it: a message the processor rejects becomes an
// `error` message instead of an exception, errors a surface reports are
// forwarded, a reply to a call nobody made is reported and ignored, and every
// call to the agent has a time limit.
import type { Catalog, FunctionCall, WebComponentImplementation } from "@a2ui/web_core/v1_0";
import { dpCatalog } from "./catalog.ts";
import { DEFAULT_LIMITS, overLimit, type Limits } from "./limits.ts";
import { createProcessor } from "./processor.ts";

export type ClientMessage = { version: "v1.0" } & Record<string, unknown>;

export type ClientOptions = {
  send: (message: ClientMessage) => void | Promise<void>;
  // How long a callAgentFunction waits for its answer.
  callTimeoutMs?: number;
  // Overrides of the size limits (limits.ts).
  limits?: Partial<Limits>;
  // Catalogs of functions to add to the basic one (define.ts).
  catalogs?: Catalog<WebComponentImplementation>[];
};

const SURFACE_KEYS = ["createSurface", "updateComponents", "updateDataModel", "deleteSurface", "callRendererFunction"] as const;

// The surface a message from the agent is about, when it names one.
const surfaceOf = (message: unknown) => {
  const body = message as Record<string, { surfaceId?: unknown } | undefined>;
  for (const key of SURFACE_KEYS) {
    const id = body?.[key]?.surfaceId;
    if (typeof id === "string") return id;
  }
  return undefined;
};

export function createClient({ send, callTimeoutMs = 15_000, limits: overrides, catalogs = [] }: ClientOptions) {
  const limits = { ...DEFAULT_LIMITS, ...overrides };
  const out = (body: Record<string, unknown>) => send({ version: "v1.0", ...body });
  const reported = new WeakSet<object>();
  const pending = new Set<string>();
  let counter = 0;

  const processor = createProcessor(
    (action: unknown) => {
      const payload = action as Record<string, unknown>;
      return out({ action: { timestamp: new Date().toISOString(), ...payload } });
    },
    { outboundListener: (message) => send(message as unknown as ClientMessage), defaultTimeoutMs: callTimeoutMs },
    catalogs,
  );

  // Errors go to the agent. Warnings (a binding to a path the data model does
  // not have yet, which is normal while an agent is still filling it in) only
  // go to the console.
  type Watched = {
    id: string;
    onError: { subscribe(fn: (e: Record<string, unknown>) => void): unknown };
    onWarning: { subscribe(fn: (w: Record<string, unknown>) => void): unknown };
  };
  const watch = (surface: Watched) => {
    surface.onError.subscribe((error) => {
      void out({ error: { ...error, surfaceId: surface.id } });
    });
    surface.onWarning.subscribe((warning) => console.warn(`A2UI surface ${surface.id}: ${String(warning.message)}`));
  };
  processor.onSurfaceCreated(watch as never);

  const report = (error: unknown, surfaceId: string | undefined, path: string) => {
    if (typeof error === "object" && error !== null) {
      if (reported.has(error)) return;
      reported.add(error);
    }
    const message = error instanceof Error ? error.message : String(error);
    if (!surfaceId) return console.error("A2UI message rejected:", message);
    const code = (error as { code?: unknown })?.code;
    if (code === "VALIDATION_ERROR") return out({ error: { code: "VALIDATION_FAILED", surfaceId, path, message } });
    return out({ error: { code: typeof code === "string" && code ? code : "RENDERER_ERROR", surfaceId, message } });
  };

  // The processor keeps a component whose type is not in the catalog (it draws
  // as a note); the agent is told, so it can correct itself.
  const reportUnknownComponents = async (message: unknown) => {
    const update = (message as { updateComponents?: { surfaceId: string; components?: { component?: unknown }[] } }).updateComponents;
    const list = update?.components ?? [];
    for (const [index, entry] of list.entries()) {
      const type = entry?.component;
      if (typeof type === "string" && !dpCatalog.components.has(type)) {
        await out({
          error: { code: "VALIDATION_FAILED", surfaceId: update!.surfaceId, path: `/components/${index}/component`, message: `Unknown component type '${type}'.` },
        });
      }
    }
  };

  return {
    processor,

    // Handles messages from the agent one at a time, so one bad message does
    // not stop the ones after it.
    async receive(messages: unknown[]) {
      for (const message of messages) {
        const response = (message as { agentFunctionResponse?: { functionCallId?: string } }).agentFunctionResponse;
        if (response && !pending.has(String(response.functionCallId))) {
          await out({
            error: { code: "UNKNOWN_FUNCTION_CALL", functionCallId: response.functionCallId, message: "No call to the agent is waiting for this answer." },
          });
          continue;
        }
        const tooBig = overLimit(message, limits, (id) => new Set(processor.getSurface(id)?.componentsModel.keys ?? []));
        if (tooBig) {
          const surfaceId = surfaceOf(message);
          if (surfaceId) await out({ error: { code: "LIMIT_EXCEEDED", surfaceId, message: tooBig } });
          else console.error("A2UI message rejected:", tooBig);
          continue;
        }
        try {
          await processor.processMessagesAsync(message as never);
          await reportUnknownComponents(message);
        } catch (error) {
          await report(error, surfaceOf(message), "/");
        }
      }
    },

    // Calls a function on the agent and waits for the answer, which comes back
    // through receive() as an agentFunctionResponse. Rejects on an error answer
    // or when no answer arrives in time.
    async callAgentFunction<T = unknown>(surfaceId: string, call: FunctionCall): Promise<T> {
      const functionCallId = `renderer-${Date.now().toString(36)}-${++counter}`;
      pending.add(functionCallId);
      try {
        return await processor.callAgentFunction<T>(surfaceId, call, { functionCallId, timeoutMs: callTimeoutMs });
      } finally {
        pending.delete(functionCallId);
      }
    },
  };
}
