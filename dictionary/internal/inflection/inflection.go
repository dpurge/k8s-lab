// Package inflection manages inflection templates (markdown with tag
// placeholders) and inflection forms (the actual wordforms), and renders a
// template against stored forms for a requested tag combination.
//
// Matching is subset-based in both directions:
//   - a template applies to a request when the request's tags ⊆ the
//     template's index_tags (a broader request like {V} matches every
//     tense's template; a narrower one like {V,praes} matches just that one)
//   - a form belongs to a template when the template's index_tags ⊆ the
//     form's tags (the form carries the index tags plus whatever the
//     template's placeholders further distinguish, e.g. person)
//
// A placeholder in a template body is written as "{tag}" — a literal tag
// value, not a positional index. It's filled by the one form (among those
// belonging to the template) whose tags, minus the template's own index
// tags, contain that tag value.
package inflection

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"dictionary/internal/languages"
)

var (
	ErrNotFound      = errors.New("inflection resource not found")
	ErrAlreadyExists = errors.New("a template with these index tags already exists")
	ErrValidation    = errors.New("validation failed")
)

type Template struct {
	ID           int64    `json:"id"`
	LanguageCode string   `json:"language_code"`
	IndexTags    []string `json:"index_tags"`
	Body         string   `json:"body"`
}

// Form is one concrete wordform, e.g. "am" tagged {V,praes,sg,1}.
type Form struct {
	ID           int64    `json:"id"`
	LanguageCode string   `json:"language_code"`
	Tags         []string `json:"tags"`
	Text         string   `json:"text"`
}

type RenderedTable struct {
	IndexTags []string `json:"index_tags"`
	Markdown  string   `json:"markdown"`
}

type Service struct {
	db        *pgxpool.Pool
	languages *languages.Service
}

func New(db *pgxpool.Pool, languagesSvc *languages.Service) *Service {
	return &Service{db: db, languages: languagesSvc}
}

func (s *Service) canonicalizeTags(ctx context.Context, languageCode string, tags []string) ([]string, error) {
	sch, err := s.languages.GetSchema(ctx, languageCode)
	if err != nil {
		return nil, err
	}
	_, canonical, err := sch.ValidateAndCanonicalizeTags(tags)
	return canonical, err
}

// ── Templates ────────────────────────────────────────────────────────────────

func (s *Service) CreateTemplate(ctx context.Context, languageCode string, indexTags []string, body string) (Template, error) {
	if strings.TrimSpace(body) == "" {
		return Template{}, fmt.Errorf("%w: body must not be empty", ErrValidation)
	}
	canonical, err := s.canonicalizeTags(ctx, languageCode, indexTags)
	if err != nil {
		return Template{}, err
	}
	var t Template
	err = s.db.QueryRow(ctx, `
		INSERT INTO inflection_templates (language_code, index_tags, body) VALUES ($1, $2, $3)
		RETURNING id, language_code, index_tags, body
	`, languageCode, canonical, body).Scan(&t.ID, &t.LanguageCode, &t.IndexTags, &t.Body)
	if isUniqueViolation(err) {
		return Template{}, ErrAlreadyExists
	}
	return t, err
}

