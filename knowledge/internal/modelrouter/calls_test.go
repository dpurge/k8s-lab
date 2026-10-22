package modelrouter

import (
	"context"
	"testing"

	"knowledge/internal/queue"
)

func TestKindAndLogAttrsComeFromTheContext(t *testing.T) {
	if Kind(context.Background()) != "" || len(LogAttrs(context.Background())) != 0 {
		t.Error("a plain context has no kind and no log attributes")
	}
	ctx := queue.WithOperationID(WithKind(context.Background(), KindTranslate), "op-9")
	if Kind(ctx) != KindTranslate {
		t.Errorf("Kind = %q, want translate", Kind(ctx))
	}
	got := map[string]string{}
	for _, a := range LogAttrs(ctx) {
		got[a.Key] = a.Value.String()
	}
	if got["kind"] != "translate" || got["operation_id"] != "op-9" || len(got) != 2 {
		t.Errorf("LogAttrs = %v, want only the kind and the operation id", got)
	}
}
