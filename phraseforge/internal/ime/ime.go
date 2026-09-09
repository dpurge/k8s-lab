package ime

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Preset is one selectable IME, e.g. "rus" / "Russian" / script "cyrl".
type Preset struct {
	Code   string
	Name   string
	Script string
}

// Config is one (language, script) pair's IME assignment. Each *IME field is
// "" when that role isn't configured yet. NeedsTranscription records whether
// this pair's script is opaque enough to need a romanization at all — set
// independently of whether a transcription IME has actually been chosen.
type Config struct {
	Language           string
	Script             string
	SourceIME          string
	TranscriptionIME   string
	NeedsTranscription bool
}

func ListPresets(ctx context.Context, db *pgxpool.Pool) ([]Preset, error) {
	rows, err := db.Query(ctx, `SELECT code, name, script FROM ime ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Preset
	for rows.Next() {
		var p Preset
		if err := rows.Scan(&p.Code, &p.Name, &p.Script); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const configCols = `language, script, coalesce(source_ime, ''), coalesce(transcription_ime, ''), needs_transcription`

func scanConfig(row interface{ Scan(...any) error }, c *Config) error {
	return row.Scan(&c.Language, &c.Script, &c.SourceIME, &c.TranscriptionIME, &c.NeedsTranscription)
}

// ListConfigs returns every configured (language, script) pair, for the
// admin page's existing-configuration table.
func ListConfigs(ctx context.Context, db *pgxpool.Pool) ([]Config, error) {
	rows, err := db.Query(ctx, `SELECT `+configCols+` FROM ime_config ORDER BY language, script`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Config
	for rows.Next() {
		var c Config
		if err := scanConfig(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetConfig creates or replaces the IME assignment for one (language,
// script) pair. Pass "" for any role that should be left unconfigured.
func SetConfig(ctx context.Context, db *pgxpool.Pool, c Config) error {
	toNull := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err := db.Exec(ctx, `
		INSERT INTO ime_config (language, script, source_ime, transcription_ime, needs_transcription)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (language, script) DO UPDATE SET
			source_ime = EXCLUDED.source_ime,
			transcription_ime = EXCLUDED.transcription_ime,
			needs_transcription = EXCLUDED.needs_transcription`,
		c.Language, c.Script, toNull(c.SourceIME), toNull(c.TranscriptionIME), c.NeedsTranscription)
	return err
}

// GetConfig looks up one (language, script) pair's configuration — the
// editor's live IME-wiring endpoint uses this. found is false (zero Config,
// nil error) if the pair has never been configured.
func GetConfig(ctx context.Context, db *pgxpool.Pool, language, script string) (Config, bool, error) {
	var c Config
	err := scanConfig(db.QueryRow(ctx, `SELECT `+configCols+` FROM ime_config WHERE language = $1 AND script = $2`, language, script), &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// DeleteConfig removes one (language, script) pair's configuration entirely.
func DeleteConfig(ctx context.Context, db *pgxpool.Pool, language, script string) error {
	_, err := db.Exec(ctx, `DELETE FROM ime_config WHERE language = $1 AND script = $2`, language, script)
	return err
}
