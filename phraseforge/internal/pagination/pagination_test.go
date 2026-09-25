package pagination

import (
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrips(t *testing.T) {
	// A nanosecond-precision, non-UTC-offset time — exactly the case that
	// would silently lose precision with a looser time format.
	want := Cursor{CreatedAt: time.Date(2026, 9, 25, 12, 34, 56, 123456789, time.UTC), ID: 42}
	token := Encode(want)
	got, err := Decode(token)
	if err != nil {
		t.Fatalf("Decode(%q) error = %v", token, err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Errorf("Decode(Encode(c)) = %+v, want %+v", got, want)
	}
}

func TestDecodeMalformedTokenIsAnError(t *testing.T) {
	cases := []string{"", "not-base64!!!", "aGVsbG8", "e30"} // empty, invalid base64, valid base64 but not JSON/wrong shape
	for _, c := range cases {
		if _, err := Decode(c); err == nil {
			t.Errorf("Decode(%q) = nil error, want an error (caller treats this as \"no cursor\", never a 400)", c)
		}
	}
}
