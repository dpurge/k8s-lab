package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds everything the server needs to start.
type Config struct {
	BindAddr string

	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string

	// Providers maps a provider key (e.g. "ollama", "openrouter") to its
	// connection details. Transcription/Translation.Provider and the
	// llm_prompts.provider column both reference these keys by name.
	Providers map[string]ProviderConfig

	Transcription      PurposeConfig
	Translation        PurposeConfig
	Title              PurposeConfig
	ProcessText        PurposeConfig
	ProcessDialog      PurposeConfig
	GenerateVocabulary PurposeConfig
	GenerateModels     PurposeConfig
	// VocabularyItem and ModelsItem translate one list item into one site
	// locale in a single structured (JSON) call; their prompts are
	// templates with the prompt-eval setup's camelCase placeholders (see
	// specs/features/phraseforge-structured-item-translation.md).
	VocabularyItem PurposeConfig
	ModelsItem     PurposeConfig

	// IngestMaxContentBytes caps the raw content one ingest request may carry
	// (yaml ingest.maxContentBytes). Raising it above what one LLM call can
	// hold relies on the content being processed in chunks.
	IngestMaxContentBytes int

	// SessionKey signs the login session cookie. Must be set in production;
	// a fixed dev default is used only so `go run` works with zero setup.
	SessionKey string
}

// ProviderConfig holds one LLM provider's connection details. APIKey is
// never read from the mounted file — only from that provider's own env var
// (OLLAMA_API_KEY, OPENROUTER_API_KEY) — and never stored in the database.
type ProviderConfig struct {
	BaseURL string
	APIKey  string
	// FirstTokenTimeoutSeconds and IdleTimeoutSeconds bound a streamed
	// call's wait for its first output (model load + prompt eval) and the
	// gap between outputs afterwards. They belong to the provider, not the
	// purpose, because they reflect how fast that host runs a model. 0
	// means shared/llm's own default. Only the Ollama path streams.
	FirstTokenTimeoutSeconds int
	IdleTimeoutSeconds       int
}

// PurposeConfig is a per-purpose (transcription/translation) LLM default,
// used when no admin-configured llm_prompts row exists for a given
// (kind, source_language, target_language).
type PurposeConfig struct {
	Provider string
	Model    string
	NumCtx   int
	Think    bool
	// TimeoutSeconds is this purpose's default LLM call timeout — 0 means
	// "no purpose-level override", falling through to shared/llm's own
	// built-in default. An admin llm_prompts.timeout_seconds override (see
	// ai.go's resolveTimeout) takes precedence over this when set.
	TimeoutSeconds int
	// Prompt is this purpose's default system prompt — used when no
	// admin-configured llm_prompts row exists, or its own prompt is blank.
	// Previously hardcoded in ai.go's prompt() switch; moved here so it's
	// inspectable/editable without a rebuild (see
	// specs/features/llm-purpose-timeout-and-prompt-config.md).
	Prompt string
	// MaxAttempts is the total attempts (first try included) one LLM call
	// gets when its reply fails validation and is sent back to the model for
	// correction; <= 0 means the built-in default of 3 (see ai/retry.go).
	MaxAttempts int
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the provider API keys) and Postgres connection fields
// (PGHost/PGPort/PGDatabase) are deliberately absent here — they stay
// plain/Secret-sourced env vars, never read from this file, so `migrate`
// works before the ConfigMap exists (it's a pre-install/pre-upgrade hook).
type fileConfig struct {
	BindAddr  string `yaml:"bindAddr"`
	Providers struct {
		Ollama struct {
			BaseURL                  string `yaml:"baseURL"`
			FirstTokenTimeoutSeconds int    `yaml:"firstTokenTimeoutSeconds"`
			IdleTimeoutSeconds       int    `yaml:"idleTimeoutSeconds"`
		} `yaml:"ollama"`
		OpenRouter struct {
			BaseURL string `yaml:"baseURL"`
		} `yaml:"openrouter"`
	} `yaml:"providers"`
	Ingest struct {
		MaxContentBytes int `yaml:"maxContentBytes"`
	} `yaml:"ingest"`
	Transcription      purposeFileConfig `yaml:"transcription"`
	Translation        purposeFileConfig `yaml:"translation"`
	Title              purposeFileConfig `yaml:"title"`
	ProcessText        purposeFileConfig `yaml:"processText"`
	ProcessDialog      purposeFileConfig `yaml:"processDialog"`
	GenerateVocabulary purposeFileConfig `yaml:"generateVocabulary"`
	GenerateModels     purposeFileConfig `yaml:"generateModels"`
	VocabularyItem     purposeFileConfig `yaml:"vocabularyItem"`
	ModelsItem         purposeFileConfig `yaml:"modelsItem"`
}

