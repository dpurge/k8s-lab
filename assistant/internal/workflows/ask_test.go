package workflows

import (
	"context"
	"errors"
	"strings"
	"testing"

	"assistant/internal/workflow"
)

type fakeLLM struct {
	reqs []workflow.Request
	resp workflow.Response
	err  error
}

func (f *fakeLLM) Complete(_ context.Context, req workflow.Request) (workflow.Response, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, f.err
}

func TestAskSendsThePromptAndReturnsTheTextAndModel(t *testing.T) {
	model := &fakeLLM{resp: workflow.Response{Text: "four", ModelID: "mid-gpu", InputTokens: 9, OutputTokens: 1}}
	r := New(&fakeClock{}, WithLLM(model))
	got, err := r.Run(context.Background(), task("ask", `{"prompt":"2+2?","system":"Be brief.","tags":["chat","strong"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"text":"four","model_id":"mid-gpu"}` {
		t.Errorf("result = %s", got)
	}
	if len(model.reqs) != 1 {
		t.Fatalf("calls = %d", len(model.reqs))
	}
	req := model.reqs[0]
	if req.Input != "2+2?" || req.System != "Be brief." || strings.Join(req.Tags, ",") != "chat,strong" || req.Schema != nil {
		t.Errorf("request = %+v", req)
	}
}

func TestAskNeedsOnlyAPrompt(t *testing.T) {
	model := &fakeLLM{resp: workflow.Response{Text: "hi"}}
	if _, err := New(&fakeClock{}, WithLLM(model)).Run(context.Background(), task("ask", `{"prompt":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if req := model.reqs[0]; req.System != "" || len(req.Tags) != 0 {
		t.Errorf("request = %+v", req)
	}
}

func TestAskRejectsABadPayloadWithoutCallingTheModel(t *testing.T) {
	cases := map[string]string{
		"missing prompt": `{}`,
		"blank prompt":   `{"prompt":"  \n"}`,
		"not an object":  `[]`,
		"not json":       `nope`,
		"empty":          ``,
		"unknown key":    `{"prompt":"x","temperature":1}`,
		"prompt type":    `{"prompt":5}`,
		"tags type":      `{"prompt":"x","tags":"chat"}`,
	}
	for name, payload := range cases {
		model := &fakeLLM{}
		_, err := New(&fakeClock{}, WithLLM(model)).Run(context.Background(), task("ask", payload))
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(model.reqs) != 0 {
			t.Errorf("%s: the model must not be called", name)
		}
	}
	_, err := New(&fakeClock{}, WithLLM(&fakeLLM{})).Run(context.Background(), task("ask", `{}`))
	if err == nil || !strings.Contains(err.Error(), "payload.prompt is required") {
		t.Errorf("err = %v", err)
	}
}

func TestAskFailsTheTaskWhenTheModelCallFails(t *testing.T) {
	cause := errors.New("no model has tags [huge]")
	_, err := New(&fakeClock{}, WithLLM(&fakeLLM{err: cause})).Run(context.Background(), task("ask", `{"prompt":"x"}`))
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "model call failed") {
		t.Errorf("err = %v", err)
	}
}

func TestAskExistsOnlyWithAModel(t *testing.T) {
	if _, err := New(&fakeClock{}).Run(context.Background(), task("ask", `{"prompt":"x"}`)); err == nil || err.Error() != "unknown workflow: ask" {
		t.Errorf("err = %v", err)
	}
	names := New(&fakeClock{}, WithLLM(&fakeLLM{})).Names()
	if strings.Join(sortedCopy(names), ",") != "ask,noop" {
		t.Errorf("names = %v", names)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
