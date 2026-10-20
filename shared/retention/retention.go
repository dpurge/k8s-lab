// Package retention keeps tables from growing forever: a Rule says which rows
// of which table are too old, DeleteOlderThan removes them in small batches,
// and a Purger applies a set of rules once after start and then at every
// interval. It knows nothing about any application's tables.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Defaults: the first purge waits a minute so that startup is not loaded with
// it; a batch is small enough to delete quickly, with a short pause so other
// writers are never held up behind a long run of deletes.
const (
	defaultStartDelay = time.Minute
	defaultInterval   = 6 * time.Hour
	defaultBatchSize  = 5000
	defaultBatchPause = 50 * time.Millisecond
	day               = 24 * time.Hour
)

// MaxDays is the longest retention, in days, that configuration may ask for
// (100 years). It keeps days*24h far from overflowing time.Duration, whose
// limit is about 106751 days, so that a typo cannot turn into a negative
// retention that purges everything.
const MaxDays = 36500

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Rule says which rows of a table are purged: those whose TimeColumn is older
// than Retention and that match Condition.
type Rule struct {
	// Name identifies the rule in logs and metrics; it never reaches SQL.
	Name string
	// Table, TimeColumn and IDColumn are plain identifiers; they are validated
	// and quoted. IDColumn defaults to "id".
	Table      string
	TimeColumn string
	IDColumn   string
	// Condition is a static SQL fragment such as "status = 'done'". It is
	// pasted into the statement as it is: code constants only, never input.
	// Empty means every old row.
	Condition string
	// Retention is how long rows are kept; 0 keeps them forever.
	Retention time.Duration
}

func (r Rule) idColumn() string {
	if r.IDColumn == "" {
		return "id"
	}
	return r.IDColumn
}

// Validate reports a rule that cannot be executed safely.
func (r Rule) Validate() error {
	if r.Name == "" {
		return errors.New("retention: rule has no name")
	}
	for _, id := range []struct{ field, value string }{
		{"Table", r.Table}, {"TimeColumn", r.TimeColumn}, {"IDColumn", r.idColumn()},
	} {
		if !identifierPattern.MatchString(id.value) {
			return fmt.Errorf("retention: rule %q: %s %q is not a plain identifier", r.Name, id.field, id.value)
		}
	}
	if r.Retention < 0 {
		return fmt.Errorf("retention: rule %q: retention must not be negative, got %s", r.Name, r.Retention)
	}
	return nil
}

// deleteSQL is the statement of one batch: $1 is the cutoff, $2 the batch size.
func (r Rule) deleteSQL() string {
	table, id, at := pgx.Identifier{r.Table}.Sanitize(), pgx.Identifier{r.idColumn()}.Sanitize(), pgx.Identifier{r.TimeColumn}.Sanitize()
	condition := ""
	if r.Condition != "" {
		condition = " AND (" + r.Condition + ")"
	}
	return fmt.Sprintf("DELETE FROM %[1]s WHERE %[2]s IN (SELECT %[2]s FROM %[1]s WHERE %[3]s < $1%[4]s ORDER BY %[3]s LIMIT $2)",
		table, id, at, condition)
}

// DB is the part of a pgx pool a purge needs; *pgxpool.Pool implements it.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type settings struct {
	batchSize  int
	batchPause time.Duration
	now        func() time.Time
	startDelay time.Duration
	interval   time.Duration
	log        *slog.Logger
	onPurged   func(rule string, deleted int64)
}

// Option configures DeleteOlderThan or a Purger; an option that does not
// concern the one it is given to is ignored.
type Option func(*settings)

// WithBatchSize sets how many rows one delete statement removes.
func WithBatchSize(n int) Option { return func(s *settings) { s.batchSize = n } }

// WithBatchPause sets the pause between the batches of one rule.
func WithBatchPause(d time.Duration) Option { return func(s *settings) { s.batchPause = d } }

// WithClock sets the clock the cutoff is computed from.
func WithClock(now func() time.Time) Option { return func(s *settings) { s.now = now } }

// WithSchedule sets the wait before the first run and the one between runs.
func WithSchedule(startDelay, interval time.Duration) Option {
	return func(s *settings) { s.startDelay, s.interval = startDelay, interval }
}

