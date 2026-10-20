package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"k8s-lab/shared/llm"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"phraseforge/internal/config"
)

func TestContextOverflowDetection(t *testing.T) {
	tests := []struct {
		name             string
		prompt, reply, n int
		overflow         bool
	}{
		{"healthy call", 2217, 1653, 8192, false},
		{"input cut to the window (spike: 1027 + 1 at 1024)", 1027, 1, 1024, true},
		{"reply shifted the context (spike: 616 + 958 at 1024)", 616, 958, 1024, true},
		{"exactly full", 600, 424, 1024, true},
		{"one token to spare", 600, 423, 1024, false},
		{"window unknown", 5000, 5000, 0, false},
		{"no counts reported", 0, 0, 1024, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := contextOverflow(router.Response{InputTokens: tt.prompt, OutputTokens: tt.reply}, tt.n)
			var overflow *ContextOverflowError
			if got := errors.As(err, &overflow); got != tt.overflow {
				t.Fatalf("overflow = %v (%v), want %v", got, err, tt.overflow)
			}
		})
	}
}

// echo is a model that repeats its input, and overflows above limit runes.
func echo(limit int, calls *[]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, chunk string) (string, error) {
		*calls = append(*calls, chunk)
		if utf8.RuneCountInString(chunk) > limit {
			return "", &ContextOverflowError{PromptTokens: 90, ReplyTokens: 20, NumCtx: 100}
		}
		return chunk, nil
	}
}

func TestGenerateWithSplitFittingChunkIsOneCallUntouched(t *testing.T) {
	var calls []string
	got, err := generateWithSplit(context.Background(), "  as is \n", false, echo(100, &calls))
	if err != nil || got != "  as is \n" || len(calls) != 1 {
		t.Fatalf("got (%q, %v) after %d calls, want the reply untouched after 1", got, err, len(calls))
	}
}

// The point of reassembly: whatever the sizes involved, text that comes back
// through a split-and-rejoin is the original, paragraphs and all.
func TestGenerateWithSplitReassemblesTheOriginalText(t *testing.T) {
	paragraphs := []string{
		"First paragraph has two sentences. Here is the second one.",
		"Second paragraph: a single, slightly longer sentence than the first one was.",
		"Third.\nWith a line break inside it.",
		"Fourth paragraph closes the text with a final remark that is fairly long.",
	}
	text := strings.Join(paragraphs, "\n\n")
	for _, limit := range []int{len(text) - 1, 120, 80, 50, 30} {
		var calls []string
		got, err := generateWithSplit(context.Background(), text, false, echo(limit, &calls))
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if got != text {
			t.Errorf("limit %d: reassembled\n%q\nwant\n%q", limit, got, text)
		}
		for _, c := range calls {
			if utf8.RuneCountInString(c) > limit && len(calls) == 1 {
				t.Errorf("limit %d: chunk of %d runes was accepted", limit, utf8.RuneCountInString(c))
			}
		}
	}
}

func TestGenerateWithSplitSplitsBetweenParagraphsWhenItCan(t *testing.T) {
	var calls []string
	text := "alpha alpha alpha.\n\nbeta beta beta.\n\ngamma gamma gamma.\n\ndelta delta delta."
	if _, err := generateWithSplit(context.Background(), text, false, echo(45, &calls)); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls[1:] {
		if strings.Contains(c, "\n") && strings.HasPrefix(c, "\n") || strings.HasSuffix(c, ".\n") {
			t.Errorf("a piece %q was cut away from a paragraph break", c)
		}
		if !strings.HasSuffix(c, ".") {
			t.Errorf("piece %q does not end at a paragraph end", c)
		}
	}
}

