package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"k8s-lab/shared/llm"
)

// The text and dialog reviews are two purposes, each with its own model,
// prompt and reply schema: one reviews the transcription, one the translations.
// Each has its own model tags.
const (
	KindReviewTranscription = "review_transcription"
	KindReviewTranslation   = "review_translation"
)

// KindReviewTextJob is the job kind of a text or dialog review; the payload's
// part says which one.
const KindReviewTextJob = "review_text"

// The parts of a text or dialog that are reviewed separately, each with its own
// job, call and stored proposals.
const (
	ReviewPartTranscription = "transcription"
	ReviewPartTranslation   = "translation"
)

// IsReviewPart reports whether part is one of the reviewable parts.
func IsReviewPart(part string) bool {
	return part == ReviewPartTranscription || part == ReviewPartTranslation
}

// reviewKindOf is the purpose that reviews part.
func reviewKindOf(part string) string {
	if part == ReviewPartTranscription {
		return KindReviewTranscription
	}
	return KindReviewTranslation
}

// maxTextReviewChars bounds what one review call is given (source and the
// reviewed part together, in characters): a longer resource fails the job with a
// clear message instead of overflowing the model.
const maxTextReviewChars = 12000

// ReviewTranscriptionSchema is the reply schema of the transcription review: one
// {ok, suggestion, reason} check; a suggestion is the whole corrected
// transcription.
var ReviewTranscriptionSchema = json.RawMessage(`{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}`)

// ReviewTranslationSchema is the reply schema of the translation review: one
// check per locale; a suggestion is the whole corrected translation.
var ReviewTranslationSchema = json.RawMessage(`{"type":"object","required":["translations"],"properties":{"translations":{"type":"array","items":{"type":"object","required":["locale","ok","suggestion","reason"],"properties":{"locale":{"type":"string"},"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}}}}`)

// TextReviewVocabulary is one phrase of the vocabulary attached to a text, with
// its non-blank translations by locale.
type TextReviewVocabulary struct {
	Phrase       string
	Translations map[string]string
}

// TextReviewInput is what one text review call is given: a text or dialog as
// stored, with the vocabulary list generated from it. Version identifies the
// source text the review is about (a hash the store computes); a proposal is
// only applied while the resource still has it.
type TextReviewInput struct {
	Language      string
	Body          string
	Transcription string
	Translations  []ReviewTranslation // Translation holds the whole translated text
	Vocabulary    []TextReviewVocabulary
	Version       string
}

// textReviewPromptVars fills the text review prompt template.
type textReviewPromptVars struct {
	SourceLanguage, Text, Transcription, Translations, Vocabulary, TranscriptionPrompt string
}

func renderTextReviewPrompt(template string, v textReviewPromptVars) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", v.SourceLanguage,
		"{{text}}", v.Text,
		"{{transcription}}", v.Transcription,
		"{{translations}}", v.Translations,
		"{{vocabulary}}", v.Vocabulary,
		"{{transcriptionPrompt}}", v.TranscriptionPrompt,
	).Replace(template)
}

// formatTextTranslations lays the translations out as "[locale]" lines each
// followed by the text, English first (the reference), then locale-code order.
func formatTextTranslations(translations []ReviewTranslation) string {
	sorted := slices.Clone(translations)
	slices.SortStableFunc(sorted, func(a, b ReviewTranslation) int {
		order := func(l string) string {
			if l == "en" {
				return ""
			}
			return l
		}
		return strings.Compare(order(a.Locale), order(b.Locale))
	})
	blocks := make([]string, len(sorted))
	for i, t := range sorted {
		blocks[i] = "[" + t.Locale + "]\n" + t.Translation
	}
	return orNone(strings.Join(blocks, "\n\n"))
}

// formatTextVocabulary lists the attached vocabulary, one phrase per line with
// its translations ("phrase = en: x | pl: y"), none when the list is empty.
func formatTextVocabulary(entries []TextReviewVocabulary) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		locales := make([]string, 0, len(e.Translations))
		for l := range e.Translations {
			locales = append(locales, l)
		}
		slices.Sort(locales)
		parts := make([]string, len(locales))
		for j, l := range locales {
			parts[j] = l + ": " + e.Translations[l]
		}
		lines[i] = "- " + e.Phrase
		if len(parts) > 0 {
			lines[i] += " = " + strings.Join(parts, " | ")
		}
	}
	return orNone(strings.Join(lines, "\n"))
}

