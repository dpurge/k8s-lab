# shared/ui

The Lit component library shared by every app (`dp-*` custom elements) and the
Go package that serves it. TypeScript source in `src/`, bundled by esbuild into
`dist/` (built, not committed except for the `dist/.gitkeep` placeholder), embedded by `ui.go`.

## Use it in an app

1. Mount the handler: `r.Handle(ui.Prefix+"*", ui.Handler())` (`k8s-lab/shared/ui`).
   It serves `dist/` under `/ui/`, with `nosniff` and cache headers.
2. Build the bundle before `go run`/`go test`: `task build-ui`. The app's
   Dockerfile needs the `ui` stage (copy it from `phraseforge/Dockerfile`).
3. In the page: `<script type="module" src="/ui/dp-core.js"></script>`. For a page
   framed by `dp-shell` also put `<script src="/ui/theme-init.js" data-key="myapp">`
   (a classic script, before the first paint) in `<head>`, and
   `dp-shell:not(:defined) { opacity: 0; }` in the page's CSS.
   Optionally `<link rel="stylesheet" href="/ui/tokens.css">` for the shared palette.
4. A page whose Content-Security-Policy is strict needs `script-src 'self'`; the
   components use constructable stylesheets, so `style-src 'self'` is enough.

## Rules every component follows

These are what make a component usable in any app, so keep to them:

- **No knowledge of any app.** No app names, routes, ids, API calls or page
  selectors. Content comes in through attributes, properties and slots;
  results go out as events.
- **Themed only through CSS custom properties.** Read the shared tokens
  (`--surface`, `--border`, `--text`, `--link`, `--btn-bg`, ...) and offer
  component-level properties (`--dp-card-padding`, ...). Always give a fallback,
  `var(--dp-card-bg, var(--surface, #ffffff))`, so the component also works in
  an app that does not load `tokens.css`. Never read a page's class names.
- **Shadow DOM, slots for content.** Slotted content stays in the page, so the
  page styles it. Expose the parts people may need to restyle with `part="..."`.
- **Events** are named `dp-<component>-<verb>`, carry their data in `detail`,
  and set `bubbles` and `composed` (one delegated listener per list works).
  An event the page may want to override is `cancelable`.
- **Accessible by default.** Native elements inside (a real `<button>`), a
  focus ring (`:focus-visible`), keyboard operation, ARIA forwarded from the host.
- **Documented in the file.** The comment at the top of `component.ts` lists the
  attributes, slots, events, parts and CSS properties.
- **Form controls stay native and in the page's DOM.** A component that wraps a
  text input, textarea or select slots the page's own element; it never replaces
  it with one drawn in a shadow root and never changes its attributes or handlers.
  Input methods depend on it: phraseforge's own IME (`ime.js`, wired by
  `editor.js`) assigns `onkeydown`, `onkeypress` and `onkeyup` to the control and
  reads `this.value` and `this.selectionStart`, and the operating system's IME
  composes into the native control.
