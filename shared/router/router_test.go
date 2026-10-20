package router_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

const baseConfig = `providers:
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
  - {id: gpu-box, type: ollama, base_url: "http://gpu-box.lan:11434", api_key_from: ""}
models:
  - {id: big-gpu, provider: gpu-box, model: gemma4:12b, tags: [chat, review], priority: 20}
  - {id: mid-gpu, provider: gpu-box, model: gemma4:e4b, tags: [chat], priority: 15}
  - {id: laptop-a, provider: laptop, model: gemma4:12b, tags: [chat, review, cheap], priority: 10}
  - {id: laptop-b, provider: laptop, model: gemma4:e2b, tags: [chat, cheap], priority: 10}
  - {id: embed, provider: laptop, model: bge-m3, tags: [embed], priority: 10}
routing:
  breaker: {failures: 3, cooldown_seconds: 60, rate_limited_cooldown_seconds: 30}
`

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

type rig struct {
	router   *router.Router
	provider *routertest.Provider
	clock    *routertest.Clock
	observer *routertest.Observer
}

func newRig(t *testing.T, config string, scripts map[string][]routertest.Result) *rig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{provider: routertest.NewProvider(scripts), clock: routertest.NewClock(t0), observer: &routertest.Observer{}}
	r.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": r.provider}, router.WithClock(r.clock), router.WithObserver(r.observer))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ids(ms []modelcfg.Model) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return strings.Join(out, ",")
}

func (r *rig) call(tags ...string) (router.Response, error) {
	return r.router.Complete(context.Background(), router.Request{System: "s", Input: "{}", Tags: tags})
}

// ── selection ────────────────────────────────────────────────────────────────

func TestSelectByTagsAndPriority(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	tests := []struct {
		tags []string
		want string
	}{
		{[]string{"chat"}, "big-gpu,mid-gpu,laptop-a,laptop-b"},
		{[]string{"review"}, "big-gpu,laptop-a"},
		{[]string{"chat", "review"}, "big-gpu,laptop-a"},
		{[]string{"chat", "cheap"}, "laptop-a,laptop-b"},
		{[]string{"embed"}, "embed"},
		{[]string{"cheap", "review"}, "laptop-a"},
		{nil, "big-gpu,mid-gpu,laptop-a,laptop-b,embed"},
		{[]string{}, "big-gpu,mid-gpu,laptop-a,laptop-b,embed"},
	}
	for _, tc := range tests {
		got, err := r.router.Select(tc.tags)
		if err != nil || ids(got) != tc.want {
			t.Errorf("tags %v: got %q, %v; want %q", tc.tags, ids(got), err, tc.want)
		}
	}
}

func TestEqualPrioritiesKeepTheOrderOfTheFile(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	got, _ := r.router.Select([]string{"cheap"})
	if ids(got) != "laptop-a,laptop-b" {
		t.Errorf("got %s", ids(got))
	}
	swapped := strings.Replace(strings.Replace(baseConfig, "laptop-a", "TMP", 1), "laptop-b", "laptop-a", 1)
	swapped = strings.Replace(swapped, "TMP", "laptop-b", 1)
	r = newRig(t, swapped, nil)
	got, _ = r.router.Select([]string{"cheap"})
	if ids(got) != "laptop-b,laptop-a" {
		t.Errorf("file order must decide: got %s", ids(got))
	}
}

func TestTagsThatNoModelHasNameTheTagsAndTheModelsChecked(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	_, err := r.router.Select([]string{"vision"})
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "[vision]") {
		t.Errorf("the tags are named: %s", msg)
	}
	for _, id := range []string{"big-gpu", "mid-gpu", "laptop-a", "laptop-b", "embed"} {
		if !strings.Contains(msg, id+" lacks the tags") {
			t.Errorf("model %s was checked and should be named: %s", id, msg)
		}
	}
	if _, err := r.call("vision"); !errors.As(err, &noModel) {
		t.Errorf("Complete must report the same: %v", err)
	}
	if len(r.provider.Calls()) != 0 {
		t.Errorf("no provider is called when no model can take the call")
	}
}

func TestTheErrorForNoTagsAtAllNamesNoTags(t *testing.T) {
	_, err := newRig(t, baseConfig, nil).router.Select([]string{"nope", "worse"})
	if err == nil || !strings.Contains(err.Error(), "[nope, worse]") {
		t.Errorf("err = %v", err)
	}
}

