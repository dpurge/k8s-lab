package config

import (
	"embed"
	"fmt"
)

// promptFS is a copy of prompt-eval's prompt/default directory (default
// system prompts and snippets). prompt-eval is the source of truth: the Docker
// build cannot reach it, so `npm run sync-prompts` copies it here and
// `npm run check-sync` fails when the copy differs.
//
//go:embed prompts/default
var promptFS embed.FS

// readPrompt returns an embedded prompt file byte for byte. The files ship
// inside the binary, so a missing one is a programmer error.
func readPrompt(path string) string {
	text, err := promptFS.ReadFile("prompts/default/" + path)
	if err != nil {
		panic(fmt.Sprintf("config: embedded prompt %s: %v", path, err))
	}
	return string(text)
}

// Default system prompts, one per prompt-eval/prompt/default/system file.
var (
	DefaultTranslationPrompt        = readPrompt("system/generate-translation.txt")
	DefaultTranscriptionPrompt      = readPrompt("system/generate-transcription.txt")
	DefaultTitlePrompt              = readPrompt("system/generate-title.txt")
	DefaultProcessTextPrompt        = readPrompt("system/process-text.txt")
	DefaultProcessDialogPrompt      = readPrompt("system/process-dialog.txt")
	DefaultGenerateVocabularyPrompt = readPrompt("system/generate-vocabulary.txt")
	DefaultGenerateModelsPrompt     = readPrompt("system/generate-models.txt")
	DefaultVocabularyItemPrompt     = readPrompt("system/generate-vocabulary-item.txt")
	DefaultModelsItemPrompt         = readPrompt("system/generate-models-item.txt")
)

// Default language snippets, rendered as {{grammarPrompt}} and
// {{transcriptionPrompt}} (see the ai package) for a language without its own.
var (
	DefaultGrammarSnippet       = readPrompt("snippet/grammar.txt")
	DefaultTranscriptionSnippet = readPrompt("snippet/transcription.txt")
)
