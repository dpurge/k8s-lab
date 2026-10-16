package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
)

const learnTopicPath = "../../workflows/learn-topic.yaml"

var learnTools = []string{"memory_search", "memory_add", "rag_search", "web_search"}

func learnToolSet(t *testing.T) (map[string]*workflowtest.Tool, *workflow.Registry) {
	t.Helper()
	query := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"query"},
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
	}
	text := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"text"},
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	}
	tools := map[string]*workflowtest.Tool{
		"memory_search": {ToolName: "memory_search", Schema: query, Fn: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"memories": []any{}}, nil
		}},
		"memory_add": {ToolName: "memory_add", Schema: text, Fn: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"saved": true}, nil
		}},
		"rag_search": {ToolName: "rag_search", Schema: query, Fn: func(_ context.Context, args map[string]any) (any, error) {
			return map[string]any{"documents": []any{"RAG:" + args["query"].(string)}}, nil
		}},
		"web_search": {ToolName: "web_search", Schema: query},
	}
	var list []workflow.Tool
	for _, name := range learnTools {
		list = append(list, tools[name])
	}
	reg, err := workflow.NewRegistry(list...)
	if err != nil {
		t.Fatal(err)
	}
	return tools, reg
}

func loadLearnTopic(t *testing.T) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Load(learnTopicPath)
	if err != nil {
		t.Fatalf("load learn-topic: %v", err)
	}
	return wf
}

// The scripted conversation: a learner asks to learn Arabic, is assessed with
// two questions, gets one lesson, and finishes. The model's replies, in the
// order the engine will ask for them, grouped by the call that needs them.
func learnTopicScript() (start []string, turns [][]string) {
	start = []string{
		queryReply("memory_search", `{}`, "learner Arabic"),
		askReply(`{"goal":"learn Arabic"}`, `{"text":"What do you want to do with Arabic, and what do you know already?"}`),
	}
	turns = [][]string{
		{ // after "I want to read the news. I only know the alphabet."
			gotoReply(`{"goal":"read Arabic news","level":"unknown"}`, "plan"),
			queryReply("rag_search", `{}`, "Arabic learning program reading news"),
			gotoReply(`{"program":["Alphabet and sounds","Basic grammar","Reading simple news"],"unit_index":0}`, "assess"),
			queryReply("rag_search", `{}`, "Arabic placement test"),
			askReply(`{"assessment":{"asked":1,"correct":0},"pending":{"question":"Q1: write the letter alif","expected":"ا"}}`, `{"kind":"quiz","number":1,"question":"Q1: write the letter alif"}`),
		},
		{ // after "A1 answer"
			askReply(`{"assessment":{"asked":2,"correct":1},"pending":{"question":"Q2: what does kitab mean?","expected":"book"}}`, `{"kind":"quiz","number":2,"question":"Q2: what does kitab mean?"}`),
		},
		{ // after "A2 answer"
			gotoReply(`{"level":"beginner","mastered":["alphabet"],"gaps":["verb conjugation"],"assessment":{"asked":2,"correct":2},"pending":null}`, "teach"),
			queryReply("rag_search", `{}`, "Basic grammar lesson 1"),
			askReply(`{"unit_index":1,"pending":{"question":"Translate: the book","expected":"الكتاب"}}`, `{"kind":"lesson","title":"Nouns and the definite article","exercise":{"prompt":"Translate: the book"}}`),
		},
		{ // after "my exercise answer"
			replyJSON(`{"lessons_done":1,"mastered":["alphabet","basic nouns"],"gaps":["verb conjugation"],"pending":null}`,
				`{"type":"tool","tool":"memory_add","args":{"text":"Learner is a beginner in Arabic; goal: read news"}}`),
			gotoReply(`{}`, "done"),
		},
	}
	return start, turns
}

var learnTopicMessages = []string{
	"I want to read the news. I only know the alphabet.",
	"A1 answer",
	"A2 answer",
	"my exercise answer",
}

type learnOutcome struct {
	run      *workflow.Run
	statuses []workflow.Status
	asks     []map[string]any
	rec      *workflowtest.MemoryRecorder
	tools    map[string]*workflowtest.Tool
}