func TestGenerateWithSplitJoinLinesKeepsEveryItemOnItsOwnLine(t *testing.T) {
	var calls []string
	got, err := generateWithSplit(context.Background(), "aaa aaa. bbb bbb. ccc ccc. ddd ddd.", true, echo(20, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if want := "aaa aaa. bbb bbb.\nccc ccc. ddd ddd."; got != want {
		t.Errorf("got %q, want %q: the replies must be joined by a newline", got, want)
	}
}

func TestGenerateWithSplitGivesUpAfterTheMaxDepth(t *testing.T) {
	var calls []string
	always := func(_ context.Context, chunk string) (string, error) {
		calls = append(calls, chunk)
		return "", &ContextOverflowError{PromptTokens: 90, ReplyTokens: 20, NumCtx: 100}
	}
	_, err := generateWithSplit(context.Background(), strings.Repeat("word ", 400), false, always)
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) || !strings.Contains(err.Error(), "after splitting") {
		t.Fatalf("err = %v, want the overflow after splitting", err)
	}
	if len(calls) != 1+maxSplitDepth { // the failing first half is retried no further
		t.Errorf("made %d calls, want %d (one per level down the first half)", len(calls), 1+maxSplitDepth)
	}
}

func TestGenerateWithSplitOtherErrorsPassThrough(t *testing.T) {
	boom := errors.New("boom")
	_, err := generateWithSplit(context.Background(), "text", false, func(context.Context, string) (string, error) { return "", boom })
	if err != boom {
		t.Fatalf("err = %v, want it unchanged", err)
	}
}

// callLLM turns the token counts Ollama reports into the overflow error, and
// discards the reply it cannot trust.
func TestCallLLMDetectsContextOverflow(t *testing.T) {
	serve := func(prompt, reply int) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, `{"message":{"role":"assistant","content":"reply"},"done":false}`)
			fmt.Fprintf(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":%d,"eval_count":%d}`+"\n", prompt, reply)
		}))
		t.Cleanup(server.Close)
		return server
	}
	call := func(url string, numCtx int) (string, error) {
		s := correctionService(t, url)
		return s.callLLM(context.Background(), "translation", Prompt{}, config.PurposeConfig{NumCtx: numCtx},
			[]llm.Message{{Role: "user", Content: "hi"}}, nil, 1, 1)
	}

	if out, err := call(serve(300, 200).URL, 1000); err != nil || out != "reply" {
		t.Fatalf("healthy call: (%q, %v), want the reply", out, err)
	}
	out, err := call(serve(600, 500).URL, 1000)
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) || out != "" || overflow.NumCtx != 1000 {
		t.Fatalf("overflowing call: (%q, %v), want an empty reply and a ContextOverflowError", out, err)
	}
	if out, err := call(serve(600, 500).URL, 0); err != nil || out != "reply" {
		t.Fatalf("unknown window: (%q, %v), want the reply accepted", out, err)
	}
}

// providersOfEveryType has one model on each kind of provider; the same scripted
// provider stands in for all of them.
const providersOfEveryType = `
providers:
  - {id: local, type: ollama, base_url: "http://127.0.0.1:1", api_key_from: ""}
  - {id: hosted, type: openai, base_url: "https://hosted.example/v1", api_key_from: ""}
  - {id: claude, type: anthropic, api_key_from: "env:OVERFLOW_TEST_KEY"}
  - {id: gemini, type: vertex, project: p, location: us-central1, api_key_from: "env:OVERFLOW_TEST_KEY"}
models:
  - {id: ollama-model, provider: local, model: m, tags: [ollama], priority: 1}
  - {id: openai-model, provider: hosted, model: m, tags: [openai], priority: 1}
  - {id: anthropic-model, provider: claude, model: m, tags: [anthropic], priority: 1}
  - {id: vertex-model, provider: gemini, model: m, tags: [vertex], priority: 1}
`

func serviceOverEveryProviderType(t *testing.T, fake *routertest.Provider) *Service {
	t.Helper()
	models := mustModels(t, providersOfEveryType)
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": fake, "openai": fake, "anthropic": fake, "vertex": fake})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.SetRouter(r, models)
	return s
}

