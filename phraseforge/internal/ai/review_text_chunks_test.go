package ai

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanTextChunksPacksParagraphsAndNamesAnOversizedOne(t *testing.T) {
	sizes := []int{5, 5, 6, 3, 3, 3}
	got, err := planTextChunks(len(sizes), 10, func(i int) int { return sizes[i] })
	want := []textChunk{{0, 2}, {2, 4}, {4, 6}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
	if _, err := planTextChunks(2, 10, func(i int) int { return []int{4, 11}[i] }); err == nil || !strings.Contains(err.Error(), "paragraph 2") {
		t.Errorf("oversized paragraph: err = %v, want one naming paragraph 2", err)
	}
}

func TestParagraphRunJoinsWhatItCut(t *testing.T) {
	text := "one\n\ntwo\n\n\nthree"
	run := newParagraphRun(text)
	if got := run.join(0, len(run.parts)); got != text {
		t.Errorf("join all = %q, want %q", got, text)
	}
	if got := run.join(1, 2); got != "two" {
		t.Errorf("join(1,2) = %q", got)
	}
}

func TestMergeChunkFindingsBuildsOneWholeTextFinding(t *testing.T) {
	texts, seps := []string{"a1\n\na2", "b1", "c1"}, []string{"\n\n", "\n\n"}
	fix := func(s, reason string) []ReviewFinding {
		return []ReviewFinding{{Field: ReviewFieldTranscription, Action: ReviewReplace, Suggestion: s, Reason: reason}}
	}
	got, ok := mergeChunkFindings(ReviewFieldTranscription, "", "FULL", texts, seps, [][]ReviewFinding{nil, fix("B1!", "typo"), nil})
	want := ReviewFinding{Field: ReviewFieldTranscription, Action: ReviewReplace, Current: "FULL", Suggestion: "a1\n\na2\n\nB1!\n\nc1", Reason: "Part 2 of 3: typo"}
	if !ok || got != want {
		t.Errorf("got %+v, %v; want %+v", got, ok, want)
	}
	comment := []ReviewFinding{{Field: ReviewFieldTranscription, Action: ReviewComment, Reason: "unclear"}}
	got, ok = mergeChunkFindings(ReviewFieldTranscription, "", "FULL", texts, seps, [][]ReviewFinding{comment, nil, nil})
	if !ok || got.Action != ReviewComment || got.Suggestion != "" {
		t.Errorf("comment only: got %+v, %v", got, ok)
	}
	if _, ok := mergeChunkFindings(ReviewFieldTranscription, "", "FULL", texts, seps, make([][]ReviewFinding, 3)); ok {
		t.Error("no chunk had a finding, want none")
	}
	// A finding of another locale is not this field's.
	other := []ReviewFinding{{Field: ReviewFieldTranslation, Locale: "pl", Action: ReviewReplace, Suggestion: "x"}}
	if _, ok := mergeChunkFindings(ReviewFieldTranslation, "en", "FULL", texts, seps, [][]ReviewFinding{other, nil, nil}); ok {
		t.Error("a pl finding must not count for en")
	}
}

func longTextInput(paragraphs int, translationParagraphs int) TextReviewInput {
	para := func(tag string, i int) string { return tag + string(rune('A'+i)) + strings.Repeat("x", 2990) }
	join := func(tag string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = para(tag, i)
		}
		return strings.Join(parts, "\n\n")
	}
	return TextReviewInput{
		Language: "deu", Body: join("s", paragraphs), Transcription: join("t", translationParagraphs), Version: "v1",
		Translations: []ReviewTranslation{{Locale: "en", Translation: join("e", translationParagraphs)}},
	}
}

func TestReviewResourceSplitsALongTextAndMergesTheChunks(t *testing.T) {
	// 3 paragraphs of about 3000+3000 characters: two chunks of at most 12000.
	in := longTextInput(3, 3)
	svc, ollama := textReviewService(t, transcriptionFix, transcriptionOK)
	res, err := svc.ReviewResource(t.Context(), in, ReviewPartTranscription)
	if err != nil {
		t.Fatal(err)
	}
	if len(ollama.lastText) != 2 {
		t.Fatalf("%d calls, want 2", len(ollama.lastText))
	}
	third := strings.Split(in.Transcription, "\n\n")[2]
	want := "tr2\n\n" + third // chunk 1 (paragraphs 1-2) replaced by "tr2", chunk 2 kept
	if len(res.Findings) != 1 || res.Findings[0].Suggestion != want || res.Findings[0].Current != in.Transcription {
		t.Errorf("findings = %+v", res.Findings)
	}
	if !strings.Contains(ollama.lastText[1], "sC") || strings.Contains(ollama.lastText[1], "sA") {
		t.Error("the second call must carry only the third paragraph")
	}
}

func TestReviewResourceRefusesALongTextWhoseParagraphsDoNotMatch(t *testing.T) {
	svc, ollama := textReviewService(t, transcriptionOK)
	_, err := svc.ReviewResource(t.Context(), longTextInput(3, 2), ReviewPartTranscription)
	if err == nil || !strings.Contains(err.Error(), "3 paragraphs") || !strings.Contains(err.Error(), "has 2") || len(ollama.lastText) != 0 {
		t.Errorf("err = %v after %d calls, want the counts named and no call", err, len(ollama.lastText))
	}
}
