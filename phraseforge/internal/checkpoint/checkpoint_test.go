package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// memStore is a Store in memory that counts saves.
type memStore struct {
	data         map[string][]json.RawMessage
	saves        int
	loadErr      error
	saveErr      error
	ctxErrAtSave error
	loadKeys     []string
}

func newMemStore() *memStore { return &memStore{data: map[string][]json.RawMessage{}} }

func (m *memStore) Load(_ context.Context, key string) ([]json.RawMessage, error) {
	m.loadKeys = append(m.loadKeys, key)
	return m.data[key], m.loadErr
}

func (m *memStore) Save(ctx context.Context, key string, results []json.RawMessage) error {
	m.ctxErrAtSave = ctx.Err()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saves++
	m.data[key] = results
	return nil
}

func raw(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

func TestWithoutAStoreARunDoesNothing(t *testing.T) {
	r := Start(context.Background(), "call", []string{"a", "b"})
	if r != nil {
		t.Fatalf("Start without a store = %v, want nil", r)
	}
	if _, ok := r.Done(0); ok {
		t.Error("nil Run reported a done chunk")
	}
	r.Record(context.Background(), 0, raw("x")) // must not panic
}

func TestRecordedChunksAreDoneInALaterRunOfTheSameCall(t *testing.T) {
	store := newMemStore()
	ctx := With(context.Background(), store)
	chunks := []string{"one", "two", "three"}

	first := Start(ctx, "translate", chunks)
	first.Record(ctx, 0, raw("1"))
	first.Record(ctx, 1, raw("2"))

	second := Start(ctx, "translate", chunks)
	for i, want := range []string{`"1"`, `"2"`} {
		got, ok := second.Done(i)
		if !ok || string(got) != want {
			t.Errorf("chunk %d: got (%s, %v), want %s", i, got, ok, want)
		}
	}
	if _, ok := second.Done(2); ok {
		t.Error("chunk 2 was never recorded but reported done")
	}
	second.Record(ctx, 2, raw("3"))
	if got, ok := Start(ctx, "translate", chunks).Done(2); !ok || string(got) != `"3"` {
		t.Errorf("chunk 2 after recording: (%s, %v)", got, ok)
	}
}

// A different text, chunking or purpose must never pick up these results.
func TestADifferentCallDoesNotReuseResults(t *testing.T) {
	store := newMemStore()
	ctx := With(context.Background(), store)
	Start(ctx, "translate", []string{"one", "two"}).Record(ctx, 0, raw("1"))

	for name, r := range map[string]*Run{
		"other chunking": Start(ctx, "translate", []string{"one two"}),
		"other text":     Start(ctx, "translate", []string{"one", "TWO"}),
		"other purpose":  Start(ctx, "transcribe", []string{"one", "two"}),
	} {
		if _, ok := r.Done(0); ok {
			t.Errorf("%s: reused a result that belongs to another call", name)
		}
	}
}

// Results are a prefix: recording out of order cannot leave a gap.
func TestRecordKeepsOnlyTheNextChunkInOrder(t *testing.T) {
	store := newMemStore()
	ctx := With(context.Background(), store)
	r := Start(ctx, "c", []string{"a", "b", "c"})
	r.Record(ctx, 2, raw("late"))
	if store.saves != 0 {
		t.Fatalf("saved a result with a gap before it")
	}
	r.Record(ctx, 0, raw("1"))
	r.Record(ctx, 0, raw("again")) // already recorded: ignored
	if store.saves != 1 {
		t.Errorf("saves = %d, want 1", store.saves)
	}
	if got, _ := r.Done(0); string(got) != `"1"` {
		t.Errorf("chunk 0 = %s, want the first result kept", got)
	}
}

func TestStoreFailuresNeverFailTheJob(t *testing.T) {
	store := newMemStore()
	store.loadErr = errors.New("load boom")
	ctx := With(context.Background(), store)
	r := Start(ctx, "c", []string{"a"})
	if _, ok := r.Done(0); ok {
		t.Error("a failed load reported a done chunk")
	}
	store.saveErr = errors.New("save boom")
	r.Record(ctx, 0, raw("1")) // logged, not fatal
	if _, ok := r.Done(0); ok {
		t.Error("a failed save was reported as done")
	}
}

// A chunk that finished right as the job was cancelled is still kept.
func TestRecordSavesEvenAfterCancellation(t *testing.T) {
	store := newMemStore()
	ctx, cancel := context.WithCancel(With(context.Background(), store))
	r := Start(ctx, "c", []string{"a"})
	cancel()
	r.Record(ctx, 0, raw("1"))
	if store.saves != 1 || store.ctxErrAtSave != nil {
		t.Fatalf("saves = %d, save ctx err = %v, want one save under a live context", store.saves, store.ctxErrAtSave)
	}
}

// progressStore is a memStore that also reports progress.
type progressStore struct {
	*memStore
	reports [][2]int
	err     error
}

func (p *progressStore) SetProgress(_ context.Context, done, total int) error {
	p.reports = append(p.reports, [2]int{done, total})
	return p.err
}

// A resumed run shows where it picks up, and every finished chunk moves it.
func TestProgressIsReportedAtStartAndAfterEachChunk(t *testing.T) {
	store := &progressStore{memStore: newMemStore()}
	ctx := With(context.Background(), store)
	chunks := []string{"a", "b", "c", "d"}

	first := Start(ctx, "c", chunks)
	first.Record(ctx, 0, raw("1"))
	first.Record(ctx, 1, raw("2"))
	store.reports = nil

	resumed := Start(ctx, "c", chunks)
	resumed.Record(ctx, 2, raw("3"))
	want := [][2]int{{2, 4}, {3, 4}}
	if !reflect.DeepEqual(store.reports, want) {
		t.Errorf("reports = %v, want %v", store.reports, want)
	}
}

func TestOneChunkHasNoProgressToShow(t *testing.T) {
	store := &progressStore{memStore: newMemStore()}
	ctx := With(context.Background(), store)
	r := Start(ctx, "c", []string{"only"})
	r.Record(ctx, 0, raw("1"))
	if len(store.reports) != 0 {
		t.Errorf("reports = %v, want none for a single chunk", store.reports)
	}
}

func TestAStoreWithoutProgressAndAFailingOneAreBothFine(t *testing.T) {
	plain := newMemStore()
	ctx := With(context.Background(), plain)
	Start(ctx, "c", []string{"a", "b"}).Record(ctx, 0, raw("1")) // no ProgressReporter: no panic

	failing := &progressStore{memStore: newMemStore(), err: errors.New("db down")}
	ctx = With(context.Background(), failing)
	r := Start(ctx, "c", []string{"a", "b"})
	r.Record(ctx, 0, raw("1"))
	if got, ok := r.Done(0); !ok || string(got) != `"1"` {
		t.Errorf("a failing progress report lost the result: (%s, %v)", got, ok)
	}
}