// ── fallback ─────────────────────────────────────────────────────────────────

func TestTheBestModelAnswers(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{"big-gpu": {routertest.OK("hello", 10, 3)}})
	resp, err := r.call("chat")
	if err != nil || resp.Text != "hello" || resp.InputTokens != 10 || resp.OutputTokens != 3 {
		t.Fatalf("%+v %v", resp, err)
	}
	if got := strings.Join(r.provider.Models(), ","); got != "big-gpu" {
		t.Errorf("called %s", got)
	}
}

func TestTheRequestReachesTheProviderUnchanged(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	schema := map[string]any{"type": "object"}
	_, err := r.router.Complete(context.Background(), router.Request{System: "sys", Input: `{"a":1}`, Schema: schema, Tags: []string{"review"}})
	if err != nil {
		t.Fatal(err)
	}
	got := r.provider.Calls()[0]
	if got.Request.System != "sys" || got.Request.Input != `{"a":1}` || got.Request.Schema["type"] != "object" || got.ProviderID != "gpu-box" {
		t.Errorf("call = %+v", got)
	}
}

func TestAFailureFallsBackToTheNextModel(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailServer, "boom")},
		"mid-gpu":  {routertest.Fail(router.FailTimeout, "slow")},
		"laptop-a": {routertest.OK("from the laptop", 5, 5)},
	})
	resp, err := r.call("chat")
	if err != nil || resp.Text != "from the laptop" {
		t.Fatalf("%+v %v", resp, err)
	}
	if got := strings.Join(r.provider.Models(), ","); got != "big-gpu,mid-gpu,laptop-a" {
		t.Errorf("order = %s", got)
	}
}

func TestWhenEveryModelFailsTheErrorSaysWhyForEach(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailServer, "gpu exploded")},
		"laptop-a": {routertest.Fail(router.FailTimeout, "no first token")},
	})
	_, err := r.call("review")
	var all *router.AllFailedError
	if !errors.As(err, &all) || len(all.Attempts) != 2 {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"[review]", "big-gpu: server: gpu exploded", "laptop-a: timeout: no first token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q missing from %s", want, msg)
		}
	}
	var f *router.Failure
	if !errors.As(err, &f) {
		t.Errorf("the failures must be reachable with errors.As")
	}
}

func TestAClientErrorFallsBackButDoesNotCountAgainstTheModel(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailClient, "bad request")},
		"laptop-a": {routertest.OK("ok", 1, 1)},
	})
	for i := 0; i < 10; i++ {
		if _, err := r.call("review"); err != nil {
			t.Fatal(err)
		}
	}
	calls := r.provider.Models()
	bigCalls := 0
	for _, m := range calls {
		if m == "big-gpu" {
			bigCalls++
		}
	}
	if bigCalls != 10 {
		t.Errorf("a refused request says nothing about the model's health: big-gpu was tried %d of 10 times", bigCalls)
	}
	for _, s := range r.router.Status() {
		if s.ID == "big-gpu" && !s.Available {
			t.Errorf("big-gpu must stay available")
		}
	}
}

func TestAnUnclassifiedErrorCountsAsTheProvidersFault(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"embed": {{Err: errors.New("something unexpected")}},
	})
	for i := 0; i < 3; i++ {
		r.call("embed")
	}
	if status(r, "embed").Available {
		t.Errorf("three unexplained failures in a row should set the model aside")
	}
}

func status(r *rig, id string) router.ModelStatus {
	for _, s := range r.router.Status() {
		if s.ID == id {
			return s
		}
	}
	panic("no model " + id)
}

// ── the breaker ──────────────────────────────────────────────────────────────

