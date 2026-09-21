package ingest

import (
	"errors"
	"strings"
	"testing"
)

func TestIsUUIDAcceptsGeneratedUUID(t *testing.T) {
	id, err := uuid()
	if err != nil {
		t.Fatalf("uuid() error = %v, want nil", err)
	}
	if !isUUID(id) {
		t.Errorf("isUUID(%q) = false, want true for a uuid() generated value", id)
	}
}

func TestIsUUIDRejectsMalformedShapes(t *testing.T) {
	valid, err := uuid()
	if err != nil {
		t.Fatalf("uuid() error = %v, want nil", err)
	}

	// wrongHexPosition takes a real, valid UUID and swaps one character that
	// must be a hex digit for 'g' — a non-hex character sitting exactly
	// where a hex digit is expected, rather than in a hyphen slot.
	wrongHexPosition := []byte(valid)
	wrongHexPosition[0] = 'g'

	cases := map[string]string{
		"empty string":                     "",
		"too short":                        valid[:len(valid)-1],
		"too long":                         valid + "a",
		"hyphens in wrong positions":       "0123456789ab-cde0-1234-5678-9abcdef01234",
		"uppercase hex digits":             strings.ToUpper(valid),
		"non-hex character in a hex slot":  string(wrongHexPosition),
		"non-hex character, wrong overall": "not-a-uuid-at-all-but-36-characters",
	}
	for name, id := range cases {
		if isUUID(id) {
			t.Errorf("isUUID(%q) [%s] = true, want false", id, name)
		}
	}
}

func TestValidateDraftFieldsAllNonEmpty(t *testing.T) {
	if err := validateDraftFields("title", "summary", "body"); err != nil {
		t.Errorf("validateDraftFields() error = %v, want nil", err)
	}
}

func TestValidateDraftFieldsEmptyTitle(t *testing.T) {
	err := validateDraftFields("", "summary", "body")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("validateDraftFields() error = %v, want it to wrap ErrValidation", err)
	}
}

func TestValidateDraftFieldsEmptySummary(t *testing.T) {
	err := validateDraftFields("title", "", "body")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("validateDraftFields() error = %v, want it to wrap ErrValidation", err)
	}
}

func TestValidateDraftFieldsEmptyBody(t *testing.T) {
	err := validateDraftFields("title", "summary", "")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("validateDraftFields() error = %v, want it to wrap ErrValidation", err)
	}
}

func TestValidateDraftFieldsWhitespaceOnlyTitle(t *testing.T) {
	err := validateDraftFields("   ", "summary", "body")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("validateDraftFields() error = %v, want it to wrap ErrValidation for a whitespace-only title", err)
	}
}

func TestValidateDraftFieldsAllEmpty(t *testing.T) {
	// First-failure-wins: title is checked first, so this is equivalent to
	// TestValidateDraftFieldsEmptyTitle, but it documents that all three
	// being empty at once still produces exactly one clear error rather
	// than a multi-field report.
	err := validateDraftFields("", "", "")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("validateDraftFields() error = %v, want it to wrap ErrValidation", err)
	}
}
