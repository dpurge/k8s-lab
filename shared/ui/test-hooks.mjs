// Loaded with node --import by "npm test": lets a test import a component's
// .css file as a string, the way esbuild's "text" loader does in build.mjs.
import { registerHooks } from "node:module";
import { readFileSync } from "node:fs";

registerHooks({
  load(url, context, nextLoad) {
    if (!url.endsWith(".css")) return nextLoad(url, context);
    const text = readFileSync(new URL(url), "utf8");
    return { format: "module", shortCircuit: true, source: `export default ${JSON.stringify(text)};` };
  },
});