func (s *Service) ListTemplates(ctx context.Context, languageCode string) ([]Template, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, language_code, index_tags, body FROM inflection_templates
		WHERE language_code = $1 ORDER BY id
	`, languageCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Template, 0)
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.LanguageCode, &t.IndexTags, &t.Body); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) UpdateTemplate(ctx context.Context, languageCode string, id int64, indexTags []string, body string) (Template, error) {
	if strings.TrimSpace(body) == "" {
		return Template{}, fmt.Errorf("%w: body must not be empty", ErrValidation)
	}
	canonical, err := s.canonicalizeTags(ctx, languageCode, indexTags)
	if err != nil {
		return Template{}, err
	}
	var t Template
	err = s.db.QueryRow(ctx, `
		UPDATE inflection_templates SET index_tags = $1, body = $2
		WHERE id = $3 AND language_code = $4
		RETURNING id, language_code, index_tags, body
	`, canonical, body, id, languageCode).Scan(&t.ID, &t.LanguageCode, &t.IndexTags, &t.Body)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Template{}, ErrNotFound
	case isUniqueViolation(err):
		return Template{}, ErrAlreadyExists
	}
	return t, err
}

func (s *Service) DeleteTemplate(ctx context.Context, languageCode string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM inflection_templates WHERE id = $1 AND language_code = $2`, id, languageCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Forms ────────────────────────────────────────────────────────────────────

type FormInput struct {
	Text string   `json:"text"`
	Tags []string `json:"tags"`
}

// Re-saving the same tag combination updates its text rather than erroring —
// bulk loading is expected to be safe to rerun.
func (s *Service) UpsertForms(ctx context.Context, languageCode string, inputs []FormInput) ([]Form, error) {
	sch, err := s.languages.GetSchema(ctx, languageCode)
	if err != nil {
		return nil, err
	}

	type prepared struct {
		text string
		tags []string
	}
	batch := make([]prepared, 0, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.Text) == "" || strings.ContainsAny(in.Text, "\r\n") {
			return nil, fmt.Errorf("%w: text must be a non-empty single line", ErrValidation)
		}
		_, canonical, err := sch.ValidateAndCanonicalizeTags(in.Tags)
		if err != nil {
			return nil, err
		}
		batch = append(batch, prepared{text: in.Text, tags: canonical})
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if Commit already succeeded

	out := make([]Form, 0, len(batch))
	for _, p := range batch {
		var f Form
		err := tx.QueryRow(ctx, `
			INSERT INTO inflection_forms (language_code, tags, text) VALUES ($1, $2, $3)
			ON CONFLICT (language_code, tags) DO UPDATE SET text = EXCLUDED.text
			RETURNING id, language_code, tags, text
		`, languageCode, p.tags, p.text).Scan(&f.ID, &f.LanguageCode, &f.Tags, &f.Text)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ListForms(ctx context.Context, languageCode string) ([]Form, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, language_code, tags, text FROM inflection_forms
		WHERE language_code = $1 ORDER BY id
	`, languageCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Form, 0)
	for rows.Next() {
		var f Form
		if err := rows.Scan(&f.ID, &f.LanguageCode, &f.Tags, &f.Text); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Service) DeleteForm(ctx context.Context, languageCode string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM inflection_forms WHERE id = $1 AND language_code = $2`, id, languageCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Rendering ────────────────────────────────────────────────────────────────

var placeholderPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// Templates with no matching form data at all are silently skipped.
func (s *Service) RenderTables(ctx context.Context, languageCode string, requestTags []string) ([]RenderedTable, error) {
	sch, err := s.languages.GetSchema(ctx, languageCode)
	if err != nil {
		return nil, err
	}
	_, canonicalRequest, err := sch.ValidateAndCanonicalizeTags(requestTags)
	if err != nil {
		return nil, err
	}

	templates, err := s.ListTemplates(ctx, languageCode)
	if err != nil {
		return nil, err
	}
	forms, err := s.ListForms(ctx, languageCode)
	if err != nil {
		return nil, err
	}

	out := make([]RenderedTable, 0)
	for _, t := range templates {
		if !isSubset(canonicalRequest, t.IndexTags) {
			continue
		}
		var matching []Form
		for _, f := range forms {
			if isSubset(t.IndexTags, f.Tags) {
				matching = append(matching, f)
			}
		}
		if len(matching) == 0 {
			continue
		}
		out = append(out, RenderedTable{
			IndexTags: t.IndexTags,
			Markdown:  render(t.Body, t.IndexTags, matching),
		})
	}
	return out, nil
}

// render fills every "{tag}" placeholder in body with the text of the one
// form (among matching, which all already satisfy indexTags ⊆ form.Tags)
// whose remaining tags (form.Tags minus indexTags) contain that tag value.
// A placeholder with no matching form renders as empty. If more than one
// form matches the same placeholder (ambiguous data), the first one found
// wins — a data-authoring problem to fix upstream, not something to error on.
func render(body string, indexTags []string, matching []Form) string {
	return placeholderPattern.ReplaceAllStringFunc(body, func(m string) string {
		token := m[1 : len(m)-1]
		for _, f := range matching {
			if slices.Contains(remainder(f.Tags, indexTags), token) {
				return f.Text
			}
		}
		return ""
	})
}

func isSubset(a, b []string) bool {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	for _, v := range a {
		if !set[v] {
			return false
		}
	}
	return true
}

func remainder(tags, exclude []string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, v := range exclude {
		skip[v] = true
	}
	out := make([]string, 0, len(tags))
	for _, v := range tags {
		if !skip[v] {
			out = append(out, v)
		}
	}
	return out
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
