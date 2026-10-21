// Stylesheets are imported as text: esbuild's "text" loader in build.mjs, and
// test-hooks.mjs under node --test.
declare module "*.css" {
  const text: string;
  export default text;
}
