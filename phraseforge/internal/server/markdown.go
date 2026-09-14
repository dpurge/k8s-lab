package server

import (
	"fmt"
	"strings"

	"phraseforge/internal/models"
	"phraseforge/internal/vocabulary"
)

type vocabViewRow struct {
	vocabulary.Item
	Translation string
	Notes       string
}

type modelsViewRow struct {
	models.Item
	Translation string
}

func textBlockMarkdown(body, role, language, script string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("{start-text as=%s lang=%s script=%s}\n%s\n{end-text}", role, language, script, body)
}

func vocabularyMarkdown(language, script string, rows []vocabViewRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{start-vocabulary lang=%s script=%s}\n", language, script)
	for _, row := range rows {
		line := row.Phrase
		if strings.TrimSpace(row.Grammar) != "" {
			line += " {" + row.Grammar + "}"
		}
		if strings.TrimSpace(row.Transcription) != "" {
			line += " [" + row.Transcription + "]"
		}
		if strings.TrimSpace(row.Translation) != "" {
			line += " = " + row.Translation
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("{end-vocabulary}")
	return b.String()
}

func modelsMarkdown(language, script string, rows []modelsViewRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{start-models lang=%s script=%s}\n", language, script)
	for _, row := range rows {
		line := row.Phrase
		if strings.TrimSpace(row.Transcription) != "" {
			line += " [" + row.Transcription + "]"
		}
		if strings.TrimSpace(row.Translation) != "" {
			line += " = " + row.Translation
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("{end-models}")
	return b.String()
}
