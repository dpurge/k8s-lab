package languages

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// wordClassKey is the reserved category key every template pivots on.
const wordClassKey = "word_class"

// TagPattern: tags may mix case (e.g. "V" and "v" are distinct, never
// case-folded) and start with a digit, so bare person markers like "1" work.
var TagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// keyPattern is slightly stricter than TagPattern (no hyphen) since category
// keys double as JSON-ish identifiers (e.g. "word_class").
var keyPattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

var ErrSchemaInvalid = errors.New("invalid grammar schema")

// GrammarCategory is one category a language defines (e.g. "gender" -> {m,f,n}).
type GrammarCategory struct {
	Key       string   `json:"key"`
	TagValues []string `json:"tag_values"`
}

// GrammarTemplate names the extra categories one word class may carry, on
// top of its own word_class tag. AllowedCategories is a ceiling, not a
// requirement — an entry may use any subset, including none.
type GrammarTemplate struct {
	WordClass         string   `json:"word_class"`
	AllowedCategories []string `json:"allowed_categories"`
}

// Schema is a language's full grammar schema.
type Schema struct {
	Categories []GrammarCategory `json:"categories"`
	Templates  []GrammarTemplate `json:"templates"`
}

func (s *Service) GetSchema(ctx context.Context, languageCode string) (Schema, error) {
	catRows, err := s.db.Query(ctx, `
		SELECT key, tag_values FROM grammar_categories WHERE language_code = $1 ORDER BY key
	`, languageCode)
	if err != nil {
		return Schema{}, err
	}
	var out Schema
	out.Categories = make([]GrammarCategory, 0)
	for catRows.Next() {
		var c GrammarCategory
		if err := catRows.Scan(&c.Key, &c.TagValues); err != nil {
			catRows.Close()
			return Schema{}, err
		}
		out.Categories = append(out.Categories, c)
	}
	if err := catRows.Err(); err != nil {
		return Schema{}, err
	}
	catRows.Close()

	tplRows, err := s.db.Query(ctx, `
		SELECT word_class, allowed_categories FROM grammar_templates WHERE language_code = $1 ORDER BY word_class
	`, languageCode)
	if err != nil {
		return Schema{}, err
	}
	defer tplRows.Close()
	out.Templates = make([]GrammarTemplate, 0)
	for tplRows.Next() {
		var t GrammarTemplate
		if err := tplRows.Scan(&t.WordClass, &t.AllowedCategories); err != nil {
			return Schema{}, err
		}
		out.Templates = append(out.Templates, t)
	}
	return out, tplRows.Err()
}

