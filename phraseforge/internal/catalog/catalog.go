package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Language struct {
	Code string
	ISO1 string
	Name string
}

type Script struct {
	Code      string
	Name      string
	Direction string // "ltr" or "rtl"
	Enlarged  bool
}

func ListLanguages(ctx context.Context, db *pgxpool.Pool) ([]Language, error) {
	rows, err := db.Query(ctx, `SELECT code, coalesce(iso1, ''), name FROM language ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Language
	for rows.Next() {
		var l Language
		if err := rows.Scan(&l.Code, &l.ISO1, &l.Name); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func ListScripts(ctx context.Context, db *pgxpool.Pool) ([]Script, error) {
	rows, err := db.Query(ctx, `SELECT code, name, direction, enlarged FROM script ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Script
	for rows.Next() {
		var s Script
		if err := rows.Scan(&s.Code, &s.Name, &s.Direction, &s.Enlarged); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetScript looks up one script's direction/enlarged treatment for rendering.
func GetScript(ctx context.Context, db *pgxpool.Pool, code string) (Script, error) {
	var s Script
	err := db.QueryRow(ctx, `SELECT code, name, direction, enlarged FROM script WHERE code = $1`, code).
		Scan(&s.Code, &s.Name, &s.Direction, &s.Enlarged)
	return s, err
}

// GetScriptIfExists is GetScript but reports found=false instead of erroring
// when code doesn't match any row (e.g. an editor's script field is empty or
// stale before the first real selection) — callers that want a safe default
// rather than a 500 use this.
func GetScriptIfExists(ctx context.Context, db *pgxpool.Pool, code string) (Script, bool, error) {
	s, err := GetScript(ctx, db, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return Script{}, false, nil
	}
	if err != nil {
		return Script{}, false, err
	}
	return s, true, nil
}
