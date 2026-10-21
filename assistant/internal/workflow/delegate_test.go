package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
)

const routerYAML = `id: router
version: 1
description: Routes a request to a workflow.
state_schema:
  type: object
  properties:
    reply: {type: string}
  additionalProperties: false
start: route
limits: {max_steps: 12, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  route:
    instructions: Hand the request to the workflow that fits, or answer.
    delegate: true
    next: [done]
  done:
    instructions: Finished.
    end: true
`

const greeterYAML = `id: greeter
version: 1
description: Greets.
intents:
  description: Greets the user by name.
  examples: ["say hello to me", "greet me"]
state_schema:
  type: object
  properties:
    name: {type: string}
  required: [name]
  additionalProperties: false
start: ask
limits: {max_steps: 5, max_tokens: 100000, max_retries: 0, max_visits: 3}
states:
  ask:
    instructions: Ask the name.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

// deeperYAML is a workflow that offers itself for delegation and may delegate.
const deeperYAML = `id: deeper
version: 1
description: Delegates again.
intents:
  description: Hands requests on.
state_schema:
  type: object
  properties:
    note: {type: string}
  additionalProperties: false
start: hand
limits: {max_steps: 5, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  hand:
    instructions: Hand on.
    delegate: true
    next: [done]
  done:
    instructions: Finished.
    end: true
`

type delegates struct {
	all map[string]*workflow.Workflow
}

func (d delegates) Workflow(id string) (*workflow.Workflow, bool) { w, ok := d.all[id]; return w, ok }

func (d delegates) Intents() []workflow.IntentInfo {
	var out []workflow.IntentInfo
	for id, w := range d.all {
		if w.Intents != nil {
			out = append(out, workflow.IntentInfo{ID: id, Description: w.Intents.Description, Examples: w.Intents.Examples})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func loadAll(t *testing.T, yamls ...string) (*workflow.Workflow, delegates) {
	t.Helper()
	all := map[string]*workflow.Workflow{}
	var first *workflow.Workflow
	for _, y := range yamls {
		wf, err := workflow.LoadBytes("x.yaml", []byte(y))
		if err != nil {
			t.Fatal(err)
		}
		all[wf.ID] = wf
		if first == nil {
			first = wf
		}
	}
	return first, delegates{all}
}

func delegateReply(patch, target, message, reason string) string {
	return replyJSON(patch, fmt.Sprintf(`{"type":"delegate","workflow":%q,"message":%s,"reason":%q}`, target, message, reason))
}

func delegating(t *testing.T, steps ...workflowtest.Step) (*harness, *workflow.Workflow, delegates) {
	t.Helper()
	parent, all := loadAll(t, routerYAML, greeterYAML, deeperYAML)
	h := newHarness(t, flowYAML, steps...)
	h.wf = parent
	h.eng.Delegates = all
	return h, parent, all
}

func TestARequestHandedToAWorkflowMakesItTheOneThatTalksToTheUser(t *testing.T) {
	h, _, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"say hello to Ada"`, "a greeting")),
		workflowtest.Reply(askReply(`{}`, `{"question":"What is your name?"}`)),
	)
	run, res := h.start(workflow.Input{Message: "say hello to Ada"})
	if res.Status != workflow.StatusWaiting || res.Workflow != "greeter" || res.Ask["question"] != "What is your name?" {
		t.Fatalf("result %+v", res)
	}
	if res.Routed == nil || res.Routed.Workflow != "greeter" || res.Routed.Reason != "a greeting" {
		t.Errorf("routed = %+v", res.Routed)
	}
	if run.Child == nil || run.Child.WorkflowID != "greeter" || run.Child.Depth != 1 || run.Status != workflow.StatusWaiting {
		t.Fatalf("run = %+v child %+v", run, run.Child)
	}
	// The child started from the delegated message, not from the user's own.
	contains(t, h.llm.Requests()[1].Input, `"observation":"say hello to Ada"`, "the child's first observation")
	contains(t, h.llm.Requests()[1].Input, `"id":"greeter"`, "the child runs its own workflow")
	recs := h.rec.Records()
	if recs[0].Action.Type != workflow.ActionDelegate || recs[0].Action.Workflow != "greeter" || recs[0].Workflow != "router" || recs[0].Depth != 0 {
		t.Errorf("the delegation is in the trace: %+v", recs[0])
	}
	if recs[1].Workflow != "greeter" || recs[1].Depth != 1 {
		t.Errorf("the child's step is marked: %+v", recs[1])
	}
}

