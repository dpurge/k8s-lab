package workflow

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed prompts
var defaultPromptFiles embed.FS

// Placeholders the prompt template and its snippets may use.
const (
	placeholderLanguage      = "language"
	placeholderOutput        = "output"
	placeholderLanguageRules = "languageRules"
	placeholderOutputRules   = "outputRules"
)

var (
	placeholderRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	nameRe        = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	blankRunsRe   = regexp.MustCompile(`\n{3,}`)

	// The system template may use the two selector variables and the two
	// snippet placeholders; a snippet may only use the selector variables, so
	// it can never pull in another snippet.
	templatePlaceholders = map[string]bool{
		placeholderLanguage: true, placeholderOutput: true,
		placeholderLanguageRules: true, placeholderOutputRules: true,
	}
	snippetPlaceholders = map[string]bool{placeholderLanguage: true, placeholderOutput: true}
)

// Prompts is the system prompt template with its per-language and per-output
// snippets. Build one with LoadPrompts, which rejects any placeholder it does
// not know, so a bad prompt fails at startup and never reaches a model.
type Prompts struct {
	// UIGuide is added to the system prompt of every step of a workflow that
	// declares ui: how to write an interface and which components there are.
	UIGuide  string
	template string
	lang     map[string]string
	output   map[string]string
}

// DefaultPrompts returns the prompts embedded in the binary.
func DefaultPrompts() (*Prompts, error) {
	sub, err := fs.Sub(defaultPromptFiles, "prompts")
	if err != nil {
		return nil, fmt.Errorf("open embedded prompts: %w", err)
	}
	return LoadPrompts(sub)
}

// LoadPrompts reads step.system.txt, lang/<code>/rules.txt and
// output/<kind>/rules.txt from fsys.
func LoadPrompts(fsys fs.FS) (*Prompts, error) {
	tpl, err := fs.ReadFile(fsys, "step.system.txt")
	if err != nil {
		return nil, fmt.Errorf("read step.system.txt: %w", err)
	}
	p := &Prompts{template: string(tpl)}
	if err := checkPlaceholders("step.system.txt", p.template, templatePlaceholders); err != nil {
		return nil, err
	}
	if p.lang, err = loadSnippets(fsys, "lang"); err != nil {
		return nil, err
	}
	if p.output, err = loadSnippets(fsys, "output"); err != nil {
		return nil, err
	}
	return p, nil
}

func loadSnippets(fsys fs.FS, dir string) (map[string]string, error) {
	snippets := map[string]string{}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return snippets, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !nameRe.MatchString(e.Name()) {
			return nil, fmt.Errorf("%s/%s: name must match %s", dir, e.Name(), nameRe)
		}
		path := dir + "/" + e.Name() + "/rules.txt"
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if err := checkPlaceholders(path, string(body), snippetPlaceholders); err != nil {
			return nil, err
		}
		snippets[e.Name()] = strings.TrimSpace(string(body))
	}
	return snippets, nil
}

func checkPlaceholders(file, text string, allowed map[string]bool) error {
	var unknown []string
	for _, m := range placeholderRe.FindAllStringSubmatch(text, -1) {
		if !allowed[m[1]] {
			unknown = append(unknown, "{{"+m[1]+"}}")
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%s: unknown placeholder %s", file, strings.Join(unknown, ", "))
	}
	return nil
}

// Languages returns the language codes that have a snippet, sorted.
func (p *Prompts) Languages() []string { return sortedKeys(p.lang) }

// Outputs returns the output kinds that have a snippet, sorted.
func (p *Prompts) Outputs() []string { return sortedKeys(p.output) }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// System renders the system prompt for a language code and an output kind,
// either of which may be empty or have no snippet; its rules text is then
// empty. The selected snippets may use {{language}} and {{output}}; text that
// is inserted is not scanned for placeholders again.
func (p *Prompts) System(language, outputKind string) string {
	selectors := map[string]string{
		placeholderLanguage: language,
		placeholderOutput:   outputKind,
	}
	vars := map[string]string{
		placeholderLanguage:      language,
		placeholderOutput:        outputKind,
		placeholderLanguageRules: fillSelectors(p.lang[language], selectors),
		placeholderOutputRules:   fillSelectors(p.output[outputKind], selectors),
	}
	rendered := placeholderRe.ReplaceAllStringFunc(p.template, func(m string) string {
		return vars[strings.Trim(m, "{}")]
	})
	return tidyBlankLines(rendered)
}

func fillSelectors(snippet string, selectors map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(snippet, func(m string) string {
		return selectors[strings.Trim(m, "{}")]
	})
}

// tidyBlankLines collapses the runs of blank lines left where an empty
// snippet was substituted, and trims the ends.
func tidyBlankLines(s string) string {
	return strings.TrimSpace(blankRunsRe.ReplaceAllString(s, "\n\n"))
}
