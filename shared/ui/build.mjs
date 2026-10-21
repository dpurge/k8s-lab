// Bundles src/dp-core.ts into dist/ for shared/ui's Go package to embed.
// The core entry has a stable name; dynamically imported catalog components
// become hashed chunks under dist/chunks/, so an app only downloads the ones
// a page actually uses. The A2UI renderer is a second stable entry,
// imported only by pages that draw A2UI surfaces; it shares Lit with the core.
// dist/ is never committed (only dist/.gitkeep is).
import { build } from "esbuild";
import { copyFile, mkdir, readdir, rm, stat } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = fileURLToPath(new URL("./dist/", import.meta.url));

await mkdir(dist, { recursive: true });
for (const name of await readdir(dist)) {
  if (name !== ".gitkeep") await rm(join(dist, name), { recursive: true, force: true });
}

const result = await build({
  entryPoints: {
    "dp-core": "src/dp-core.ts",
    a2ui: "src/a2ui/index.ts",
    "a2ui-gallery": "src/a2ui/dp-a2ui-gallery/component.ts",
  },
  outdir: dist,
  chunkNames: "chunks/[name]-[hash]",
  bundle: true,
  splitting: true,
  format: "esm",
  target: "es2022",
  minify: true,
  legalComments: "none",
  loader: { ".css": "text" },
  metafile: true,
  logLevel: "warning",
});

// theme-init.js is a classic script (it runs in <head>, before first paint), so
// it is its own IIFE bundle, not an ES module with the core's chunks.
const themeInit = await build({
  entryPoints: { "theme-init": "src/theme-init.ts" },
  outdir: dist,
  bundle: true,
  format: "iife",
  target: "es2022",
  minify: true,
  legalComments: "none",
  metafile: true,
  logLevel: "warning",
});
Object.assign(result.metafile.outputs, themeInit.metafile.outputs);

await copyFile(fileURLToPath(new URL("./src/tokens.css", import.meta.url)), join(dist, "tokens.css"));

for (const file of Object.keys(result.metafile.outputs).sort()) {
  const { size } = await stat(file);
  console.log(`${String(size).padStart(8)} B  ${file.replace(dist, "dist/")}`);
}
const { size } = await stat(join(dist, "tokens.css"));
console.log(`${String(size).padStart(8)} B  dist/tokens.css`);