func TestThreeFailuresInARowSetAModelAsideForTheCooldown(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"embed": {routertest.Fail(router.FailServer, "e1"), routertest.Fail(router.FailTimeout, "e2"), routertest.Fail(router.FailServer, "e3"), routertest.OK("back", 1, 1)},
	})
	r.call("embed")
	r.call("embed")
	if !status(r, "embed").Available {
		t.Fatal("two failures do not trip it")
	}
	r.call("embed")
	s := status(r, "embed")
	if s.Available {
		t.Fatal("three failures in a row trip it")
	}
	if !s.Until.After(t0.Add(59*time.Second)) || s.Until.After(t0.Add(70*time.Second)) {
		t.Errorf("until = %v, want about a minute from now", s.Until)
	}

	callsBefore := len(r.provider.Calls())
	_, err := r.call("embed")
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) || !strings.Contains(err.Error(), "embed is unavailable until") {
		t.Errorf("err = %v", err)
	}
	if len(r.provider.Calls()) != callsBefore {
		t.Errorf("a model that is set aside is skipped without a call")
	}

	r.clock.Advance(61 * time.Second)
	if !status(r, "embed").Available {
		t.Errorf("after the cooldown it is available again")
	}
	resp, err := r.call("embed")
	if err != nil || resp.Text != "back" {
		t.Errorf("after the cooldown the next call tries it: %+v %v", resp, err)
	}
}

func TestOneFailureAfterTheCooldownSetsItAsideAgain(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{"embed": {routertest.Fail(router.FailServer, "down")}})
	for i := 0; i < 3; i++ {
		r.call("embed")
	}
	r.clock.Advance(61 * time.Second)
	r.call("embed")
	if status(r, "embed").Available {
		t.Errorf("a model that fails its first call after the cooldown must not get three more chances")
	}
}

func TestASuccessResetsTheCount(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"embed": {
			routertest.Fail(router.FailServer, "1"), routertest.Fail(router.FailServer, "2"), routertest.OK("fine", 1, 1),
			routertest.Fail(router.FailServer, "3"), routertest.Fail(router.FailServer, "4"),
		},
	})
	for i := 0; i < 5; i++ {
		r.call("embed")
	}
	if !status(r, "embed").Available {
		t.Errorf("failures that are not in a row must not trip the breaker")
	}
}

func TestARateLimitSetsAModelAsideBrieflyAtOnce(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailRateLimited, "429"), routertest.OK("fine", 1, 1)},
		"laptop-a": {routertest.OK("laptop", 1, 1)},
	})
	resp, _ := r.call("review")
	if resp.Text != "laptop" {
		t.Fatalf("falls back: %+v", resp)
	}
	s := status(r, "big-gpu")
	if s.Available || !s.Until.Before(t0.Add(45*time.Second)) || !s.Until.After(t0.Add(25*time.Second)) {
		t.Errorf("one rate limit holds it out for the short cooldown: %+v", s)
	}
	r.clock.Advance(31 * time.Second)
	if !status(r, "big-gpu").Available {
		t.Errorf("available after 30 s")
	}
	resp, _ = r.call("review")
	if resp.Text != "fine" {
		t.Errorf("tried again after the cooldown: %+v", resp)
	}
}

func TestAConnectionFailureSetsEveryModelOnThatMachineAside(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailConnection, "connection refused")},
		"laptop-a": {routertest.OK("laptop answered", 1, 1)},
	})
	resp, err := r.call("chat")
	if err != nil || resp.Text != "laptop answered" {
		t.Fatalf("%+v %v", resp, err)
	}
	// mid-gpu is on the same machine as big-gpu, so it is not tried at all.
	if got := strings.Join(r.provider.Models(), ","); got != "big-gpu,laptop-a" {
		t.Errorf("called %s; the other model on the dead machine must be skipped", got)
	}
	for _, id := range []string{"big-gpu", "mid-gpu"} {
		if status(r, id).Available {
			t.Errorf("%s shares the dead machine and must be unavailable", id)
		}
	}
	for _, id := range []string{"laptop-a", "laptop-b", "embed"} {
		if !status(r, id).Available {
			t.Errorf("%s is on another machine and must stay available", id)
		}
	}
	if _, err := r.call("chat"); err != nil {
		t.Fatal(err)
	}
	if got := r.provider.Models(); got[len(got)-1] != "laptop-a" || len(got) != 3 {
		t.Errorf("later calls do not touch the dead machine: %v", got)
	}
}

