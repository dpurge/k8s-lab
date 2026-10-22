package queue

import (
	"context"
	"testing"
)

func TestOperationIDTravelsInTheContext(t *testing.T) {
	if _, ok := OperationID(context.Background()); ok {
		t.Error("a context outside a handler has no operation id")
	}
	ctx := WithOperationID(context.Background(), "op-1")
	if id, ok := OperationID(ctx); !ok || id != "op-1" {
		t.Errorf("OperationID = %q, %v; want op-1, true", id, ok)
	}
	if _, ok := OperationID(WithOperationID(context.Background(), "")); ok {
		t.Error("an empty id is no id")
	}
}