// ReplaceSchema validates sch, then atomically replaces languageCode's entire
// grammar schema with it (PUT semantics: full replace, not a merge).
func (s *Service) ReplaceSchema(ctx context.Context, languageCode string, sch Schema) error {
	if err := validateSchema(sch); err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if Commit already succeeded

	if _, err := tx.Exec(ctx, `DELETE FROM grammar_templates WHERE language_code = $1`, languageCode); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM grammar_categories WHERE language_code = $1`, languageCode); err != nil {
		return err
	}
	for _, c := range sch.Categories {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grammar_categories (language_code, key, tag_values) VALUES ($1, $2, $3)
		`, languageCode, c.Key, c.TagValues); err != nil {
			return err
		}
	}
	for _, t := range sch.Templates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grammar_templates (language_code, word_class, allowed_categories) VALUES ($1, $2, $3)
		`, languageCode, t.WordClass, t.AllowedCategories); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// validateSchema checks format and cross-references entirely in Go — Postgres
// CHECK constraints can't validate array elements or cross-table references.
func validateSchema(sch Schema) error {
	seenCategory := make(map[string]bool, len(sch.Categories))
	// Tag values must be unique across ALL categories of a language, not just
	// within one — an entry's tags (a flat list) are mapped back to their
	// category by value alone (see ValidateAndCanonicalizeTags), which would
	// be ambiguous if e.g. "1" could mean both a "person" and a "number" tag.
	seenValue := make(map[string]string) // tag value -> owning category key
	for _, c := range sch.Categories {
		if !keyPattern.MatchString(c.Key) {
			return fmt.Errorf("%w: category key %q must match %s", ErrSchemaInvalid, c.Key, keyPattern)
		}
		if seenCategory[c.Key] {
			return fmt.Errorf("%w: duplicate category key %q", ErrSchemaInvalid, c.Key)
		}
		seenCategory[c.Key] = true
		if len(c.TagValues) == 0 {
			return fmt.Errorf("%w: category %q must have at least one tag value", ErrSchemaInvalid, c.Key)
		}
		for _, v := range c.TagValues {
			if !TagPattern.MatchString(v) {
				return fmt.Errorf("%w: category %q tag value %q must match %s", ErrSchemaInvalid, c.Key, v, TagPattern)
			}
			if owner, dup := seenValue[v]; dup {
				return fmt.Errorf("%w: tag value %q used in both category %q and %q — tag values must be unique across the whole language", ErrSchemaInvalid, v, owner, c.Key)
			}
			seenValue[v] = c.Key
		}
	}

	if len(sch.Templates) == 0 {
		return nil
	}
	if !seenCategory[wordClassKey] {
		return fmt.Errorf("%w: templates require a %q category to be defined", ErrSchemaInvalid, wordClassKey)
	}
	var wordClasses []string
	for _, c := range sch.Categories {
		if c.Key == wordClassKey {
			wordClasses = c.TagValues
		}
	}

	seenTemplate := make(map[string]bool, len(sch.Templates))
	for _, t := range sch.Templates {
		if !slices.Contains(wordClasses, t.WordClass) {
			return fmt.Errorf("%w: template word_class %q is not a value of the %q category", ErrSchemaInvalid, t.WordClass, wordClassKey)
		}
		if seenTemplate[t.WordClass] {
			return fmt.Errorf("%w: duplicate template for word_class %q", ErrSchemaInvalid, t.WordClass)
		}
		seenTemplate[t.WordClass] = true
		seenAllowed := make(map[string]bool, len(t.AllowedCategories))
		for _, key := range t.AllowedCategories {
			if key == wordClassKey {
				return fmt.Errorf("%w: template %q must not list %q in allowed_categories (it's the template's own key)", ErrSchemaInvalid, t.WordClass, wordClassKey)
			}
			if !seenCategory[key] {
				return fmt.Errorf("%w: template %q references undefined category %q", ErrSchemaInvalid, t.WordClass, key)
			}
			if seenAllowed[key] {
				return fmt.Errorf("%w: template %q lists category %q more than once", ErrSchemaInvalid, t.WordClass, key)
			}
			seenAllowed[key] = true
		}
	}
	return nil
}

var ErrTagsInvalid = errors.New("invalid grammar tags")

// tagCategories builds the tag-value -> owning-category-key lookup that
// ValidateAndCanonicalizeTags needs. Safe to assume unambiguous: ReplaceSchema
// enforces cross-category tag-value uniqueness before a schema is ever stored.
func (sch Schema) tagCategories() map[string]string {
	m := make(map[string]string)
	for _, c := range sch.Categories {
		for _, v := range c.TagValues {
			m[v] = c.Key
		}
	}
	return m
}

// ValidateAndCanonicalizeTags checks tags against sch (exactly one word_class
// tag; every other tag's category must be on that word class's template —
// an unlisted word class means no extra categories are allowed at all; at
// most one tag per category) and returns the word_class tag plus a
// canonical ordering (word class first, then the rest sorted by category
// key) so that two requests naming the same tag set in a different order
// are recognized as the same entry identity.
func (sch Schema) ValidateAndCanonicalizeTags(tags []string) (wordClass string, canonical []string, err error) {
	tagCategory := sch.tagCategories()

	type placed struct{ tag, category string }
	var wordClassTags []string
	var others []placed
	seenTag := make(map[string]bool, len(tags))
	seenOtherCategory := make(map[string]string, len(tags))

	for _, tag := range tags {
		if seenTag[tag] {
			return "", nil, fmt.Errorf("%w: duplicate tag %q", ErrTagsInvalid, tag)
		}
		seenTag[tag] = true

		category, ok := tagCategory[tag]
		if !ok {
			return "", nil, fmt.Errorf("%w: %q is not a grammar tag defined for this language", ErrTagsInvalid, tag)
		}
		if category == wordClassKey {
			wordClassTags = append(wordClassTags, tag)
			continue
		}
		if owner, dup := seenOtherCategory[category]; dup {
			return "", nil, fmt.Errorf("%w: both %q and %q belong to category %q — an entry may carry at most one tag per category", ErrTagsInvalid, owner, tag, category)
		}
		seenOtherCategory[category] = tag
		others = append(others, placed{tag: tag, category: category})
	}

	switch len(wordClassTags) {
	case 0:
		return "", nil, fmt.Errorf("%w: exactly one %q tag is required", ErrTagsInvalid, wordClassKey)
	case 1:
		wordClass = wordClassTags[0]
	default:
		return "", nil, fmt.Errorf("%w: only one %q tag is allowed, got %v", ErrTagsInvalid, wordClassKey, wordClassTags)
	}

	// A word class with no template at all means no extra categories are
	// permitted — the ceiling defaults to empty, not "anything goes".
	var allowed []string
	for _, t := range sch.Templates {
		if t.WordClass == wordClass {
			allowed = t.AllowedCategories
			break
		}
	}
	for _, p := range others {
		if !slices.Contains(allowed, p.category) {
			return "", nil, fmt.Errorf("%w: category %q is not allowed for word class %q", ErrTagsInvalid, p.category, wordClass)
		}
	}

	slices.SortFunc(others, func(a, b placed) int {
		if a.category != b.category {
			return strings.Compare(a.category, b.category)
		}
		return strings.Compare(a.tag, b.tag)
	})

	canonical = make([]string, 0, len(others)+1)
	canonical = append(canonical, wordClass)
	for _, p := range others {
		canonical = append(canonical, p.tag)
	}
	return wordClass, canonical, nil
}