func TestAMachineComesBackAfterTheCooldownAndAnAnswerClearsTheHold(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {routertest.Fail(router.FailConnection, "no route"), routertest.OK("gpu is back", 1, 1)},
	})
	r.call("chat")
	r.clock.Advance(61 * time.Second)
	resp, _ := r.call("chat")
	if resp.Text != "gpu is back" {
		t.Fatalf("after the cooldown the machine is tried: %+v", resp)
	}
	if !status(r, "mid-gpu").Available || !status(r, "big-gpu").Available {
		t.Errorf("an answer from the machine clears the hold on all of its models")
	}
}

func TestACallThatSucceedsAfterTheMachineWasDeclaredDownClearsTheHold(t *testing.T) {
	release := make(chan struct{})
	slow := routertest.OK("late but fine", 1, 1)
	slow.Wait = release
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {slow, routertest.Fail(router.FailConnection, "blip")},
		"laptop-a": {routertest.OK("laptop", 1, 1)},
	})
	first := make(chan string, 1)
	go func() {
		resp, _ := r.call("chat")
		first <- resp.Text
	}()
	for len(r.provider.Calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	// While the first call is still waiting on the machine, a second one finds it unreachable.
	if resp, err := r.call("chat"); err != nil || resp.Text != "laptop" {
		t.Fatalf("second call: %+v %v", resp, err)
	}
	if status(r, "big-gpu").Available {
		t.Fatal("the failed second call should have set the machine aside")
	}
	close(release)
	if text := <-first; text != "late but fine" {
		t.Fatalf("first call: %q", text)
	}
	if !status(r, "big-gpu").Available || !status(r, "mid-gpu").Available {
		t.Errorf("a call that the machine answered proves it is up: the hold must be cleared")
	}
}

func TestEveryModelOnTheDeadMachineDownMeansTheOthersTakeOver(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailConnection, "down")},
		"laptop-a": {routertest.OK("laptop", 1, 1)},
	})
	for i := 0; i < 3; i++ {
		if resp, err := r.call("chat"); err != nil || resp.Text != "laptop" {
			t.Fatalf("call %d: %+v %v", i, resp, err)
		}
	}
	gpuCalls := 0
	for _, m := range r.provider.Models() {
		if m == "big-gpu" || m == "mid-gpu" {
			gpuCalls++
		}
	}
	if gpuCalls != 1 {
		t.Errorf("the dead machine is tried once, not on every call: %d", gpuCalls)
	}
}

// ── cancellation ─────────────────────────────────────────────────────────────

func TestACanceledContextStopsTheChainAndIsNotHeldAgainstTheModel(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{"big-gpu": {{Block: true}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.router.Complete(ctx, router.Request{Tags: []string{"chat"}})
		done <- err
	}()
	for len(r.provider.Calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(r.provider.Models(), ","); got != "big-gpu" {
		t.Errorf("no fallback after a cancellation: %s", got)
	}
	if !status(r, "big-gpu").Available {
		t.Errorf("a cancellation is not the model's fault")
	}
	attempts := r.observer.Attempts()
	if len(attempts) != 1 || attempts[0].Outcome != router.OutcomeCanceled {
		t.Errorf("attempts = %+v", attempts)
	}
}

func TestAContextThatIsAlreadyCanceledCallsNobody(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.router.Complete(ctx, router.Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if len(r.provider.Calls()) != 0 || len(r.observer.Attempts()) != 0 {
		t.Errorf("nothing should have been called or recorded")
	}
}

func TestAProviderThatReportsCanceledWithoutTheContextEndingIsStillNotCounted(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{"embed": {routertest.Fail(router.FailCanceled, "gave up")}})
	for i := 0; i < 5; i++ {
		resp, err := r.router.Complete(context.Background(), router.Request{Tags: []string{"embed"}})
		var allFailed *router.AllFailedError
		if !errors.As(err, &allFailed) || len(allFailed.Attempts) != 1 || allFailed.Attempts[0].ModelID != "embed" {
			t.Fatalf("call %d: err = %v, want an AllFailedError naming embed", i, err)
		}
		if resp.Text != "" {
			t.Errorf("call %d: a failed call carries no text: %+v", i, resp)
		}
	}
	if !status(r, "embed").Available {
		t.Errorf("a canceled call must not count toward the breaker")
	}
}

func TestAProviderThatReportsCanceledWhileTheCallerWaitsFallsBackToTheNextModel(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {routertest.Fail(router.FailCanceled, "gave up")},
		"mid-gpu": {routertest.OK("mid answered", 3, 4)},
	})
	resp, err := r.call("chat")
	if err != nil || resp.Text != "mid answered" || resp.ModelID != "mid-gpu" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if got := strings.Join(r.provider.Models(), ","); got != "big-gpu,mid-gpu" {
		t.Errorf("called %s", got)
	}
	attempts := r.observer.Attempts()
	if len(attempts) != 2 || attempts[0].Outcome != router.OutcomeCanceled || attempts[1].Outcome != router.OutcomeOK {
		t.Errorf("attempts = %+v", attempts)
	}
	if !status(r, "big-gpu").Available {
		t.Errorf("a canceled call must not count toward the breaker")
	}
}

