package workflowtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"assistant/internal/workflow"
)

// TranscriptStep is one recorded model exchange: the request the engine sent
// and the reply that came back.
type TranscriptStep struct {
	System       string          `json:"system"`
	Input        json.RawMessage `json:"input"`
	Reply        string          `json:"reply"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
}

// Transcript is a whole run's exchanges with the model. Saved as a golden
// file it is readable in a code review, and replayed it makes the engine
// prove that it still sends exactly the same requests.
type Transcript struct {
	Steps []TranscriptStep `json:"steps"`
}

// Save writes the transcript as indented JSON.
func (t Transcript) Save(path string) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transcript: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write transcript %s: %w", path, err)
	}
	return nil
}

// LoadTranscript reads a transcript written by Save.
func LoadTranscript(path string) (Transcript, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Transcript{}, fmt.Errorf("read transcript %s: %w", path, err)
	}
	var t Transcript
	if err := json.Unmarshal(data, &t); err != nil {
		return Transcript{}, fmt.Errorf("parse transcript %s: %w", path, err)
	}
	return t, nil
}

// RecordingLLM passes calls to Inner and keeps a Transcript of them. Use it
// once, around a ScriptedLLM, to produce a golden file.
type RecordingLLM struct {
	Inner workflow.LLM

	mu    sync.Mutex
	steps []TranscriptStep
}

// Complete implements workflow.LLM.
func (r *RecordingLLM) Complete(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	resp, err := r.Inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, TranscriptStep{
		System: req.System, Input: compact(req.Input), Reply: resp.Text,
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
	})
	return resp, nil
}

// Transcript returns what was recorded so far.
func (r *RecordingLLM) Transcript() Transcript {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Transcript{Steps: append([]TranscriptStep(nil), r.steps...)}
}

// ReplayLLM answers from a Transcript and checks that every request equals
// the recorded one. A difference fails the call and is kept for Err, so an
// engine change that alters a prompt or the order of steps is caught.
type ReplayLLM struct {
	mu         sync.Mutex
	steps      []TranscriptStep
	calls      int
	mismatches []error
}

// NewReplayLLM returns a fake that replays t.
func NewReplayLLM(t Transcript) *ReplayLLM {
	return &ReplayLLM{steps: t.Steps}
}

// Complete implements workflow.LLM.
func (r *ReplayLLM) Complete(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Response{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls > len(r.steps) {
		err := fmt.Errorf("replay: call %d but the transcript has only %d steps", r.calls, len(r.steps))
		r.mismatches = append(r.mismatches, err)
		return workflow.Response{}, err
	}
	step := r.steps[r.calls-1]
	if d := firstDifference(step.System, req.System); d != "" {
		err := fmt.Errorf("replay: call %d: system prompt differs from the transcript: %s", r.calls, d)
		r.mismatches = append(r.mismatches, err)
		return workflow.Response{}, err
	}
	if d := firstDifference(string(compact(string(step.Input))), string(compact(req.Input))); d != "" {
		err := fmt.Errorf("replay: call %d: step input differs from the transcript: %s", r.calls, d)
		r.mismatches = append(r.mismatches, err)
		return workflow.Response{}, err
	}
	return workflow.Response{Text: step.Reply, InputTokens: step.InputTokens, OutputTokens: step.OutputTokens}, nil
}

// Err reports every mismatch and any recorded step that was never requested.
func (r *ReplayLLM) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.mismatches) > 0 {
		return r.mismatches[0]
	}
	if r.calls < len(r.steps) {
		return fmt.Errorf("replay: the transcript has %d steps but only %d were requested", len(r.steps), r.calls)
	}
	return nil
}

// compact removes insignificant whitespace from JSON text, returning the text
// unchanged when it is not valid JSON.
func compact(s string) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return json.RawMessage(s)
	}
	return buf.Bytes()
}

// firstDifference returns "" when want equals got, otherwise a short
// description of where they first differ.
func firstDifference(want, got string) string {
	if want == got {
		return ""
	}
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	return fmt.Sprintf("first difference at byte %d: transcript has %q, engine sent %q", i, excerpt(want, i), excerpt(got, i))
}

func excerpt(s string, at int) string {
	start := at - 20
	if start < 0 {
		start = 0
	}
	end := at + 40
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