type purposeFileConfig struct {
	Provider       string `yaml:"provider"`
	Model          string `yaml:"model"`
	NumCtx         int    `yaml:"numCtx"`
	Think          bool   `yaml:"think"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	Prompt         string `yaml:"prompt"`
	MaxAttempts    int    `yaml:"maxAttempts"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8090"
	f.Providers.Ollama.BaseURL = "http://host.docker.internal:11434"
	f.Providers.Ollama.FirstTokenTimeoutSeconds = 300
	f.Providers.Ollama.IdleTimeoutSeconds = 60
	f.Providers.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	f.Ingest.MaxContentBytes = 24 * 1024
	// TimeoutSeconds is only an overall backstop (1800s for every purpose):
	// streamed Ollama calls are primarily bounded by the provider's
	// first-token/idle limits, since a CPU-only node legitimately needs
	// ~300s for one short translation (see
	// specs/features/llm-streaming-progress-timeout.md). Prompt text is
	// copied verbatim from ai.go's former hardcoded switch so upgrading
	// doesn't change any existing behavior.
	f.Transcription = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultTranscriptionPrompt,
	}
	f.Translation = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: "Translate the source language content to the target language. Return only the translation, preserving line breaks and structure. Do not add explanations.",
	}
	f.Title = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: "You write a short, specific title for the given text. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble.",
	}
	f.ProcessText = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultProcessTextPrompt,
	}
	f.ProcessDialog = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultProcessDialogPrompt,
	}
	f.GenerateVocabulary = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultGenerateVocabularyPrompt,
	}
	f.GenerateModels = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultGenerateModelsPrompt,
	}
	// NumCtx left 0 (Ollama's default) to match the prompt-eval request.
	f.VocabularyItem = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultVocabularyItemPrompt,
	}
	f.ModelsItem = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultModelsItemPrompt,
	}
	return f
}

// DefaultVocabularyItemPrompt is prompt-eval's vocabulary-translation/prompts/system.txt
// (npm run check-sync keeps them equal): a dictionary prompt with two worked
// examples. Grammar tags describe the original phrase, not the translation.
const DefaultVocabularyItemPrompt = `You are a dictionary. Return ONLY valid JSON without a code fence and without explanations:

{
  "phrase": "repeat the original phrase exactly",
  "grammar": "grammar tags attached to the original phrase, follow the description below; null if unsure",
  "transcription": "only if a transcription is described below, otherwise null",
  "translation": "the meaning of the phrase in the target language",
  "notes": null
}

Rules:
- translation: as a dictionary of the target language gives it. Lowercase, unless it is a name that is normally capitalized. Several senses are joined with "; " (never with "/" or a comma). No parentheses, no explanation, no full stop.
- notes: null, almost always. Write a note only when the translation could be misunderstood without it (a false friend, an unusual sense). Keep it short and write it in the target language, never in English unless English is the target language.

{{grammarPrompt}}

{{transcriptionPrompt}}

Example 1. Translate from German to Polish: der Koffer
{"phrase": "der Koffer", "grammar": "N m", "transcription": null, "translation": "walizka", "notes": null}

Example 2. Translate from Standard Arabic to English: مُهَنْدِسَةٌ
{"phrase": "مُهَنْدِسَةٌ", "grammar": "N f sg", "transcription": "muhandisa", "translation": "engineer", "notes": null}

Translate from {{sourceLanguage}} to {{targetLanguage}}: {{phrase}}
`