// The same token counts, at the same numCtx, overflow for an Ollama answer and
// for no other provider's.
func TestOnlyAnOllamaAnswerCanOverflowTheContextWindow(t *testing.T) {
	const numCtx = 4096
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"ollama-model":    {routertest.OK("reply", 3000, 1096)},
		"openai-model":    {routertest.OK("reply", 3000, 1096)},
		"anthropic-model": {routertest.OK("reply", 3000, 1096)},
		"vertex-model":    {routertest.OK("reply", 3000, 1096)},
	})
	s := serviceOverEveryProviderType(t, fake)
	for _, tc := range []struct {
		tag      string
		overflow bool
	}{{"ollama", true}, {"openai", false}, {"anthropic", false}, {"vertex", false}} {
		out, err := s.callLLM(context.Background(), "translation", Prompt{ModelTags: []string{tc.tag}}, config.PurposeConfig{NumCtx: numCtx}, userHi, nil, 1, 1)
		var overflow *ContextOverflowError
		if got := errors.As(err, &overflow); got != tc.overflow {
			t.Errorf("%s: overflow = %v (%v), want %v", tc.tag, got, err, tc.overflow)
		}
		if !tc.overflow && (err != nil || out != "reply") {
			t.Errorf("%s: got (%q, %v), want the reply accepted", tc.tag, out, err)
		}
	}
}

func TestAHostedAnswerOverTheNumCtxIsNotSplit(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{"openai-model": {routertest.OK("reply", 3000, 1096)}})
	s := serviceOverEveryProviderType(t, fake)
	chunk := strings.Repeat("word ", 400)
	out, err := generateWithSplit(context.Background(), chunk, false, func(ctx context.Context, piece string) (string, error) {
		return s.callLLM(ctx, "translation", Prompt{ModelTags: []string{"openai"}}, config.PurposeConfig{NumCtx: 4096}, userHi, nil, 1, 1)
	})
	if err != nil || out != "reply" {
		t.Fatalf("got (%q, %v)", out, err)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("provider calls = %d, want 1: a hosted answer must not be halved and repeated", n)
	}
}

func TestIsOllamaModelIsFalseForAnUnknownIdAndWithoutModels(t *testing.T) {
	s := serviceOverEveryProviderType(t, routertest.NewProvider(nil))
	for id, want := range map[string]bool{"ollama-model": true, "openai-model": false, "vertex-model": false, "": false, "ghost": false} {
		if got := s.isOllamaModel(id); got != want {
			t.Errorf("isOllamaModel(%q) = %v, want %v", id, got, want)
		}
	}
	if (&Service{}).isOllamaModel("ollama-model") {
		t.Error("without models nothing is an Ollama model")
	}
	if err := s.overflowOf(router.Response{ModelID: "ghost", InputTokens: 9000, OutputTokens: 9000}, 4096); err != nil {
		t.Errorf("an unknown model id is never an overflow: %v", err)
	}
	if err := s.overflowOf(router.Response{ModelID: "ollama-model", InputTokens: 4096}, 4096); err == nil {
		t.Error("an Ollama answer at numCtx overflows")
	}
}

func TestAnOllamaAnswerAtNumCtxStillSplitsTheChunk(t *testing.T) {
	// The first call overflows, the two halves fit.
	fake := routertest.NewProvider(map[string][]routertest.Result{"ollama-model": {
		routertest.OK("whole", 3000, 1096), routertest.OK("first", 100, 50), routertest.OK("second", 100, 50),
	}})
	s := serviceOverEveryProviderType(t, fake)
	chunk := strings.Repeat("word ", 40) + "\n\n" + strings.Repeat("more ", 40)
	out, err := generateWithSplit(context.Background(), chunk, false, func(ctx context.Context, piece string) (string, error) {
		return s.callLLM(ctx, "translation", Prompt{ModelTags: []string{"ollama"}}, config.PurposeConfig{NumCtx: 4096}, userHi, nil, 1, 1)
	})
	if err != nil || out != "first\n\nsecond" {
		t.Fatalf("got (%q, %v)", out, err)
	}
	if n := len(fake.Calls()); n != 3 {
		t.Errorf("provider calls = %d, want 3 (whole, then two halves)", n)
	}
}
