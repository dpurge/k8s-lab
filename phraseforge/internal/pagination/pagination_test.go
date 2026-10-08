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

func TestParsePage(t *testing.T) {
	tests := []struct {
		raw    string
		page   int
		wantOK bool
	}{
		{"3", 3, true},
		{"1", 1, true},
		{"", 1, false},
		{"0", 1, false},
		{"-2", 1, false},
		{"abc", 1, false},
	}
	for _, tc := range tests {
		page, ok := ParsePage(tc.raw)
		if page != tc.page || ok != tc.wantOK {
			t.Errorf("ParsePage(%q) = (%d, %v), want (%d, %v)", tc.raw, page, ok, tc.page, tc.wantOK)
		}
	}
}

func TestOffsetAndPageCount(t *testing.T) {
	if got := Offset(1, 25); got != 0 {
		t.Errorf("Offset(1, 25) = %d, want 0", got)
	}
	if got := Offset(3, 25); got != 50 {
		t.Errorf("Offset(3, 25) = %d, want 50", got)
	}
	counts := []struct{ total, want int }{{0, 1}, {1, 1}, {25, 1}, {26, 2}, {50, 2}, {51, 3}}
	for _, tc := range counts {
		if got := PageCount(tc.total, 25); got != tc.want {
			t.Errorf("PageCount(%d, 25) = %d, want %d", tc.total, got, tc.want)
		}
	}
}