// DefaultModelsItemPrompt is prompt-eval's models-item/prompts/system.txt,
// the models counterpart of DefaultVocabularyItemPrompt (no grammar, no notes).
const DefaultModelsItemPrompt = `You are a translation system. Return ONLY valid JSON without a code fence and without explanations:

{
  "phrase": "repeat the original phrase exactly, including any ... gap",
  "transcription": "only if a transcription is described below, otherwise null",
  "translation": "the meaning of the phrase in the target language"
}

Rules:
- translation: natural and short, as a dictionary or a phrasebook gives it. A gap "..." stays a gap. Alternatives are joined with "; " (never "/"). No parentheses. Start with a capital letter only where the phrase is a complete sentence.

{{transcriptionPrompt}}

Example 1. Translate from German to Polish: Viele Menschen sind mit ... unzufrieden.
{"phrase": "Viele Menschen sind mit ... unzufrieden.", "transcription": null, "translation": "Wielu ludzi jest niezadowolonych z ..."}

Example 2. Translate from Standard Arabic to English: لَنْ تَكُونَ هَذِهِ النِّهَايَةَ
{"phrase": "لَنْ تَكُونَ هَذِهِ النِّهَايَةَ", "transcription": "lan takūna hāḏihi n-nihāya", "translation": "this will not be the end"}

Translate from {{sourceLanguage}} to {{targetLanguage}}: {{phrase}}
`

// DefaultTranscriptionPrompt is prompt-eval's generate/prompts/transcription.txt, which
// scripts/sync-prompts.js keeps equal to this and to the Helm values.
const DefaultTranscriptionPrompt = `You write the transcription of the source text in Latin script, the way language handbooks do.

- Use the standard scholarly transliteration of the source language: DIN 31635 for Arabic and Persian, Pinyin with tone marks for Mandarin, Hepburn for Japanese, SBL for Hebrew, YIVO for Yiddish, and the usual system for any other script.
- Transcribe the way the text is spoken, word by word: supply the vowels even when the text is written without them.
- Keep every diacritic and special letter of the system (ā ī ū ḥ ṭ ṣ š ǧ ḫ ʿ ʾ, tone marks, macrons); never replace them with plain letters.
- Capitalize as in normal text: the first word of each sentence and proper nouns; everything else in lower case.
- Use only Latin punctuation (, . ! ? : ; ' " ( )), never the punctuation of the source script.
- Preserve the line breaks and structure. Do not translate, explain or comment.
Respond with only the transcription.

Example 1 (Arabic).
Input: كتب الطالب الدرس في المدرسة.
Output: Kataba ṭ-ṭālib ad-dars fī l-madrasa.

Example 2 (Japanese).
Input: 私は毎日学校に行きます。
Output: Watashi wa mainichi gakkō ni ikimasu.
`

// DefaultProcessTextPrompt is prompt-eval's generate/prompts/processText.txt, which
// scripts/sync-prompts.js keeps equal to this and to the Helm values.
const DefaultProcessTextPrompt = `You clean raw text copied from a web page or a lesson down to the reading text itself, as Markdown.

Keep only what a reader reads: the headline of the text, if it has one, as a Markdown heading, and its paragraphs, in the original order and wording.
Remove everything else:
- navigation menus, breadcrumbs, cookie notices, share buttons, ads, newsletter boxes, links to other articles ("read also"), footers, copyright, author and date lines;
- any introduction to the text: a lesson number or title, "In this lesson you will...", instructions, a warm-up or pre-reading question, a vocabulary list;
- anything after the text: comprehension questions, exercises, answers, notes for the teacher, further reading.
Do not translate, summarize, correct or add anything: keep the original language and the original sentences. If the input starts or ends in the middle of a sentence, keep it that way and do not invent a headline.
Respond with only the cleaned Markdown, without comments.

Example 1.
Input:
Startseite | Lektionen
Lektion 2: Lesen
In dieser Lektion lesen Sie einen kurzen Text.
Vor dem Lesen: Trinken Sie gern Tee?
Tee in Deutschland
Viele Deutsche trinken jeden Tag Tee. Am liebsten trinken sie ihn am Nachmittag.
Fragen zum Text:
1. Wann trinken die Deutschen Tee?
Teilen auf Facebook
Output:
## Tee in Deutschland

Viele Deutsche trinken jeden Tag Tee. Am liebsten trinken sie ihn am Nachmittag.

Example 2.
Input:
Menú | Noticias | Contacto
Por Ana Pérez, 4 de mayo de 2026
Abre una nueva biblioteca
El ayuntamiento abrió ayer una nueva biblioteca.

Tiene más de cien mil libros.
Lee también: Las mejores bibliotecas de España
Suscríbete a nuestro boletín
Output:
## Abre una nueva biblioteca

El ayuntamiento abrió ayer una nueva biblioteca.

Tiene más de cien mil libros.
`