// parseTextReviewReply reads the reply of the review of part into findings. The
// transcription reply is one check (a finding only when the resource has a
// transcription and its language has a transcription snippet); the translation
// reply has a "translations" array with one check per locale. A reply that is
// not the expected JSON object is an error (the call is corrected); a malformed
// entry, an unknown locale, and a check without a fix or a reason are ignored.
func parseTextReviewReply(raw, part string, in TextReviewInput, sections LanguageSections) ([]ReviewFinding, error) {
	body := []byte(strings.TrimSpace(raw))
	if part == ReviewPartTranscription {
		var reply struct {
			OK *bool `json:"ok"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
		}
		if reply.OK == nil {
			return nil, fmt.Errorf(`the reply has no boolean "ok"`)
		}
		var findings []ReviewFinding
		if hasSnippet(sections.TranscriptionPrompt) && strings.TrimSpace(in.Transcription) != "" {
			entry, _ := decodeReviewEntry(body)
			if f, ok := reviewField(ReviewFieldTranscription, entry, in.Transcription, false); ok {
				findings = append(findings, f)
			}
		}
		return findings, nil
	}
	var reply struct {
		Translations *[]json.RawMessage `json:"translations"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
	}
	if reply.Translations == nil {
		return nil, fmt.Errorf(`the reply has no "translations" array`)
	}
	return localeFindings(*reply.Translations, ReviewFieldTranslation, in.Translations,
		func(t ReviewTranslation) string { return t.Translation }, false), nil
}

// ReviewResource asks the model, in one structured call, to review one part of
// in (the transcription or the translations) and returns its findings; the
// other part is not sent. The call is corrected within the purpose's attempt
// budget. A resource whose source and reviewed part together exceed
// maxTextReviewChars is reviewed in paragraph chunks instead (see
// reviewInChunks), one call each, and the chunk findings are merged into the
// same one-finding-per-field shape.
func (s *Service) ReviewResource(ctx context.Context, in TextReviewInput, part string) (ReviewResult, error) {
	if !IsReviewPart(part) {
		return ReviewResult{}, fmt.Errorf("review: unknown part %q", part)
	}
	if part == ReviewPartTranscription {
		in.Translations = nil
	} else {
		in.Transcription = ""
	}
	if textReviewSize(in) > maxTextReviewChars {
		return s.reviewInChunks(ctx, in, part)
	}
	return s.reviewOnce(ctx, in, part)
}

// textReviewSize is what one call would carry of in, in characters: the source
// and the part that is reviewed (the other one is already cleared).
func textReviewSize(in TextReviewInput) int {
	size := utf8.RuneCountInString(in.Body) + utf8.RuneCountInString(in.Transcription)
	for _, t := range in.Translations {
		size += utf8.RuneCountInString(t.Translation)
	}
	return size
}

// reviewOnce is one review call of in, which must fit maxTextReviewChars.
func (s *Service) reviewOnce(ctx context.Context, in TextReviewInput, part string) (ReviewResult, error) {
	name, err := s.languageName(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, err
	}
	sections, err := s.EffectiveSections(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load language sections for %s: %w", in.Language, err)
	}
	kind := reviewKindOf(part)
	schema := ReviewTranslationSchema
	if part == ReviewPartTranscription {
		schema = ReviewTranscriptionSchema
	}
	def := s.purposeDefault(kind)
	prompt := s.prompt(ctx, kind, in.Language, kind)
	text := renderTextReviewPrompt(prompt.Prompt, textReviewPromptVars{
		SourceLanguage: name, Text: in.Body, Transcription: orNone(in.Transcription),
		Translations: formatTextTranslations(in.Translations), Vocabulary: formatTextVocabulary(in.Vocabulary),
		TranscriptionPrompt: sections.TranscriptionPrompt,
	})
	msgs := []llm.Message{{Role: "user", Content: text}}
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	raw, err := runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, kind, prompt, def, m, schema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := parseTextReviewReply(raw, part, in, sections)
			if err != nil {
				slog.Info("llm reply rejected", "kind", kind, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
	if err != nil {
		return ReviewResult{}, err
	}
	findings, err := parseTextReviewReply(raw, part, in, sections)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Findings: findings, Response: json.RawMessage(strings.TrimSpace(raw))}, nil
}

// TextReviewJobPayload identifies the text or dialog to review.
type TextReviewJobPayload struct {
	ResourceType string `json:"resource_type"` // "text" or "dialog"
	ResourceID   int64  `json:"resource_id"`
	Part         string `json:"part"` // ReviewPartTranscription or ReviewPartTranslation
}

// TextReviewStore is the consumer-side interface of what HandleReviewResource
// reads and writes; both methods return ErrItemGone when the resource is gone
// and SaveTextReview ErrItemChanged when its source text is no longer the one
// that was reviewed (version).
type TextReviewStore interface {
	TextReviewSource(ctx context.Context, resourceType string, id int64) (TextReviewInput, error)
	SaveTextReview(ctx context.Context, resourceType string, id int64, part, version string, findings, response json.RawMessage) error
}

// SetTextReviewStore sets the store the text review job uses.
func (s *Service) SetTextReviewStore(r TextReviewStore) { s.textReviews = r }

// NewTextReviewJob builds the job reviewing one part of the text or dialog id.
func NewTextReviewJob(resourceType string, id int64, part string) (ItemJob, error) {
	if !IsReviewPart(part) {
		return ItemJob{}, fmt.Errorf("review job: unknown part %q", part)
	}
	if resourceType != "text" && resourceType != "dialog" {
		return ItemJob{}, fmt.Errorf("review job: unknown resource_type %q", resourceType)
	}
	if id == 0 {
		return ItemJob{}, fmt.Errorf("review job: resource_id is required")
	}
	payload, err := json.Marshal(TextReviewJobPayload{ResourceType: resourceType, ResourceID: id, Part: part})
	if err != nil {
		return ItemJob{}, err
	}
	return ItemJob{Kind: KindReviewTextJob, Payload: payload}, nil
}

// HandleReviewResource is the jobs.HandlerFunc of KindReviewTextJob: it reads
// the resource, makes one review call and stores the findings as its review
// (an empty one too). A resource deleted or edited since the job was queued, or
// while the model worked, ends the job as skipped.
func (s *Service) HandleReviewResource(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p TextReviewJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if _, err := NewTextReviewJob(p.ResourceType, p.ResourceID, p.Part); err != nil {
		return nil, err
	}
	if s.textReviews == nil {
		return nil, fmt.Errorf("review job: no text review store configured")
	}
	in, err := s.textReviews.TextReviewSource(ctx, p.ResourceType, p.ResourceID)
	if err != nil {
		return nil, err
	}
	result, err := s.ReviewResource(ctx, in, p.Part)
	if err != nil {
		return nil, err
	}
	findings := result.Findings
	if findings == nil {
		findings = []ReviewFinding{}
	}
	for i := range findings {
		findings[i].Status = ReviewOpen
	}
	stored, err := json.Marshal(findings)
	if err != nil {
		return nil, err
	}
	if err := s.textReviews.SaveTextReview(ctx, p.ResourceType, p.ResourceID, p.Part, in.Version, stored, result.Response); err != nil {
		return nil, err
	}
	return json.Marshal(reviewJobResult{Response: json.RawMessage(strings.TrimSpace(string(result.Response))), Findings: len(findings)})
}

// paragraphRun is a text cut into blank-line separated paragraphs, with the
// separator that stood after each one (see splitParagraphs).
type paragraphRun struct {
	parts, seps []string
}

func newParagraphRun(text string) paragraphRun {
	parts, seps := splitParagraphs(strings.ReplaceAll(text, "\r\n", "\n"))
	return paragraphRun{parts: parts, seps: seps}
}

// join is paragraphs from up to (not including) to, as they stood in the text.
func (r paragraphRun) join(from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		if i > from {
			b.WriteString(r.seps[i-1])
		}
		b.WriteString(r.parts[i])
	}
	return b.String()
}

// textChunk is the paragraphs [from, to) of a long resource.
type textChunk struct{ from, to int }

// planTextChunks groups paragraph indexes into chunks of at most limit
// characters, where cost(i) is what paragraph i adds to a call. It fails on a
// single paragraph that alone exceeds limit, naming it (1-based).
func planTextChunks(count, limit int, cost func(i int) int) ([]textChunk, error) {
	var chunks []textChunk
	start, used := 0, 0
	for i := 0; i < count; i++ {
		c := cost(i)
		if c > limit {
			return nil, fmt.Errorf("paragraph %d alone is %d characters, the limit is %d", i+1, c, limit)
		}
		if used+c > limit {
			chunks = append(chunks, textChunk{start, i})
			start, used = i, 0
		}
		used += c
	}
	return append(chunks, textChunk{start, count}), nil
}

// reviewInChunks reviews a resource too long for one call: the source and the
// reviewed part (each locale's translation) are cut into paragraphs, which must
// match one to one, packed into chunks that fit, reviewed one call per chunk,
// and the findings merged back into whole-text findings (see
// mergeChunkFindings). A resource that cannot be cut this way fails with the
// reason.
func (s *Service) reviewInChunks(ctx context.Context, in TextReviewInput, part string) (ReviewResult, error) {
	tooLong := func(why string) error {
		return fmt.Errorf("too long to review in one call (%d characters, the limit is %d) and cannot be split: %s", textReviewSize(in), maxTextReviewChars, why)
	}
	body := newParagraphRun(in.Body)
	type reviewed struct {
		name string
		run  paragraphRun
	}
	var parts []reviewed
	if part == ReviewPartTranscription {
		parts = append(parts, reviewed{"transcription", newParagraphRun(in.Transcription)})
	} else {
		for _, t := range in.Translations {
			parts = append(parts, reviewed{"translation [" + t.Locale + "]", newParagraphRun(t.Translation)})
		}
	}
	for _, p := range parts {
		if len(p.run.parts) != len(body.parts) {
			return ReviewResult{}, tooLong(fmt.Sprintf("the text has %d paragraphs but the %s has %d; they must match", len(body.parts), p.name, len(p.run.parts)))
		}
	}
	chunks, err := planTextChunks(len(body.parts), maxTextReviewChars, func(i int) int {
		n := utf8.RuneCountInString(body.parts[i])
		for _, p := range parts {
			n += utf8.RuneCountInString(p.run.parts[i])
		}
		return n
	})
	if err != nil {
		return ReviewResult{}, tooLong(err.Error())
	}

	results := make([]ReviewResult, len(chunks))
	for i, c := range chunks {
		if err := ctx.Err(); err != nil {
			return ReviewResult{}, err
		}
		sub := in
		sub.Body = body.join(c.from, c.to)
		if part == ReviewPartTranscription {
			sub.Transcription = parts[0].run.join(c.from, c.to)
		} else {
			sub.Translations = make([]ReviewTranslation, len(in.Translations))
			for k, t := range in.Translations {
				sub.Translations[k] = ReviewTranslation{Locale: t.Locale, Translation: parts[k].run.join(c.from, c.to)}
			}
		}
		if results[i], err = s.reviewOnce(ctx, sub, part); err != nil {
			return ReviewResult{}, fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
		}
	}

	// The separator that stood between chunk i and chunk i+1 in the source.
	chunkSeps := make([]string, len(chunks)-1)
	for i := range chunkSeps {
		chunkSeps[i] = body.seps[chunks[i].to-1]
	}
	var findings []ReviewFinding
	merge := func(field, locale, current string, run paragraphRun) {
		texts := make([]string, len(chunks))
		perChunk := make([][]ReviewFinding, len(chunks))
		for i, c := range chunks {
			texts[i], perChunk[i] = run.join(c.from, c.to), results[i].Findings
		}
		if f, ok := mergeChunkFindings(field, locale, current, texts, chunkSeps, perChunk); ok {
			findings = append(findings, f)
		}
	}
	if part == ReviewPartTranscription {
		if hasSnippet(in.Transcription) {
			merge(ReviewFieldTranscription, "", in.Transcription, parts[0].run)
		}
	} else {
		for k, t := range in.Translations {
			if strings.TrimSpace(t.Translation) != "" {
				merge(ReviewFieldTranslation, t.Locale, t.Translation, parts[k].run)
			}
		}
	}
	responses := make([]json.RawMessage, len(results))
	for i, r := range results {
		responses[i] = r.Response
	}
	response, err := json.Marshal(responses)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Findings: findings, Response: response}, nil
}

