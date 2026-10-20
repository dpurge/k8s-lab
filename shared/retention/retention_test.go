package retention_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"k8s-lab/shared/retention"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func clock() time.Time { return now }

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// execCall is one statement the fake database received.
type execCall struct {
	sql    string
	cutoff time.Time
	limit  int
}

// fakeDB answers each Exec with the next number of affected rows; once the
// script is used up it affects none. A table of errors per SQL substring makes
// one rule fail.
type fakeDB struct {
	mu       sync.Mutex
	calls    []execCall
	script   []int64
	failOn   string
	onExec   func(n int)
	failWith error
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	f.calls = append(f.calls, execCall{sql: sql, cutoff: args[0].(time.Time), limit: args[1].(int)})
	n := len(f.calls)
	failOn, failWith, onExec := f.failOn, f.failWith, f.onExec
	var rows int64
	if (failOn == "" || !strings.Contains(sql, failOn)) && len(f.script) > 0 {
		rows, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if onExec != nil {
		onExec(n)
	}
	if failOn != "" && strings.Contains(sql, failOn) {
		return pgconn.CommandTag{}, failWith
	}
	return pgconn.NewCommandTag("DELETE " + itoa(rows)), nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func (f *fakeDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var callsRule = retention.Rule{Name: "llm_calls", Table: "llm_calls", TimeColumn: "started_at", Retention: 30 * 24 * time.Hour}

func TestTheGeneratedSQLQuotesIdentifiersAndWrapsTheCondition(t *testing.T) {
	db := &fakeDB{}
	rule := retention.Rule{Name: "jobs_failed", Table: "jobs", TimeColumn: "updated_at", Condition: "status IN ('failed','cancelled')"}
	if _, err := retention.DeleteOlderThan(context.Background(), db, rule, now); err != nil {
		t.Fatal(err)
	}
	want := `DELETE FROM "jobs" WHERE "id" IN (SELECT "id" FROM "jobs" WHERE "updated_at" < $1 AND (status IN ('failed','cancelled')) ORDER BY "updated_at" LIMIT $2)`
	if got := db.calls[0].sql; got != want {
		t.Errorf("sql =\n%s\nwant\n%s", got, want)
	}
}

func TestTheGeneratedSQLWithoutConditionAndWithACustomIDColumn(t *testing.T) {
	db := &fakeDB{}
	rule := retention.Rule{Name: "r", Table: "t", TimeColumn: "at", IDColumn: "uid"}
	if _, err := retention.DeleteOlderThan(context.Background(), db, rule, now); err != nil {
		t.Fatal(err)
	}
	want := `DELETE FROM "t" WHERE "uid" IN (SELECT "uid" FROM "t" WHERE "at" < $1 ORDER BY "at" LIMIT $2)`
	if got := db.calls[0].sql; got != want {
		t.Errorf("sql =\n%s\nwant\n%s", got, want)
	}
}

func TestInjectionLookingIdentifiersAreRejected(t *testing.T) {
	bad := []string{"", "jobs; DROP TABLE jobs", `jobs"`, "a b", "1jobs", "jobs--", "sch.jobs", "jöbs"}
	for _, name := range bad {
		for field, rule := range map[string]retention.Rule{
			"Table":      {Name: "r", Table: name, TimeColumn: "at"},
			"TimeColumn": {Name: "r", Table: "t", TimeColumn: name},
			"IDColumn":   {Name: "r", Table: "t", TimeColumn: "at", IDColumn: name},
		} {
			if field == "IDColumn" && name == "" {
				continue // empty means the default
			}
			db := &fakeDB{}
			if _, err := retention.NewPurger(db, []retention.Rule{rule}); err == nil {
				t.Errorf("NewPurger accepted %s %q", field, name)
			}
			if _, err := retention.DeleteOlderThan(context.Background(), db, rule, now); err == nil {
				t.Errorf("DeleteOlderThan accepted %s %q", field, name)
			}
			if db.count() != 0 {
				t.Errorf("%s %q reached the database", field, name)
			}
		}
	}
}

func TestARuleNeedsANameAndANonNegativeRetention(t *testing.T) {
	if _, err := retention.NewPurger(&fakeDB{}, []retention.Rule{{Table: "t", TimeColumn: "at"}}); err == nil {
		t.Error("a rule without a name was accepted")
	}
	if _, err := retention.NewPurger(&fakeDB{}, []retention.Rule{{Name: "r", Table: "t", TimeColumn: "at", Retention: -time.Hour}}); err == nil {
		t.Error("a negative retention was accepted")
	}
}

func TestBatchesRunUntilOneDeletesFewerThanTheBatchSize(t *testing.T) {
	db := &fakeDB{script: []int64{2, 2, 2, 1}}
	got, err := retention.DeleteOlderThan(context.Background(), db, callsRule, now, retention.WithBatchSize(2), retention.WithBatchPause(0))
	if err != nil || got != 7 {
		t.Fatalf("DeleteOlderThan = %d, %v; want 7, nil", got, err)
	}
	if db.count() != 4 {
		t.Errorf("%d statements, want 4", db.count())
	}
	for _, c := range db.calls {
		if c.limit != 2 || !c.cutoff.Equal(now) {
			t.Errorf("statement args = %v, %v; want limit 2, cutoff %v", c.limit, c.cutoff, now)
		}
	}
}

func TestExactlyOneFullBatchEndsWithAnEmptyOne(t *testing.T) {
	db := &fakeDB{script: []int64{2}}
	got, err := retention.DeleteOlderThan(context.Background(), db, callsRule, now, retention.WithBatchSize(2), retention.WithBatchPause(0))
	if err != nil || got != 2 || db.count() != 2 {
		t.Errorf("DeleteOlderThan = %d, %v after %d statements; want 2, nil after 2", got, err, db.count())
	}
}

func TestAnErrorNamesTheRuleAndWrapsTheCause(t *testing.T) {
	boom := errors.New("database is down")
	db := &fakeDB{failOn: "DELETE", failWith: boom}
	got, err := retention.DeleteOlderThan(context.Background(), db, callsRule, now, retention.WithBatchSize(2))
	if !errors.Is(err, boom) || got != 0 {
		t.Errorf("DeleteOlderThan = %d, %v; want 0 and the database error", got, err)
	}
	if !strings.Contains(err.Error(), "llm_calls") {
		t.Errorf("error %q does not name the rule", err)
	}
}

func TestCancelBetweenBatchesStopsWithTheCountSoFar(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := &fakeDB{script: []int64{1, 1, 1}, onExec: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	got, err := retention.DeleteOlderThan(ctx, db, callsRule, now, retention.WithBatchSize(1), retention.WithBatchPause(0))
	if !errors.Is(err, context.Canceled) || got != 2 || db.count() != 2 {
		t.Errorf("DeleteOlderThan = %d, %v after %d statements; want 2, context.Canceled after 2", got, err, db.count())
	}
}

func TestCancelDuringThePauseStopsAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	db := &fakeDB{script: []int64{1, 1}}
	got, err := retention.DeleteOlderThan(ctx, db, callsRule, now, retention.WithBatchSize(1), retention.WithBatchPause(time.Hour))
	if !errors.Is(err, context.Canceled) || got != 1 {
		t.Errorf("DeleteOlderThan = %d, %v; want 1, context.Canceled", got, err)
	}
}

func TestTheCutoffIsTheRetentionBeforeTheInjectedNow(t *testing.T) {
	db := &fakeDB{}
	p, err := retention.NewPurger(db, []retention.Rule{{Name: "r", Table: "t", TimeColumn: "at", Retention: 7 * 24 * time.Hour}},
		retention.WithClock(func() time.Time { return now.In(time.FixedZone("x", 3600)) }), retention.WithLogger(discard()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PurgeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := now.AddDate(0, 0, -7); !db.calls[0].cutoff.Equal(want) {
		t.Errorf("cutoff = %v, want %v", db.calls[0].cutoff, want)
	}
}

func TestARuleWithRetentionZeroIsSkipped(t *testing.T) {
	db := &fakeDB{script: []int64{5}}
	p, _ := retention.NewPurger(db, []retention.Rule{{Name: "off", Table: "t", TimeColumn: "at"}}, retention.WithLogger(discard()))
	got, err := p.PurgeOnce(context.Background())
	if err != nil || len(got) != 0 || db.count() != 0 {
		t.Errorf("PurgeOnce = %v, %v after %d statements; want nothing", got, err, db.count())
	}
}

func TestOneFailingRuleDoesNotStopTheOthers(t *testing.T) {
	boom := errors.New("table is locked")
	db := &fakeDB{failOn: `"broken"`, failWith: boom, script: []int64{3}}
	var purged []string
	logs := &bytes.Buffer{}
	day := 24 * time.Hour
	p, err := retention.NewPurger(db, []retention.Rule{
		{Name: "first", Table: "broken", TimeColumn: "at", Retention: day},
		{Name: "second", Table: "fine", TimeColumn: "at", Retention: 30 * day},
	}, retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))), retention.WithClock(clock),
		retention.WithOnPurged(func(rule string, n int64) { purged = append(purged, rule) }))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.PurgeOnce(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the joined rule error", err)
	}
	if got["second"] != 3 || got["first"] != 0 {
		t.Errorf("deleted = %v, want second=3", got)
	}
	if len(purged) != 1 || purged[0] != "second" {
		t.Errorf("OnPurged called for %v, want only second", purged)
	}
	for _, want := range []string{`"msg":"could not purge rows"`, `"rule":"first"`, `"msg":"rows purged"`, `"rule":"second"`,
		`"deleted":3`, `"retention_days":30`, `"older_than":"2026-09-20T12:00:00Z"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s:\n%s", want, logs.String())
		}
	}
}

func TestNothingIsLoggedForARuleThatDeletedNothing(t *testing.T) {
	logs := &bytes.Buffer{}
	p, _ := retention.NewPurger(&fakeDB{}, []retention.Rule{callsRule}, retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	if _, err := p.PurgeOnce(context.Background()); err != nil || logs.Len() != 0 {
		t.Errorf("err = %v, log = %q; want none", err, logs.String())
	}
}

func TestRunPurgesRepeatedlySurvivesErrorsAndStopsWhenCanceled(t *testing.T) {
	db := &fakeDB{failOn: "DELETE", failWith: errors.New("database is down")}
	p, _ := retention.NewPurger(db, []retention.Rule{callsRule},
		retention.WithSchedule(time.Millisecond, time.Millisecond), retention.WithLogger(discard()))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { p.Run(ctx); close(stopped) }()
	deadline := time.Now().Add(5 * time.Second)
	for db.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.count() < 3 {
		t.Fatalf("only %d runs: an error must be retried at the next tick", db.count())
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after the context was canceled")
	}
}

func TestRunWaitsForTheStartDelay(t *testing.T) {
	db := &fakeDB{}
	p, _ := retention.NewPurger(db, []retention.Rule{callsRule}, retention.WithSchedule(time.Hour, time.Hour), retention.WithLogger(discard()))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { p.Run(ctx); close(stopped) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-stopped
	if db.count() != 0 {
		t.Errorf("%d statements before the start delay passed", db.count())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunSaysOncePerDisabledRuleAndReturnsWhenAllAreDisabled(t *testing.T) {
	logs, db := &syncBuffer{}, &fakeDB{script: []int64{5}}
	p, _ := retention.NewPurger(db, []retention.Rule{
		{Name: "a", Table: "t", TimeColumn: "at"}, {Name: "b", Table: "t", TimeColumn: "at"},
	}, retention.WithSchedule(time.Millisecond, time.Millisecond), retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	p.Run(context.Background()) // returns at once: nothing to schedule
	if db.count() != 0 {
		t.Errorf("%d statements for disabled rules", db.count())
	}
	out := logs.String()
	for _, name := range []string{`"rule":"a"`, `"rule":"b"`} {
		if n := strings.Count(out, name); n != 1 || !strings.Contains(out, "retention is disabled") {
			t.Errorf("%s logged %d times, want 1:\n%s", name, n, out)
		}
	}
}

func TestRunWithOneDisabledRuleStillRunsTheOthers(t *testing.T) {
	logs, db := &syncBuffer{}, &fakeDB{}
	p, _ := retention.NewPurger(db, []retention.Rule{
		{Name: "off", Table: "t", TimeColumn: "at"}, callsRule,
	}, retention.WithSchedule(time.Millisecond, time.Hour), retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { p.Run(ctx); close(stopped) }()
	deadline := time.Now().Add(5 * time.Second)
	for db.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-stopped
	if db.count() != 1 || !strings.Contains(logs.String(), `"rule":"off"`) {
		t.Errorf("%d statements, log %s", db.count(), logs.String())
	}
}