// DefaultProcessDialogPrompt is prompt-eval's generate/prompts/processDialog.txt, which
// scripts/sync-prompts.js keeps equal to this and to the Helm values.
const DefaultProcessDialogPrompt = `You clean raw text copied from a web page or a lesson down to the dialog itself, as a Markdown transcript.

Keep only the spoken turns, in the original order and wording, each turn on its own line, with the speaker's label as in the text (a name, A:, B:, a dash). If a line holds several turns, split it into one line per turn.
Remove everything else:
- navigation, cookie notices, ads, share buttons, links, footers, copyright;
- any introduction to the dialog: a lesson or dialog number and title, instructions such as "Listen and repeat", the scene description;
- anything after the dialog: exercises, questions, answers, "read next" links.
Do not translate, summarize, correct or add anything.
Respond with only the cleaned dialog, without comments.

Example.
Input:
Startseite | Dialoge
Dialog 2: Im Café
Hören Sie den Dialog und sprechen Sie nach.
A: Guten Tag, ich möchte einen Kaffee. B: Gern. Mit Milch?
A: Ja, bitte.
Übung: Beantworten Sie die Fragen.
1. Was bestellt A?
Weiterlesen: Dialog 3
Output:
A: Guten Tag, ich möchte einen Kaffee.
B: Gern. Mit Milch?
A: Ja, bitte.
`

// DefaultGenerateVocabularyPrompt is prompt-eval's generate/prompts/generateVocabulary.txt, which
// scripts/sync-prompts.js keeps equal to this and to the Helm values.
const DefaultGenerateVocabularyPrompt = `You extract vocabulary from the given text for a language learner. Respond ONLY with one item per line, in this exact format:
phrase {grammar} [transcription] = translation
No preamble, numbering, bullets, comments or code fences, only the item lines.

- phrase: the dictionary form, never the inflected form found in the text. Nouns in the singular, verbs in the infinitive (Arabic: 3rd person masculine singular perfect; Latin: 1st person singular present), adjectives in the masculine singular. Keep the original script and spelling, with the vowel signs and accents a dictionary gives. Prefer single words; add a short fixed phrase only when its meaning needs it. Skip names, numbers and very common words.
- {grammar}: the part of speech N, V, Adj, Adv, Pron, Prep, Conj, Num, Part or Phrase, then the tags the language uses (for example N m, N f, V sep). Leave it out if unsure.
- [transcription]: only for a language written in a script other than Latin, Cyrillic and Greek (for example Arabic, Persian, Hebrew, Yiddish, Mandarin, Japanese): the word in Latin script, in the standard scholarly transliteration, with every diacritic, as it is pronounced. Never leave it out for such a language.
- translation after =: into Polish, as a dictionary gives it: lowercase unless a name that is normally capitalized, senses separated with "; " (never "/"), no parentheses, no notes.
Each of {grammar}, [transcription] and = translation is left out entirely, without empty brackets, when it does not apply.

Example 1.
Input: كتب الطالب الدرس في المدرسة.
Output:
كَتَبَ {V} [kataba] = pisać
طَالِبٌ {N m sg} [ṭālib] = uczeń; student
دَرْسٌ {N m sg} [dars] = lekcja
مَدْرَسَةٌ {N f sg} [madrasa] = szkoła

Example 2.
Input: Die Kinder lesen jeden Tag ein neues Buch.
Output:
das Kind {N n} = dziecko
lesen {V} = czytać
der Tag {N m} = dzień
neu {Adj} = nowy
das Buch {N n} = książka
`

