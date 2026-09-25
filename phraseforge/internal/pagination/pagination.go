// Package pagination provides the keyset-cursor primitives shared by every
// paginated list endpoint (Texts/Dialogs/Vocabulary/Models — see
// specs/features/phraseforge-spa-pagination.md). This is genuinely common
// utility logic all four resources need identically, unlike a job
// payload's independently-versioned wire contract (which this codebase
// deliberately duplicates per package instead of sharing) — a cursor is
// the same opaque token shape everywhere it's used, so one shared package
// is the right call here.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultLimit is this feature's fixed page size — not client-configurable
// via a query param (see the feature spec's Out of Scope).
const DefaultLimit = 25

// Cursor identifies the last item of a previously-returned page — the next
// page's query is "everything before this point" on the same (created_at,
// id) DESC ordering every list endpoint already uses. Compound (not just
// ID) because an imported/backfilled row can legitimately have a
// created_at that doesn't match insertion/id order, same as today's plain
// ORDER BY created_at DESC already assumes.
type Cursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        int64     `json:"id"`
}

// wireCursor mirrors Cursor's JSON shape with RFC3339Nano-formatted time —
// encoding/json's default time.Time marshaling is already RFC3339Nano, but
// spelled out explicitly here since exact round-tripping (down to the
// nanosecond) is what keeps a decoded cursor's keyset comparison correct;
// silently losing precision would let it skip or repeat a row.
type wireCursor struct {
	CreatedAt string `json:"created_at"`
	ID        int64  `json:"id"`
}

// Encode turns a Cursor into the opaque token a client passes back as
// ?cursor=. Never fails in practice (json.Marshal on this fixed, simple
// shape has no failure mode) — panics rather than threading an error
// through every caller for a case that can't happen.
func Encode(c Cursor) string {
	w := wireCursor{CreatedAt: c.CreatedAt.Format(time.RFC3339Nano), ID: c.ID}
	b, err := json.Marshal(w)
	if err != nil {
		panic(fmt.Sprintf("pagination: encode cursor: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a token from Encode. A missing/garbled value is the
// caller's signal to treat the request as "first page", not a 400 —
// matching this app's existing lenient-query-param conventions elsewhere
// (see e.g. apiListTexts's own ?language= handling) — so every caller
// should treat a Decode error as "no cursor", never surface it to the
// client as a validation error.
func Decode(token string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, fmt.Errorf("pagination: decode cursor: %w", err)
	}
	var w wireCursor
	if err := json.Unmarshal(b, &w); err != nil {
		return Cursor{}, fmt.Errorf("pagination: decode cursor: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, w.CreatedAt)
	if err != nil {
		return Cursor{}, fmt.Errorf("pagination: decode cursor: parse created_at: %w", err)
	}
	return Cursor{CreatedAt: t, ID: w.ID}, nil
}
