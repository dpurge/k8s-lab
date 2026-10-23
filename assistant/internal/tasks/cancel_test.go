package tasks

import (
	"context"
	"errors"
	"testing"
)

// These tests need no database: the store and the worker are fakes.

type fakeCancelStore struct {
	cancelTask Task
	cancelErr  error
	getTask    Task
	getErr     error
	gets       int
}

func (f *fakeCancelStore) Cancel(context.Context, string) (Task, error) {
	return f.cancelTask, f.cancelErr
}

func (f *fakeCancelStore) Get(context.Context, string) (Task, error) {
	f.gets++
	return f.getTask, f.getErr
}

type fakeStopper struct {
	stops bool
	asked []string
}

func (f *fakeStopper) Cancel(id string) bool {
	f.asked = append(f.asked, id)
	return f.stops
}

func TestAPendingOrWaitingTaskIsCancelledAtOnceAndTheWorkerIsNotAsked(t *testing.T) {
	store := &fakeCancelStore{cancelTask: Task{ID: "t1", Status: StatusCancelled}}
	worker := &fakeStopper{stops: true}
	got, outcome, err := CancelOrStop(context.Background(), store, worker, "t1")
	if err != nil || outcome != CancelledNow || got.Status != StatusCancelled {
		t.Fatalf("task=%+v outcome=%v err=%v", got, outcome, err)
	}
	if len(worker.asked) != 0 || store.gets != 0 {
		t.Errorf("worker asked %v, store read %d times", worker.asked, store.gets)
	}
}

func TestARunningTaskIsToldToStopAndTheCurrentTaskIsReturned(t *testing.T) {
	store := &fakeCancelStore{cancelErr: ErrNotCancellable, getTask: Task{ID: "t1", Status: StatusRunning}}
	worker := &fakeStopper{stops: true}
	got, outcome, err := CancelOrStop(context.Background(), store, worker, "t1")
	if err != nil || outcome != Stopping || got.Status != StatusRunning {
		t.Fatalf("task=%+v outcome=%v err=%v", got, outcome, err)
	}
	if len(worker.asked) != 1 || worker.asked[0] != "t1" {
		t.Errorf("worker asked %v", worker.asked)
	}
}

func TestATaskTheWorkerIsNotRunningIsNotCancellable(t *testing.T) {
	store := &fakeCancelStore{cancelErr: ErrNotCancellable}
	worker := &fakeStopper{stops: false}
	_, _, err := CancelOrStop(context.Background(), store, worker, "t1")
	if !errors.Is(err, ErrNotCancellable) || store.gets != 0 {
		t.Errorf("err=%v gets=%d", err, store.gets)
	}
}

func TestWithoutAWorkerARunningTaskIsNotCancellable(t *testing.T) {
	store := &fakeCancelStore{cancelErr: ErrNotCancellable}
	_, _, err := CancelOrStop(context.Background(), store, nil, "t1")
	if !errors.Is(err, ErrNotCancellable) {
		t.Errorf("err=%v", err)
	}
}

func TestNotFoundAndOtherErrorsPassThroughAndTheWorkerIsNotAsked(t *testing.T) {
	boom := errors.New("database is down")
	for _, want := range []error{ErrNotFound, boom} {
		store := &fakeCancelStore{cancelErr: want}
		worker := &fakeStopper{stops: true}
		_, _, err := CancelOrStop(context.Background(), store, worker, "t1")
		if err != want || len(worker.asked) != 0 {
			t.Errorf("want %v: err=%v asked=%v", want, err, worker.asked)
		}
	}
}

func TestATaskThatCannotBeReadAfterTheStopIsAnError(t *testing.T) {
	store := &fakeCancelStore{cancelErr: ErrNotCancellable, getErr: ErrNotFound}
	_, _, err := CancelOrStop(context.Background(), store, &fakeStopper{stops: true}, "t1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("err=%v", err)
	}
}