// runLearnTopic plays the whole conversation, storing and restoring the run
// between turns the way a worker would.
func runLearnTopic(t *testing.T, llm workflow.LLM, prompts *workflow.Prompts) learnOutcome {
	t.Helper()
	tools, reg := learnToolSet(t)
	rec := &workflowtest.MemoryRecorder{}
	eng := &workflow.Engine{
		LLM: llm, Tools: reg, Prompts: prompts, Recorder: rec,
		Clock: workflowtest.NewFakeClock(fixedStart, clockStep),
	}
	wf := loadLearnTopic(t)
	out := learnOutcome{rec: rec, tools: tools}

	run, res, err := eng.Start(context.Background(), wf, "learn-1", workflow.Input{
		Language: "arb", Message: "I want to learn Arabic",
	})
	if err != nil {
		t.Fatalf("Start: %v%s", err, scriptProblem(llm))
	}
	out.statuses = append(out.statuses, res.Status)
	out.asks = append(out.asks, res.Ask)

	for i, msg := range learnTopicMessages {
		restored, err := workflow.RestoreRun([]byte(mustJSON(t, run)))
		if err != nil {
			t.Fatalf("restore before turn %d: %v", i+1, err)
		}
		run = restored
		res, err := eng.Resume(context.Background(), wf, run, msg)
		if err != nil {
			t.Fatalf("Resume turn %d: %v%s", i+1, err, scriptProblem(llm))
		}
		out.statuses = append(out.statuses, res.Status)
		out.asks = append(out.asks, res.Ask)
	}
	out.run = run
	return out
}

// scriptProblem names why a fake model stopped the run, when it can say.
func scriptProblem(llm workflow.LLM) string {
	if withErr, ok := llm.(interface{ Err() error }); ok && withErr.Err() != nil {
		return "\nfake model: " + withErr.Err().Error()
	}
	return ""
}

func learnScriptSteps() []workflowtest.Step {
	start, turns := learnTopicScript()
	all := append([]string{}, start...)
	for _, turn := range turns {
		all = append(all, turn...)
	}
	return replies(all...)
}

func TestLearnTopicWorkflowFileIsValid(t *testing.T) {
	wf := loadLearnTopic(t)
	names := workflow.NameSet{}
	for _, n := range learnTools {
		names[n] = true
	}
	if findings := workflow.Lint(wf, names); len(findings) != 0 {
		for _, f := range findings {
			t.Errorf("%s:%d %s: %s", learnTopicPath, f.Line, f.Path, f.Message)
		}
	}
	for _, kind := range []string{"quiz", "lesson"} {
		found := false
		for _, st := range wf.States {
			found = found || st.Output == kind
		}
		if !found {
			t.Errorf("no state declares output kind %q", kind)
		}
	}
}

func TestLearnTopicEndToEnd(t *testing.T) {
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	llm := workflowtest.NewScriptedLLM(learnScriptSteps()...)
	out := runLearnTopic(t, llm, prompts)

	if err := llm.Err(); err != nil {
		t.Fatalf("script: %v", err)
	}
	want := []workflow.Status{
		workflow.StatusWaiting, workflow.StatusWaiting, workflow.StatusWaiting, workflow.StatusWaiting, workflow.StatusSucceeded,
	}
	if len(out.statuses) != len(want) {
		t.Fatalf("statuses = %v", out.statuses)
	}
	for i := range want {
		if out.statuses[i] != want[i] {
			t.Errorf("turn %d status = %v, want %v", i, out.statuses[i], want[i])
		}
	}
	if out.asks[0]["text"] == nil || out.asks[1]["question"] != "Q1: write the letter alif" || out.asks[2]["question"] != "Q2: what does kitab mean?" || out.asks[3]["title"] == nil {
		t.Errorf("the questions put to the user: %v", out.asks)
	}

	run := out.run
	wantState := `{"assessment":{"asked":2,"correct":2},"gaps":["verb conjugation"],"goal":"read Arabic news","lessons_done":1,"level":"beginner","mastered":["alphabet","basic nouns"],"program":["Alphabet and sounds","Basic grammar","Reading simple news"],"unit_index":1}`
	if got := stateJSON(t, run); got != wantState {
		t.Errorf("final state:\n got %s\nwant %s", got, wantState)
	}
	if run.StateName != "done" || run.Steps != 13 {
		t.Errorf("state=%s steps=%d", run.StateName, run.Steps)
	}
	for state, visits := range map[string]int{"intake": 1, "plan": 1, "assess": 1, "teach": 1, "done": 1} {
		if run.Visits[state] != visits {
			t.Errorf("visits[%s] = %d, want %d", state, run.Visits[state], visits)
		}
	}
	if n := len(out.tools["rag_search"].Calls()); n != 3 {
		t.Errorf("rag_search calls = %d, want 3 (program, placement test, lesson)", n)
	}
	if calls := out.tools["memory_add"].Calls(); len(calls) != 1 || !strings.Contains(calls[0].Args["text"].(string), "beginner") {
		t.Errorf("memory_add calls = %v", calls)
	}
	if n := len(out.tools["web_search"].Calls()); n != 0 {
		t.Errorf("web_search should not have been needed: %d calls", n)
	}

	recs := out.rec.Records()
	totals := workflow.SumTrace(recs)
	if totals.Attempts != 13 || totals.Retries != 0 || run.Tokens != totals.InputTokens+totals.OutputTokens {
		t.Errorf("totals = %+v, run tokens %d", totals, run.Tokens)
	}
	trace := workflow.FormatTrace(recs)
	if strings.Count(trace, " attempt ") != 13 || !strings.Contains(trace, "tool:   rag_search -> ") {
		t.Errorf("trace:\n%s", trace)
	}
}

