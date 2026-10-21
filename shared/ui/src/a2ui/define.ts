// Catalogs of functions an app adds to the renderer's own. The assistant's page
// has two: `viewport`, which the page runs, and `listWorkflows`, which asks the
// assistant. A function may be async (it asks the server, say): the value
// appears when it arrives, and what draws it redraws. The renderer evaluates a
// binding more than once (to check it, to draw it), so an async function runs
// once per distinct arguments for the life of the catalog, and the value is
// shared: make a catalog for each conversation, not for the page.
import { z } from "zod";
import {
  Catalog,
  createFunctionImplementation,
  setValue,
  signal,
  type A2uiReturnType,
  type FunctionImplementation,
  type WebComponentImplementation,
} from "@a2ui/web_core/v1_0";

export type ArgKind = "string" | "number" | "boolean";

export type FunctionSpec = {
  name: string;
  returnType: A2uiReturnType;
  // Who may call it, as the catalog document says (default: the renderer only).
  allowedCallers?: "rendererOnly" | "agentOnly" | "rendererOrAgent";
  description?: string;
  // The arguments it takes, all required, by name; none if omitted. Anything
  // else is refused before `execute` runs.
  args?: Record<string, ArgKind>;
  execute: (args: Record<string, unknown>) => unknown | Promise<unknown>;
};

const KINDS = { string: z.string(), number: z.number(), boolean: z.boolean() };

export function defineCatalog({ id, functions }: { id: string; functions: FunctionSpec[] }) {
  const async = new Map<string, unknown>();
  const implementations: FunctionImplementation[] = functions.map((spec) => {
    const shape = Object.fromEntries(Object.entries(spec.args ?? {}).map(([name, kind]) => [name, KINDS[kind]]));
    return createFunctionImplementation(
      {
        name: spec.name,
        returnType: spec.returnType,
        schema: z.object(shape).strict(),
        allowedCallers: spec.allowedCallers,
        description: spec.description,
      },
      (args) => {
        const key = `${spec.name}:${JSON.stringify(args)}`;
        if (async.has(key)) return async.get(key) as never;
        const result = spec.execute(args as Record<string, unknown>);
        if (!(result instanceof Promise)) return result as never;
        const pending = signal<unknown>(undefined);
        async.set(key, pending);
        result.then(
          (value) => {
            setValue(pending, value);
          },
          (error) => console.warn(`The function ${spec.name} failed: ${error instanceof Error ? error.message : String(error)}`),
        );
        return pending as never;
      },
    );
  });
  return new Catalog<WebComponentImplementation>(id, "1.0", [], implementations);
}
