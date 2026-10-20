package ai

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"phraseforge/internal/config"
)

func TestModelsAndKnownTagsAreNoneBeforeSetRouter(t *testing.T) {
	s := &Service{}
	if got := s.Models(); got != nil {
		t.Errorf("Models() = %v, want none", got)
	}
	if got := s.KnownTags(); got != nil {
		t.Errorf("KnownTags() = %v, want none", got)
	}
}

func TestKnownTagsAreDistinctAndSorted(t *testing.T) {
	s := scriptedService(t, twoModels, routertest.NewProvider(nil))
	if got, want := s.KnownTags(), []string{"default", "strong"}; !reflect.DeepEqual(got, want) {
		t.Errorf("KnownTags() = %v, want %v", got, want)
	}
}

func TestModelsListTheFileInOrderWithAvailabilityAndNothingThatLocatesThem(t *testing.T) {
	s := scriptedService(t, twoModels, routertest.NewProvider(nil))
	want := []ModelInfo{
		{ID: "strong", ProviderID: "p", Tags: []string{"default", "strong"}, Priority: 20, Available: true},
		{ID: "fallback", ProviderID: "p", Tags: []string{"default"}, Priority: 10, Available: true},
	}
	if got := s.Models(); !reflect.DeepEqual(got, want) {
		t.Errorf("Models() = %+v, want %+v", got, want)
	}
}

func TestModelsSayWhyAModelIsUnavailableAndNothingAboutTheCredential(t *testing.T) {
	s := scriptedService(t, twoModels, routertest.NewProvider(nil),
		router.WithAvailability(func(modelcfg.Provider) string { return router.ReasonMissingCredentials }))
	for _, m := range s.Models() {
		if m.Available || m.UnavailableReason != "missing_credentials" {
			t.Errorf("model = %+v", m)
		}
	}
	raw, err := json.Marshal(s.Models())
	if err != nil || !strings.Contains(string(raw), `"unavailable_reason":"missing_credentials"`) {
		t.Errorf("json = %s (%v)", raw, err)
	}
}

// tagModels has models of different sizes: "big" and "small" are Ollama models
// with a 32768 and a 2048 token window, "hosted" is on an OpenAI-compatible
// provider, which takes no num_ctx.
const tagModels = `
providers:
  - {id: p, type: ollama, base_url: "http://127.0.0.1:1", api_key_from: ""}
  - {id: h, type: openai, base_url: "https://hosted.example/v1", api_key_from: ""}
models:
  - {id: big, provider: p, model: big-name, tags: [default, strong], priority: 20, options: {num_ctx: 32768}}
  - {id: fallback, provider: p, model: fallback-name, tags: [default], priority: 10, options: {num_ctx: 32768}}
  - {id: other, provider: p, model: other-name, tags: [vision], priority: 5, options: {num_ctx: 32768}}
  - {id: small, provider: p, model: small-name, tags: [tiny], priority: 5, options: {num_ctx: 2048}}
  - {id: unsized, provider: p, model: unsized-name, tags: [unsized], priority: 5}
  - {id: hosted, provider: h, model: hosted-name, tags: [hosted, tiny-hosted], priority: 5}
`

// tagService is a Service over tagModels with the same fake behind every
// provider type.
func tagService(t *testing.T) *Service {
	t.Helper()
	fake := routertest.NewProvider(nil)
	models := mustModels(t, tagModels)
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": fake, "openai": fake})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.SetRouter(r, models)
	return s
}

func TestValidateModelTags(t *testing.T) {
	s := tagService(t)
	cases := []struct {
		name    string
		tags    []string
		wantErr []string // substrings; nil means valid
	}{
		{"inherit", nil, nil},
		{"empty means inherit", []string{}, nil},
		{"one known tag", []string{"strong"}, nil},
		{"tags one model has together", []string{"default", "strong"}, nil},
		{"unknown tag", []string{"default", "ghost"}, []string{"ghost"}},
		{"several unknown tags", []string{"ghost", "phantom"}, []string{"ghost", "phantom"}},
		{"blank tag", []string{"default", " "}, []string{"blank"}},
		{"tags no single model has", []string{"strong", "vision"}, []string{"strong", "vision", "together"}},
		{"a hosted model takes no num_ctx", []string{"hosted"}, nil},
		// translation needs the default 4096 token window.
		{"an Ollama model whose window is too small", []string{"tiny"}, []string{"kind translation", "model small", "num_ctx is 2048", "4096", "[tiny]"}},
		{"an Ollama model without num_ctx", []string{"unsized"}, []string{"kind translation", "model unsized", "num_ctx is not set"}},
	}
	for _, tc := range cases {
		err := s.ValidateModelTags("translation", tc.tags)
		if tc.wantErr == nil {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		for _, want := range tc.wantErr {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q lacks %q", tc.name, err, want)
			}
		}
	}
}

