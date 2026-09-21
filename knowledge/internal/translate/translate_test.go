package translate

import (
	"context"
	"errors"
	"testing"

	"knowledge/internal/config"
)

func TestTranslateRejectsEmptyText(t *testing.T) {
	svc := New(config.Config{KnowledgeLanguage: "English"})
	if _, err := svc.Translate(context.Background(), "   "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("Translate(whitespace) error = %v, want ErrEmptyText", err)
	}
}