// WithLogger sets the logger; the default is slog.Default().
func WithLogger(l *slog.Logger) Option { return func(s *settings) { s.log = l } }

// WithOnPurged sets a callback called after a rule deleted rows, for metrics.
func WithOnPurged(fn func(rule string, deleted int64)) Option {
	return func(s *settings) { s.onPurged = fn }
}

func newSettings(opts []Option) settings {
	s := settings{
		batchSize: defaultBatchSize, batchPause: defaultBatchPause,
		now: time.Now, startDelay: defaultStartDelay, interval: defaultInterval,
	}
	for _, o := range opts {
		o(&s)
	}
	if s.batchSize < 1 {
		s.batchSize = defaultBatchSize
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// DeleteOlderThan deletes the rows the rule selects whose time column is
// before cutoff, a batch at a time so that no single statement holds locks for
// long, and returns how many it deleted; on an error or a canceled ctx that is
// the count so far. The rule's Retention is not used: the caller owns the cutoff.
func DeleteOlderThan(ctx context.Context, db DB, rule Rule, cutoff time.Time, opts ...Option) (int64, error) {
	if err := rule.Validate(); err != nil {
		return 0, err
	}
	s := newSettings(opts)
	query := rule.deleteSQL()
	var total int64
	for {
		tag, err := db.Exec(ctx, query, cutoff, s.batchSize)
		if err != nil {
			return total, fmt.Errorf("retention: purge %s: %w", rule.Name, err)
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted < int64(s.batchSize) {
			return total, nil
		}
		// Checked first: with no pause the select below would pick at random.
		if err := ctx.Err(); err != nil {
			return total, err
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(s.batchPause):
		}
	}
}

// Purger applies its rules once shortly after start and then at every
// interval. A failed rule is logged and tried again at the next run; it never
// stops the loop or the other rules.
type Purger struct {
	db    DB
	rules []Rule
	s     settings
}

// NewPurger returns a purger of rules, or an error naming the first invalid one.
func NewPurger(db DB, rules []Rule, opts ...Option) (*Purger, error) {
	if db == nil {
		return nil, errors.New("retention: no database")
	}
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	return &Purger{db: db, rules: append([]Rule(nil), rules...), s: newSettings(opts)}, nil
}

// PurgeOnce applies every rule with a retention and returns how many rows each
// deleted, by rule name. A failing rule is logged, the others still run, and
// the errors are returned joined.
func (p *Purger) PurgeOnce(ctx context.Context) (map[string]int64, error) {
	deletedByRule := map[string]int64{}
	var errs []error
	for _, rule := range p.rules {
		if rule.Retention == 0 {
			continue
		}
		cutoff := p.s.now().UTC().Add(-rule.Retention)
		deleted, err := DeleteOlderThan(ctx, p.db, rule, cutoff, p.batchOptions()...)
		deletedByRule[rule.Name] = deleted
		if deleted > 0 {
			if p.s.onPurged != nil {
				p.s.onPurged(rule.Name, deleted)
			}
			p.s.log.Info("rows purged", "rule", rule.Name, "deleted", deleted,
				"older_than", cutoff.Format(time.RFC3339), "retention_days", int(rule.Retention/day))
		}
		if err != nil {
			// Only the error is logged, never a row's content.
			if ctx.Err() == nil {
				p.s.log.Error("could not purge rows", "rule", rule.Name, "error", err)
			}
			errs = append(errs, err)
		}
	}
	return deletedByRule, errors.Join(errs...)
}

func (p *Purger) batchOptions() []Option {
	return []Option{WithBatchSize(p.s.batchSize), WithBatchPause(p.s.batchPause)}
}

// Run purges until ctx is done. Start it in its own goroutine. With every rule
// disabled it only says so and returns.
func (p *Purger) Run(ctx context.Context) {
	var disabled []string
	for _, r := range p.rules {
		if r.Retention == 0 {
			disabled = append(disabled, r.Name)
		}
	}
	sort.Strings(disabled)
	for _, name := range disabled {
		p.s.log.Info("retention is disabled, rows are kept forever", "rule", name)
	}
	if len(disabled) == len(p.rules) {
		return
	}
	timer := time.NewTimer(p.s.startDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, _ = p.PurgeOnce(ctx) // PurgeOnce has logged every failure
		timer.Reset(p.s.interval)
	}
}