func TestLearnTopicEachStepSeesOnlyTheLatestAnswer(t *testing.T) {
	prompts, _ := workflow.DefaultPrompts()
	llm := workflowtest.NewScriptedLLM(learnScriptSteps()...)
	runLearnTopic(t, llm, prompts)

	reqs := llm.Requests()
	// call 8 (index 7) follows "A1 answer" and must not see the first message.
	contains(t, reqs[7].Input, "A1 answer", "latest answer")
	notContains(t, reqs[7].Input, "I want to read the news", "an earlier answer must not be repeated")
	// call 9 (index 8) follows "A2 answer" and must not see "A1 answer".
	contains(t, reqs[8].Input, "A2 answer", "latest answer")
	notContains(t, reqs[8].Input, "A1 answer", "an earlier answer must not be repeated")
	// what must survive is the run state, not the transcript.
	contains(t, reqs[8].Input, `"assessment":{"asked":2,"correct":1}`, "progress is carried in the run state")
	contains(t, reqs[8].Input, `"pending":{"expected":"book","question":"Q2: what does kitab mean?"}`, "the open question is carried in the run state, so the answer can be judged")
	last := reqs[len(reqs)-1].Input
	notContains(t, last, "A2 answer", "an earlier answer must not be repeated")
	contains(t, last, `"lessons_done":1`, "progress is carried in the run state")
}

func TestLearnTopicUsesOutputAndLanguageSnippetsPerState(t *testing.T) {
	prompts, err := workflow.LoadPrompts(fstest.MapFS{
		"step.system.txt":         {Data: []byte("Base prompt.\n{{languageRules}}\n{{outputRules}}")},
		"lang/arb/rules.txt":      {Data: []byte("ARABIC-RULES")},
		"output/quiz/rules.txt":   {Data: []byte("QUIZ-RULES")},
		"output/lesson/rules.txt": {Data: []byte("LESSON-RULES")},
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := workflowtest.NewScriptedLLM(learnScriptSteps()...)
	runLearnTopic(t, llm, prompts)

	stateOf := func(input string) string {
		in, err := workflow.DecodeObject([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		return in["state"].(map[string]any)["name"].(string)
	}
	seen := map[string]bool{}
	for i, req := range llm.Requests() {
		state := stateOf(req.Input)
		seen[state] = true
		contains(t, req.System, "ARABIC-RULES", "language rules in every request")
		switch state {
		case "assess":
			contains(t, req.System, "QUIZ-RULES", "assess produces a quiz")
			notContains(t, req.System, "LESSON-RULES", "assess produces a quiz")
		case "teach":
			contains(t, req.System, "LESSON-RULES", "teach produces a lesson")
			notContains(t, req.System, "QUIZ-RULES", "teach produces a lesson")
		default:
			notContains(t, req.System, "QUIZ-RULES", "state "+state)
			notContains(t, req.System, "LESSON-RULES", "state "+state)
		}
		_ = i
	}
	for _, s := range []string{"intake", "plan", "assess", "teach"} {
		if !seen[s] {
			t.Errorf("the scenario never reached state %s", s)
		}
	}
}

func TestLearnTopicGolden(t *testing.T) {
	golden := filepath.Join("testdata", "learn_topic.golden.json")
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		recorder := &workflowtest.RecordingLLM{Inner: workflowtest.NewScriptedLLM(learnScriptSteps()...)}
		runLearnTopic(t, recorder, prompts)
		if err := recorder.Transcript().Save(golden); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", golden)
		return
	}

	transcript, err := workflowtest.LoadTranscript(golden)
	if err != nil {
		t.Fatalf("%v\n(create it with: UPDATE_GOLDEN=1 go test ./internal/workflow -run TestLearnTopicGolden)", err)
	}
	replay := workflowtest.NewReplayLLM(transcript)
	out := runLearnTopic(t, replay, prompts)
	if err := replay.Err(); err != nil {
		t.Fatalf("%v\n(if the change is intended, regenerate with: UPDATE_GOLDEN=1 go test ./internal/workflow -run TestLearnTopicGolden)", err)
	}
	if out.run.Status != workflow.StatusSucceeded {
		t.Errorf("replayed run status = %v", out.run.Status)
	}
}
