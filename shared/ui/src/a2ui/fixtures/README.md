Examples of the A2UI v1.0 basic catalog, copied unchanged from the reference
repository (`catalogs/basic/v1/examples`, commit 059b8d34874f96c0395dbfe23e6cc8b41cdb0f87,
Apache-2.0 licence). The renderer tests draw each one. Examples are added as
the components they need are implemented.

`conformance/functions.json` holds the 55 `evaluate_function` cases of the
reference's `conformance/core/functions.yaml` that name no surface (converted
from YAML to JSON, `action` key dropped, nothing else changed). The 8 cases
with a `surface` block test choosing between several catalogs and do not apply
to a renderer with one catalog.

The cases without `protocolVersion` were written for v0.9, where the
validation functions (`required`, `regex`, `email`, `length`, `numeric`) return
a boolean. v1.0 returns a `ValidationResult`, so the test compares its `valid`
field; everything else is compared as the case states it.