// mergeChunkFindings folds the findings the chunks of one field (and locale)
// got into one finding about the whole text. chunkTexts are the chunks as
// stored and chunkSeps what stood between them. A chunk that got a
// replacement contributes its suggestion, any other chunk its stored text; the
// finding is a replacement if any chunk got one, else a comment if any chunk
// has a reason. Reasons are labelled with their chunk when there are several.
func mergeChunkFindings(field, locale, current string, chunkTexts, chunkSeps []string, perChunk [][]ReviewFinding) (ReviewFinding, bool) {
	var merged strings.Builder
	var reasons []string
	replaced := false
	for i, text := range chunkTexts {
		if i > 0 {
			merged.WriteString(chunkSeps[i-1])
		}
		idx := slices.IndexFunc(perChunk[i], func(f ReviewFinding) bool { return f.Field == field && f.Locale == locale })
		if idx < 0 {
			merged.WriteString(text)
			continue
		}
		f := perChunk[i][idx]
		if f.Action == ReviewReplace && f.Suggestion != "" {
			replaced = true
			text = f.Suggestion
		}
		merged.WriteString(text)
		if f.Reason != "" {
			reasons = append(reasons, fmt.Sprintf("Part %d of %d: %s", i+1, len(chunkTexts), f.Reason))
		}
	}
	finding := ReviewFinding{Field: field, Locale: locale, Current: current, Reason: strings.Join(reasons, "\n")}
	switch {
	case replaced:
		finding.Action, finding.Suggestion = ReviewReplace, merged.String()
	case len(reasons) > 0:
		finding.Action = ReviewComment
	default:
		return ReviewFinding{}, false
	}
	return finding, true
}