- **Forms keep working from the keyboard.** A control drawn in a shadow root is
  not part of the page's `<form>`. A component that submits a form (`dp-button
  type="submit"`) therefore keeps a hidden real `<button type="submit">` in the
  form's DOM, so Enter in a field submits as it would natively, and the visible
  button calls `form.requestSubmit(thatButton)` so it submits exactly once.
  Test both with a form of two or more fields: Enter in the last field, a click,
  and Enter on the focused button must each send one submission.
- **Hide an undefined component with `opacity: 0`, never `visibility: hidden`.**
  A field inside a `visibility: hidden` element cannot be focused, so a login
  page's `autofocus` is silently dropped while the component is still loading.
  Compare screenshots of a page before and after a conversion, with the same
  focus state: a pixel diff found this one.
- **Script order.** `<script type="module">` runs after the page is parsed, so a
  classic page script that calls a component's methods (`statusBar.setMessage`)
  could run before the element is defined. Put `/ui/dp-core.js` before such
  scripts and give them `defer` (deferred scripts and module scripts run in
  document order), or wait for `customElements.whenDefined("dp-...")`.
- **Declare, don't initialise, reactive fields** (`declare variant: string;` and
  set the default in the constructor), or the class fields shadow Lit's accessors.

## Shell on a phone

`dp-shell`'s sidebar is a drawer below 760 px, and its columns and top bar are
built to never make the page scroll sideways: content wider than the screen
scrolls inside the main area. A page that fills the top bar's `actions` slot
should let long text give way (`min-width: 0`, `text-overflow: ellipsis`), as
phraseforge does for the user name.

## Data tables

Three components in `dp-core` draw tabular data. They only draw the rows they
are given: the host loads, pages, sorts and (for usage) chooses the grouping.
Set `rows` as a property (there is no attribute for it). Each also has
`emptyText` (attribute `empty-text`), shown instead of the table when there are
no rows (with none set, nothing is drawn), and `labels`, an object that replaces
column headings by key. Parts: `table`, `empty`. Each file's header lists the
row fields.

| Element | Shows | Notes |
|---|---|---|
| `dp-usage-table` | per model: user, provider, model, calls, input and output tokens, cost | `groupBy` (attribute `group-by`): `"user"`, `"provider"` or `"model"` sums the rows over the other two, highest cost first; anything else (the default) shows the rows as given. Label keys: `user`, `provider`, `model`, `calls`, `inputTokens`, `outputTokens`, `cost` |
| `dp-model-table` | provider, model, tags, priority, availability | Rows are drawn in the order given. `state` is `"available"`, `"held_out"` (with `until`, when it is tried again) or `"no_credentials"`. Label keys: `provider`, `model`, `tags`, `priority`, `availability`, `available`, `heldOut`, `noCredentials`, `until` |
| `dp-attempt-table` | one row per model call: started, user, provider, model, outcome, input and output tokens, cost, duration | An `outcome` other than `"ok"` is drawn as a failure; `error` is its tooltip. Label keys: `started`, `user`, `provider`, `model`, `outcome`, `inputTokens`, `outputTokens`, `cost`, `duration` |

`src/table.css` is their shared look: a plain table in a box that scrolls
sideways when the page is narrow, themed only through the tokens (`--border`,
`--muted`, `--success`, `--danger`, each with a literal fallback).

`src/format.ts` holds the formatting they share, so every app shows a figure the
same way. Each function returns a string, and a missing or invalid value is a
dash (`DASH`, "—"):

- `tokens(n)`: a count with thousands separators (en-US).
- `cost(usd)`: dollars; two decimals from $1 up, four below it, `$0.00` for
  zero and `<$0.0001` for less than that.
- `duration(ms)`: for reading, such as `0.4 s`, `12 s`, `3 min 05 s`, `1 h 02 min`.
- `compactTime(iso)`: date and time without the year or seconds, in the reader's
  locale.
- `fullTime(iso)`: the whole timestamp, for a tooltip.

`format.ts` is not exported by `dp-core`; the table components import it.

## Add a component

```
src/components/dp-thing/
  component.ts       the class and customElements.define("dp-thing", ...)
  component.css      the styles, imported as text: unsafeCSS(styles)
  component.test.ts  node --test on the source, under jsdom
```

Import it from `src/dp-core.ts` if every app needs it, or load it on demand with
`import("...")` (esbuild turns that into a separate chunk) for catalog components.

## The A2UI renderer (on demand)

`src/a2ui/` draws interfaces that an agent describes in A2UI v1.0 messages
(the assistant's chat). It is a separate entry, never part of `dp-core`: a page
that has a surface imports `/ui/a2ui.js` (about 130 KB gzipped, mostly
`@a2ui/web_core`'s schemas); every other page never downloads it.

```js
const { createClient, setMediaHosts } = await import("/ui/a2ui.js");
setMediaHosts(hostsFromTheServer);                 // none by default
const client = createClient({ send: (message) => post(message) });
await client.receive(messagesFromTheAgent);        // draw, update, answer calls
surfaceElement.surface = client.processor.getSurface(id);   // <dp-a2ui-surface>
```

- `dpCatalog` is web_core's v1.0 basic catalog with the `dp-a2ui-*` elements as
  implementations (all 18 components), drawn in the light DOM from `dp-*`
  blocks and styled only by the tokens. Form controls are native elements in
  the page's own DOM, so input methods work (a composing text field writes the
  data model on `compositionend`).
- An app adds functions of its own with `defineCatalog({id, functions})` and
  `createClient({catalogs: [...]})`: each function says who may call it
  (`allowedCallers`, default the renderer only), its arguments (checked before it
  runs) and may be async (the value appears when it arrives; it runs once per
  distinct arguments, so make a catalog per conversation). The assistant defines
  `viewport` and `listWorkflows` this way.
- What the renderer sends back (`action`, `error`, `callAgentFunction`,
  `rendererFunctionResponse`) goes through `send`; see `src/a2ui/client.ts`.
- Untrusted by design: Text markdown is drawn as DOM (never parsed as HTML; only
  http and https links); media comes from `data:`, this origin and the hosts
  given to `setMediaHosts` (the server's Content-Security-Policy is what
  enforces it); `openUrl` opens only http and https, with no opener, and only
  from a user's action; message size, component count and depth are limited
  (`limits.ts`). Components never write a `style` attribute (a strict CSP forbids
  inline styles): use classes or CSSOM.
- `src/a2ui/icons.ts` is generated from `@material-design-icons/svg` 0.14.15
  (Apache-2.0), not a dependency: a page cannot load an icon font.
- `src/a2ui/fixtures/` holds the reference's examples and conformance cases,
  unchanged; `dp-a2ui-gallery` (`/ui/a2ui-gallery.js`) draws them all.

## Commands

| Command | What it does |
|---|---|
| `task build-ui` | `npm ci` and bundle into `dist/` (prints the file sizes) |
| `task test-ui` | `tsc --noEmit`, the component tests, then the Go handler tests |

Tests run directly on the TypeScript source (Node's type stripping), so use only
erasable syntax: no enums, parameter properties or decorators.