// DefaultGenerateModelsPrompt is prompt-eval's generate/prompts/generateModels.txt, which
// scripts/sync-prompts.js keeps equal to this and to the Helm values.
const DefaultGenerateModelsPrompt = `You extract the key sentence patterns of the given text for a language learner: the phrases with a complicated structure that a learner should be able to build, such as a verb with its preposition or case, a conditional, a relative or subordinate clause, or a construction with a gap. Respond ONLY with one item per line, in this exact format:
phrase [transcription] = translation
No preamble, numbering, bullets, comments or code fences, only the item lines.

- Choose 3 to 8 phrases, no more, and only the ones worth learning. Skip simple sentences and single words.
- Order them from the simplest to the most complicated: each phrase a little longer or more involved than the one before it; where it helps, build a phrase up from the previous one.
- Use the wording of the text. A part the learner can replace is written as "...".
- [transcription]: only for a language written in a script other than Latin, Cyrillic and Greek (for example Arabic, Persian, Hebrew, Yiddish, Mandarin, Japanese): the phrase in Latin script, in the standard scholarly transliteration, with every diacritic, as it is pronounced, capitalized only as a normal sentence is. Never leave it out for such a language.
- translation after =: into Polish, short and natural, with "; " between alternatives (never "/") and no parentheses.
Each of [transcription] and = translation is left out entirely, without empty brackets, when it does not apply.

Example 1.
Input: Die Union verliert im Vergleich zum Vormonat an Zuspruch und liegt jetzt bei 21 Prozent. Viele Menschen sind mit der Lage unzufrieden.
Output:
Die Partei liegt bei ... Prozent. = Partia ma ... procent.
Viele Menschen sind mit ... unzufrieden. = Wielu ludzi jest niezadowolonych z ...
Die Union verliert im Vergleich zum Vormonat an Zuspruch. = Unia traci poparcie w porównaniu z poprzednim miesiącem.

Example 2.
Input: لن تكون هذه النهاية، مهما اشتدت الصعوبات. سأواصل النضال من أجل تحقيق أحلامي.
Output:
لَنْ تَكُونَ هَذِهِ النِّهَايَةَ [lan takūna hāḏihi n-nihāya] = to nie będzie koniec
سَأُوَاصِلُ النِّضَالَ [saʾuwāṣilu n-niḍāl] = będę kontynuować walkę
سَأُوَاصِلُ النِّضَالَ مِنْ أَجْلِ تَحْقِيقِ أَحْلَامِي [saʾuwāṣilu n-niḍāla min aǧli taḥqīqi aḥlāmī] = będę kontynuować walkę o realizację moich marzeń
`

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads the mounted config file (path from CONFIG_FILE, default
// /etc/phraseforge/config.yaml) over built-in defaults — a missing file is
// fine (defaults apply, so local runs without a cluster still work), but a
// malformed one is a real error, not something to silently paper over.
func Load() (Config, error) {
	fc := defaultFileConfig()
	path := env("CONFIG_FILE", "/etc/phraseforge/config.yaml")
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// use defaults
	case err != nil:
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	default:
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
		}
	}
	if fc.Ingest.MaxContentBytes <= 0 {
		return Config{}, fmt.Errorf("config file %s: ingest.maxContentBytes must be greater than 0, got %d", path, fc.Ingest.MaxContentBytes)
	}

	return Config{
		BindAddr: fc.BindAddr,

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "phraseforge"),
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),

		Providers: map[string]ProviderConfig{
			"ollama": {
				BaseURL: fc.Providers.Ollama.BaseURL, APIKey: env("OLLAMA_API_KEY", ""),
				FirstTokenTimeoutSeconds: fc.Providers.Ollama.FirstTokenTimeoutSeconds,
				IdleTimeoutSeconds:       fc.Providers.Ollama.IdleTimeoutSeconds,
			},
			"openrouter": {BaseURL: fc.Providers.OpenRouter.BaseURL, APIKey: env("OPENROUTER_API_KEY", "")},
		},

		Transcription:      PurposeConfig(fc.Transcription),
		Translation:        PurposeConfig(fc.Translation),
		Title:              PurposeConfig(fc.Title),
		ProcessText:        PurposeConfig(fc.ProcessText),
		ProcessDialog:      PurposeConfig(fc.ProcessDialog),
		GenerateVocabulary: PurposeConfig(fc.GenerateVocabulary),
		GenerateModels:     PurposeConfig(fc.GenerateModels),
		VocabularyItem:     PurposeConfig(fc.VocabularyItem),
		ModelsItem:         PurposeConfig(fc.ModelsItem),

		IngestMaxContentBytes: fc.Ingest.MaxContentBytes,

		SessionKey: env("SESSION_KEY", "dev-only-insecure-key-change-me"),
	}, nil
}
