package workflowtest

import (
	"context"
	"sync"
	"time"

	"assistant/internal/workflow"
)

// MemoryRecorder keeps every StepRecord in memory, in order.
type MemoryRecorder struct {
	mu      sync.Mutex
	records []workflow.StepRecord
}

// Record implements workflow.Recorder.
func (m *MemoryRecorder) Record(_ context.Context, rec workflow.StepRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, rec)
}

// Records returns a copy of the records so far.
func (m *MemoryRecorder) Records() []workflow.StepRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]workflow.StepRecord(nil), m.records...)
}

// FakeClock is a workflow.Clock that moves forward by Step on every call, so
// durations in a trace are exact and repeatable.
type FakeClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

// NewFakeClock starts at start and advances by step per Now call.
func NewFakeClock(start time.Time, step time.Duration) *FakeClock {
	return &FakeClock{now: start, step: step}
}

// Now implements workflow.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}