// ── what the observer hears ──────────────────────────────────────────────────

func TestEveryAttemptIsReportedWithItsOutcome(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailTimeout, "first token never came")},
		"mid-gpu":  {routertest.Fail(router.FailRateLimited, "429")},
		"laptop-a": {routertest.OK("done", 120, 30)},
	})
	r.clock.Step = 5 * time.Second
	if _, err := r.call("chat"); err != nil {
		t.Fatal(err)
	}
	got := r.observer.Attempts()
	if len(got) != 3 {
		t.Fatalf("attempts = %+v", got)
	}
	want := []struct{ model, provider, name, outcome string }{
		{"big-gpu", "gpu-box", "gemma4:12b", router.OutcomeTimeout},
		{"mid-gpu", "gpu-box", "gemma4:e4b", router.OutcomeRateLimited},
		{"laptop-a", "laptop", "gemma4:12b", router.OutcomeOK},
	}
	for i, w := range want {
		a := got[i]
		if a.ModelID != w.model || a.ProviderID != w.provider || a.ModelName != w.name || a.Outcome != w.outcome {
			t.Errorf("attempt %d = %+v", i, a)
		}
		if strings.Join(a.Tags, ",") != "chat" {
			t.Errorf("attempt %d tags = %v", i, a.Tags)
		}
		if a.Duration != 5*time.Second || a.StartedAt.IsZero() {
			t.Errorf("attempt %d timing = %v at %v", i, a.Duration, a.StartedAt)
		}
	}
	if got[0].Error != "first token never came" || got[1].Error != "429" || got[2].Error != "" {
		t.Errorf("errors = %q %q %q", got[0].Error, got[1].Error, got[2].Error)
	}
	if got[2].InputTokens != 120 || got[2].OutputTokens != 30 || got[0].InputTokens != 0 {
		t.Errorf("tokens belong to the successful attempt only: %+v", got)
	}
}

func TestSkippingAModelIsNotAnAttempt(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{"big-gpu": {routertest.Fail(router.FailConnection, "down")}})
	r.call("chat")
	got := r.observer.Attempts()
	for _, a := range got {
		if a.ModelID == "mid-gpu" {
			t.Errorf("mid-gpu was never called, so it cannot be an attempt: %+v", got)
		}
	}
}

// ── construction, status and concurrency ─────────────────────────────────────

func TestNewRouterNeedsAnImplementationForEveryProviderType(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("m.yaml", []byte(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.NewRouter(cfg, map[string]router.Provider{}); err == nil || !strings.Contains(err.Error(), `type "ollama", which has no implementation`) {
		t.Errorf("err = %v", err)
	}
}

func TestStatusListsEveryModelInFileOrder(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	s := r.router.Status()
	if len(s) != 5 || s[0].ID != "big-gpu" || s[4].ID != "embed" {
		t.Fatalf("status = %+v", s)
	}
	for _, m := range s {
		if !m.Available || !m.Until.IsZero() || len(m.Tags) == 0 {
			t.Errorf("fresh status = %+v", m)
		}
	}
	if s[0].Provider != "gpu-box" || s[0].Priority != 20 {
		t.Errorf("status carries provider and priority: %+v", s[0])
	}
}

func TestRoutingIsSafeUnderConcurrentCalls(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailServer, "x"), routertest.OK("ok", 1, 1), routertest.Fail(router.FailConnection, "y")},
		"laptop-a": {routertest.OK("ok", 1, 1)},
	})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tags := []string{"chat"}
			if i%3 == 0 {
				tags = []string{"review"}
			}
			r.router.Complete(context.Background(), router.Request{Tags: tags})
			r.router.Status()
		}(i)
		if i%10 == 9 {
			r.clock.Advance(30 * time.Second)
		}
	}
	wg.Wait()
	if len(r.observer.Attempts()) < 40 {
		t.Errorf("every call makes at least one attempt: %d", len(r.observer.Attempts()))
	}
}

