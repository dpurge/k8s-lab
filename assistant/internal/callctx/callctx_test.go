package callctx

import (
	"context"
	"testing"
)

func TestInfoTravelsInTheContext(t *testing.T) {
	ctx := With(context.Background(), Info{TaskID: "t1", UserID: "u1"})
	got, ok := From(ctx)
	if !ok || got.TaskID != "t1" || got.UserID != "u1" {
		t.Errorf("got %+v, %v", got, ok)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got, ok := From(child); !ok || got.UserID != "u1" {
		t.Errorf("a derived context keeps it: %+v %v", got, ok)
	}
}

func TestNothingIsFoundInAPlainContext(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Errorf("a context without info reports none")
	}
}

func TestInfoWithoutAUserIsNotInfo(t *testing.T) {
	if _, ok := From(With(context.Background(), Info{TaskID: "t1"})); ok {
		t.Errorf("a call with no user has nobody to charge")
	}
}

func TestATaskIsOptional(t *testing.T) {
	got, ok := From(With(context.Background(), Info{UserID: "u1"}))
	if !ok || got.TaskID != "" {
		t.Errorf("got %+v, %v", got, ok)
	}
}

func TestAnInnerWithReplacesTheOuter(t *testing.T) {
	ctx := With(With(context.Background(), Info{UserID: "outer"}), Info{UserID: "inner"})
	if got, _ := From(ctx); got.UserID != "inner" {
		t.Errorf("got %+v", got)
	}
}