func TestTheUsersAnswerGoesToTheChildAndItsResultComesBackToTheParent(t *testing.T) {
	h, parent, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"hi"`, "")),
		workflowtest.Reply(askReply(`{}`, `{"question":"Name?"}`)),
		workflowtest.Reply(gotoReply(`{"name":"Ada"}`, "done")),
		workflowtest.Reply(gotoReply(`{"reply":"Hello, Ada"}`, "done")),
	)
	run, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("%+v", res)
	}
	res, err := h.eng.Resume(context.Background(), parent, run, "Ada")
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("resume: %v %+v failure %+v", err, res, run.Failure)
	}
	contains(t, h.llm.Requests()[2].Input, `"observation":"Ada"`, "the answer reaches the child")
	contains(t, h.llm.Requests()[2].Input, `"id":"greeter"`, "the child, not the parent, takes the answer")
	contains(t, h.llm.Requests()[3].Input, `"delegate_result":{"state":{"name":"Ada"},"workflow":"greeter"}`, "the child's result is the parent's observation")
	if run.Child != nil || stateJSON(t, run) != `{"reply":"Hello, Ada"}` {
		t.Errorf("child %v state %s", run.Child, stateJSON(t, run))
	}
	// One trace, numbered in order, whose steps belong to the workflow that took them.
	var steps []string
	for _, r := range h.rec.Records() {
		steps = append(steps, fmt.Sprintf("%d:%s", r.Step, r.Workflow))
	}
	if got := strings.Join(steps, " "); got != "1:router 2:greeter 3:greeter 4:router" {
		t.Errorf("trace = %s", got)
	}
	if run.Steps != 4 {
		t.Errorf("the child's steps count against the parent: %d", run.Steps)
	}
}

func TestAChildsTokensCountAgainstTheParentsBudget(t *testing.T) {
	h, parent, _ := delegating(t,
		workflowtest.Step{Text: delegateReply(`{}`, "greeter", `"hi"`, ""), InputTokens: 100, OutputTokens: 10},
		workflowtest.Step{Text: askReply(`{}`, `{"question":"Name?"}`), InputTokens: 200, OutputTokens: 20},
		workflowtest.Step{Text: gotoReply(`{"name":"Ada"}`, "done"), InputTokens: 300, OutputTokens: 30},
		workflowtest.Step{Text: gotoReply(`{"reply":"x"}`, "done"), InputTokens: 50, OutputTokens: 5},
	)
	run, _ := h.start(workflow.Input{Message: "hi"})
	if _, err := h.eng.Resume(context.Background(), parent, run, "Ada"); err != nil {
		t.Fatal(err)
	}
	if want := 110 + 220 + 330 + 55; run.Tokens != want {
		t.Errorf("tokens = %d, want %d", run.Tokens, want)
	}
}

func TestAChildIsHeldToWhatTheParentHasLeftAndItsTokensEndTheParentToo(t *testing.T) {
	h, parent, _ := delegating(t,
		workflowtest.Step{Text: delegateReply(`{}`, "greeter", `"hi"`, ""), InputTokens: 760, OutputTokens: 40},
		workflowtest.Step{Text: askReply(`{}`, `{"question":"Name?"}`), InputTokens: 230, OutputTokens: 30},
	)
	parent.Limits.MaxTokens = 1000 // 800 used by the delegating step, 200 left for the child
	run, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusFailed || res.Failure.Reason != workflow.FailMaxTokens {
		t.Fatalf("%+v failure %+v", res, res.Failure)
	}
	var child workflow.StepRecord
	for _, r := range h.rec.Records() {
		if r.Workflow == "greeter" {
			child = r
		}
	}
	if child.Workflow != "greeter" || child.Outcome != workflow.OutcomeFailed || !strings.Contains(child.Error, "max_tokens") || !strings.Contains(child.Error, "of 200 tokens") {
		t.Errorf("the child was held to 200 tokens: %+v", child)
	}
	if run.Tokens != 800+260 {
		t.Errorf("the parent counts the child's tokens: %d", run.Tokens)
	}
}

func TestAChildThatFailsComesBackAsAnErrorTheParentCanHandle(t *testing.T) {
	h, _, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"hi"`, "")),
		workflowtest.Reply("not json"), // greeter has max_retries 0
		workflowtest.Reply(gotoReply(`{"reply":"I could not do that"}`, "done")),
	)
	run, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusSucceeded || stateJSON(t, run) != `{"reply":"I could not do that"}` {
		t.Fatalf("%+v state %s", res, stateJSON(t, run))
	}
	contains(t, h.llm.Requests()[2].Input, `"delegate_error":{"message":`, "the failure is the observation")
	contains(t, h.llm.Requests()[2].Input, `"reason":"retries_exhausted","workflow":"greeter"`, "naming the child")
}