func TestFailureHasAReadableMessageAndUnwraps(t *testing.T) {
	inner := fmt.Errorf("dial tcp: refused")
	f := &router.Failure{Kind: router.FailConnection, Err: inner}
	if f.Error() != "connection: dial tcp: refused" || !errors.Is(f, inner) {
		t.Errorf("failure = %q", f.Error())
	}
}

// ── call check ───────────────────────────────────────────────────────────────

var errRefused = errors.New("refused by the check")

func checkedRig(t *testing.T, check func(context.Context) error) *rig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{provider: routertest.NewProvider(nil), clock: routertest.NewClock(t0), observer: &routertest.Observer{}}
	r.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": r.provider}, router.WithClock(r.clock), router.WithObserver(r.observer), router.WithCallCheck(check))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestACallCheckThatFailsRefusesTheCallUnchangedAndCallsNobody(t *testing.T) {
	r := checkedRig(t, func(context.Context) error { return errRefused })
	_, err := r.router.Complete(context.Background(), router.Request{Tags: []string{"chat"}})
	if err != errRefused {
		t.Errorf("err = %v; want the check's error unwrapped", err)
	}
	if len(r.provider.Calls()) != 0 || len(r.observer.Attempts()) != 0 {
		t.Errorf("nothing may be called or recorded for a refused call")
	}
}

func TestACallCheckThatPassesLetsTheCallThrough(t *testing.T) {
	r := checkedRig(t, func(context.Context) error { return nil })
	if _, err := r.router.Complete(context.Background(), router.Request{Tags: []string{"chat"}}); err != nil {
		t.Fatal(err)
	}
	if len(r.provider.Calls()) != 1 {
		t.Errorf("calls = %d", len(r.provider.Calls()))
	}
}

func TestACallCheckSeesTheCallsContext(t *testing.T) {
	type key struct{}
	r := checkedRig(t, func(ctx context.Context) error {
		if ctx.Value(key{}) == nil {
			return errRefused
		}
		return nil
	})
	if _, err := r.router.Complete(context.Background(), router.Request{}); err != errRefused {
		t.Errorf("bare context: err = %v", err)
	}
	if _, err := r.router.Complete(context.WithValue(context.Background(), key{}, 1), router.Request{}); err != nil {
		t.Errorf("marked context: err = %v", err)
	}
}

func TestNoCallCheckMeansABareContextIsFine(t *testing.T) {
	r := newRig(t, baseConfig, nil)
	if _, err := r.call("chat"); err != nil {
		t.Errorf("without the option a bare context is fine: %v", err)
	}
}

func TestTheResponseSaysWhichModelAnswered(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {routertest.Fail(router.FailServer, "down")},
		"mid-gpu": {routertest.OK("x", 1, 1)},
	})
	resp, err := r.call("chat")
	if err != nil || resp.ModelID != "mid-gpu" {
		t.Errorf("model = %q, err %v", resp.ModelID, err)
	}
}

func TestFailureDetailAppearsOnlyWhenSet(t *testing.T) {
	inner := fmt.Errorf("overloaded")
	cases := []struct {
		f    *router.Failure
		want string
	}{
		{&router.Failure{Kind: router.FailServer, Err: inner}, "server: overloaded"},
		{&router.Failure{Kind: router.FailServer, Err: inner, HTTPStatus: 529}, "server: overloaded (HTTP 529)"},
		{&router.Failure{Kind: router.FailServer, Err: inner, RequestID: "req_1"}, "server: overloaded (request-id req_1)"},
		{&router.Failure{Kind: router.FailServer, Err: inner, HTTPStatus: 529, RequestID: "req_1"}, "server: overloaded (HTTP 529, request-id req_1)"},
	}
	for _, c := range cases {
		if c.f.Error() != c.want || !errors.Is(c.f, inner) {
			t.Errorf("got %q, want %q", c.f.Error(), c.want)
		}
	}
}
