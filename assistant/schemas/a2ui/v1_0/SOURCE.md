A2UI v1.0 (a candidate release of the protocol), copied unchanged from the
reference repository (https://github.com/google/A2UI, Apache-2.0), commit
`059b8d34874f96c0395dbfe23e6cc8b41cdb0f87`:

| File here | Source in the repository |
|---|---|
| `agent_to_renderer.json` | `specification/v1_0/json/agent_to_renderer.json` |
| `renderer_to_agent.json` | `specification/v1_0/json/renderer_to_agent.json` |
| `common_types.json` | `specification/v1_0/json/common_types.json` |
| `basic_catalog.json` | `catalogs/basic/v1/catalog.json` |

`internal/a2ui` validates the assistant's messages against them; the page's
renderer (`shared/ui`, `@a2ui/web_core` 0.14.0) implements the same version.
To update, copy the files again, record the new commit here, and run the tests.

The files are unchanged. `internal/a2ui` reads them with one adjustment in
memory: the properties `\p{XID_Start}` and `\p{XID_Continue}` in their
identifier patterns, which Go's regular expressions do not have, are replaced
by the Unicode classes that cover them (see `goRegexp`).