func TestARequestNoWorkflowFitsIsAnsweredByTheRouterItself(t *testing.T) {
	h, _, _ := delegating(t, workflowtest.Reply(askReply(`{}`, `{"question":"Do you want a greeting or a form?"}`)))
	run, res := h.start(workflow.Input{Message: "what is the weather"})
	if res.Status != workflow.StatusWaiting || res.Workflow != "router" || res.Routed != nil || run.Child != nil {
		t.Errorf("result %+v child %v", res, run.Child)
	}
}

func TestDelegatingToAWorkflowThatIsNotOfferedIsRefusedAndCorrected(t *testing.T) {
	h, _, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "router", `"x"`, "")), // no intents: not offered
		workflowtest.Reply(delegateReply(`{}`, "nowhere", `"x"`, "")),
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"x"`, "")),
		workflowtest.Reply(askReply(`{}`, `{"question":"Name?"}`)),
	)
	h.wf.Limits.MaxRetries = 2
	_, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusWaiting || res.Workflow != "greeter" {
		t.Fatalf("%+v failure %+v", res, res.Failure)
	}
	recs := h.rec.Records()
	if !strings.Contains(recs[0].Error, `no workflow "router"`) || !strings.Contains(recs[0].Error, "deeper, greeter") {
		t.Errorf("the model is told what it may name: %q", recs[0].Error)
	}
	if recs[0].Outcome != workflow.OutcomeRetry || recs[1].Outcome != workflow.OutcomeRetry {
		t.Errorf("outcomes %v %v", recs[0].Outcome, recs[1].Outcome)
	}
}

func TestOnlyAStateThatDelegatesMayDelegate(t *testing.T) {
	parent, all := loadAll(t, routerYAML, greeterYAML)
	h := newHarness(t, flowYAML, workflowtest.Reply(delegateReply(`{}`, "greeter", `"x"`, "")), workflowtest.Reply(askReply(`{}`, `{"question":"?"}`)))
	h.wf = all.all["greeter"] // its state ask has no delegate flag
	h.wf.Limits.MaxRetries = 1
	h.eng.Delegates = all
	_ = parent
	_, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusWaiting || res.Workflow != "greeter" {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(h.rec.Records()[0].Error, `state "ask" cannot delegate`) {
		t.Errorf("error = %q", h.rec.Records()[0].Error)
	}
}

func TestDelegationIsLimitedInDepth(t *testing.T) {
	h, _, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "deeper", `"x"`, "")), // router (depth 0) -> deeper (depth 1)
		workflowtest.Reply(delegateReply(`{}`, "deeper", `"y"`, "")), // deeper may not go further
		workflowtest.Reply(askReply(`{}`, `{"question":"Giving up on delegating."}`)),
	)
	h.eng.MaxDepth = 1
	run, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusWaiting || res.Workflow != "deeper" || run.Child.Child != nil {
		t.Fatalf("%+v child %+v", res, run.Child)
	}
	refusal := h.rec.Records()[1].Error
	if !strings.Contains(refusal, "as deep as delegation goes") {
		t.Errorf("refusal = %q", refusal)
	}
	// And a model at the limit is not offered anything to delegate to.
	contains(t, h.llm.Requests()[1].Input, `"delegates":[]`, "no workflow is offered at the depth limit")
	contains(t, h.llm.Requests()[0].Input, `"delegates":[{"id":"deeper"`, "workflows are offered above it")
}

func TestTheModelSeesWhatEachDelegateIsForAndOnlyWhereItMayDelegate(t *testing.T) {
	h, _, _ := delegating(t, workflowtest.Reply(askReply(`{}`, `{"question":"?"}`)))
	h.start(workflow.Input{Message: "hi"})
	in := h.llm.Requests()[0].Input
	contains(t, in, `"delegates":[`, "the delegates")
	contains(t, in, `"id":"greeter","description":"Greets the user by name.","examples":["say hello to me","greet me"]`, "what the greeter is for")
	contains(t, h.llm.Requests()[0].System, "DELEGATION", "the guide")
	notContains(t, in[strings.Index(in, `"delegates"`):], `"id":"router"`, "a workflow without intents is not offered")

	plain := newHarness(t, flowYAML, replies(askReply(`{}`, `{"question":"?"}`))...)
	plain.eng.Delegates = delegates{map[string]*workflow.Workflow{}}
	plain.start(workflow.Input{})
	notContains(t, requestInput(t, plain, 0), "delegates", "a state that cannot delegate sees none")
	notContains(t, plain.llm.Requests()[0].System, "DELEGATION", "and gets no guide")
}

func TestAWaitingRunWithAChildSurvivesBeingStoredAndRestored(t *testing.T) {
	h, parent, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"hi"`, "")),
		workflowtest.Reply(askReply(`{"name":"pending"}`, `{"question":"Name?"}`)),
		workflowtest.Reply(gotoReply(`{"name":"Ada"}`, "done")),
		workflowtest.Reply(gotoReply(`{"reply":"ok"}`, "done")),
	)
	run, res := h.start(workflow.Input{Message: "hi"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("%+v", res)
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := workflow.RestoreRun(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Child == nil || restored.Child.WorkflowID != "greeter" || restored.Child.Depth != 1 || restored.Child.Routing == nil {
		t.Fatalf("restored child = %+v", restored.Child)
	}
	res, err = h.eng.Resume(context.Background(), parent, restored, "Ada")
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestAMessageForAChildThatIsNoLongerKnownFailsTheRun(t *testing.T) {
	h, parent, all := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"hi"`, "")),
		workflowtest.Reply(askReply(`{}`, `{"question":"Name?"}`)),
	)
	run, _ := h.start(workflow.Input{Message: "hi"})
	delete(all.all, "greeter")
	res, err := h.eng.Resume(context.Background(), parent, run, "Ada")
	if err != nil || res.Status != workflow.StatusFailed || !strings.Contains(res.Failure.Message, "not known any more") {
		t.Errorf("%v %+v", err, res)
	}
}

func TestTheTraceReadsAsOneRunWithTheChildsStepsMarked(t *testing.T) {
	h, parent, _ := delegating(t,
		workflowtest.Reply(delegateReply(`{}`, "greeter", `"hi"`, "a greeting")),
		workflowtest.Reply(askReply(`{}`, `{"question":"Name?"}`)),
		workflowtest.Reply(gotoReply(`{"name":"Ada"}`, "done")),
		workflowtest.Reply(gotoReply(`{"reply":"ok"}`, "done")),
	)
	run, _ := h.start(workflow.Input{Message: "hi"})
	h.eng.Resume(context.Background(), parent, run, "Ada")
	text := workflow.FormatTrace(h.rec.Records())
	for _, want := range []string{"action: delegate to greeter \"hi\" (a greeting)", "step 2 attempt 1 [greeter, depth 1] in ask", "step 4 attempt 1 in route"} {
		if !strings.Contains(text, want) {
			t.Errorf("the trace lacks %q:\n%s", want, text)
		}
	}
}