// The window a tag override must fit is the one of the kind's own purpose.
func TestValidateModelTagsChecksTheNumCtxOfTheKindsPurpose(t *testing.T) {
	build := func(numCtx int) *Service {
		s := tagService(t)
		s.cfg = config.Config{Title: config.PurposeConfig{NumCtx: numCtx}}
		return s
	}
	if err := build(2048).ValidateModelTags("title", []string{"tiny"}); err != nil {
		t.Errorf("a purpose that needs 2048 fits the 2048 model: %v", err)
	}
	err := build(8192).ValidateModelTags("title", []string{"tiny"})
	if err == nil || !strings.Contains(err.Error(), "kind title") || !strings.Contains(err.Error(), "model small") || !strings.Contains(err.Error(), "below the 8192") {
		t.Errorf("err = %v, want the kind, the model and the window it needs", err)
	}
}

func TestValidateModelTagsWithoutModelsRejectsAnyTag(t *testing.T) {
	if err := (&Service{}).ValidateModelTags("translation", []string{"default"}); err == nil {
		t.Error("a tag was accepted with no models configured")
	}
}

func TestDefaultModelTagsAreTheKindsPurposeTags(t *testing.T) {
	s := &Service{cfg: config.Config{Title: config.PurposeConfig{ModelTags: []string{"fast"}}}}
	got := s.DefaultModelTags()
	if !reflect.DeepEqual(got["title"], []string{"fast"}) {
		t.Errorf("title = %v", got["title"])
	}
	if len(got) != len(ValidKinds) {
		t.Errorf("%d kinds, want %d", len(got), len(ValidKinds))
	}
}

// TestPromptTagsRoundTripAndOverride: a row's tags come back from ListPrompts,
// replace the purpose's tags in prompt(), and a row without tags inherits them.
func TestPromptTagsRoundTripAndOverride(t *testing.T) {
	pool := newAIDB(t)
	ctx := context.Background()
	s := New(pool, config.Config{Title: config.PurposeConfig{ModelTags: []string{"default"}}}, nil, nil, nil, nil)
	models := mustModels(t, twoModels)
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": routertest.NewProvider(nil)})
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouter(r, models)

	if err := s.SetPrompt(ctx, Prompt{Kind: "title", SourceLanguage: "deu", TargetLanguage: "title", ModelTags: []string{"default", "strong"}, Think: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrompt(ctx, Prompt{Kind: "title", SourceLanguage: "pol", TargetLanguage: "title", ModelTags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrompt(ctx, Prompt{Kind: "title", SourceLanguage: "fra", TargetLanguage: "title"}); err != nil {
		t.Fatal(err)
	}

	listed, err := s.ListPrompts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]Prompt{}
	for _, p := range listed {
		bySource[p.SourceLanguage] = p
	}
	if got := bySource["deu"].ModelTags; !reflect.DeepEqual(got, []string{"default", "strong"}) {
		t.Errorf("deu tags = %v", got)
	}
	for _, source := range []string{"pol", "fra"} {
		if got := bySource[source].ModelTags; got != nil {
			t.Errorf("%s tags = %v, want nil (inherit)", source, got)
		}
	}

	if got := s.prompt(ctx, "title", "deu", "title"); !reflect.DeepEqual(got.ModelTags, []string{"default", "strong"}) || !got.Think {
		t.Errorf("deu prompt = tags %v think %v, want the row's tags and think", got.ModelTags, got.Think)
	}
	for _, source := range []string{"pol", "fra", "ita"} {
		if got := s.prompt(ctx, "title", source, "title"); !reflect.DeepEqual(got.ModelTags, []string{"default"}) {
			t.Errorf("%s prompt tags = %v, want the purpose's [default]", source, got.ModelTags)
		}
	}

	if err := s.SetPrompt(ctx, Prompt{Kind: "title", SourceLanguage: "deu", TargetLanguage: "title"}); err != nil {
		t.Fatal(err)
	}
	if got := s.prompt(ctx, "title", "deu", "title"); !reflect.DeepEqual(got.ModelTags, []string{"default"}) {
		t.Errorf("after clearing the tags: %v, want the purpose's", got.ModelTags)
	}
}

// TestPromptTagsCheckRejectsBlankAndEmptyLists: the database refuses what the
// API validation would, so no path can store a list that matches no model.
func TestPromptTagsCheckRejectsBlankAndEmptyLists(t *testing.T) {
	pool := newAIDB(t)
	for _, tags := range []string{"'{}'", `'{""}'`, `'{"a",""}'`, `'{"a",NULL}'`} {
		_, err := pool.Exec(context.Background(), `INSERT INTO llm_prompts(kind, source_language, target_language, model_tags) VALUES('title','deu','title',`+tags+`)`)
		if err == nil {
			t.Errorf("model_tags %s was stored", tags)
		}
	}
}
