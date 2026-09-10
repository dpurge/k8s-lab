-- dictionary service schema.
--
-- Step 1: no tables (health/readiness + deploy plumbing only).
-- Step 2 (this section): users, per-language role grants, and short-lived
-- bearer tokens. Languages/grammar schema, entries/translations/notes, and
-- inflection templates/forms land in later steps.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per (user, role, language) grant. "admin" is global — language_code
-- is NULL and grants every permission everywhere. "writer"/"reader" always
-- name a language. language_code deliberately has no foreign key to
-- languages(code): a grant's lifecycle is independent of the language row
-- (e.g. granting a role before the language exists, or a language being
-- removed without silently deleting historical grants). Validated at the
-- application layer instead (internal/languages).
CREATE TABLE IF NOT EXISTS role_grants (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'writer', 'reader')),
    language_code TEXT,
    CHECK ((role = 'admin') = (language_code IS NULL)),
    UNIQUE (user_id, role, language_code)
);

-- Short-lived bearer tokens minted by POST /auth/token. Only a sha256 hash of
-- the token is stored — the raw value is returned to the caller once and
-- never persisted. role/language_code are a snapshot of the grant the token
-- was minted against, so a later role change doesn't retroactively alter an
-- already-issued token (it simply isn't renewed past expiry).
CREATE TABLE IF NOT EXISTS tokens (
    id            BIGSERIAL PRIMARY KEY,
    token_hash    TEXT NOT NULL UNIQUE,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'writer', 'reader')),
    language_code TEXT,
    scope         TEXT NOT NULL CHECK (scope IN ('write', 'read')),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked       BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tokens_expires_at_idx ON tokens (expires_at);

-- Step 3 (this section): languages and each language's own grammar schema
-- (categories + word-class templates), admin-managed. Entries/translations/
-- notes and inflection templates/forms land in later steps.

CREATE TABLE IF NOT EXISTS languages (
    code              TEXT PRIMARY KEY CHECK (code ~ '^[a-z]{3}$'),
    name              TEXT NOT NULL,
    is_public         BOOLEAN NOT NULL DEFAULT false,
    has_transcription BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per grammatical category a language defines (e.g. "word_class" ->
-- {V,N,...}, "gender" -> {m,f,n}). tag_values is the closed set of tags that
-- category may take; format enforced in Go (case-sensitive, mixed case
-- allowed, e.g. "V" and "v" are different tags) and cross-category tag-value
-- uniqueness enforced in Go too (a tag value unambiguously names one category).
CREATE TABLE IF NOT EXISTS grammar_categories (
    id            BIGSERIAL PRIMARY KEY,
    language_code TEXT NOT NULL REFERENCES languages(code) ON DELETE CASCADE,
    key           TEXT NOT NULL,
    tag_values    TEXT[] NOT NULL,
    UNIQUE (language_code, key)
);

-- One row per word class, naming the OTHER categories an entry of that word
-- class may carry (the "allowed ceiling" — an entry may use any subset, none
-- are mandatory). word_class itself must be a tag_values member of that
-- language's "word_class" category (checked in Go, not the database, since
-- Postgres CHECK constraints can't look across tables).
CREATE TABLE IF NOT EXISTS grammar_templates (
    id                 BIGSERIAL PRIMARY KEY,
    language_code      TEXT NOT NULL REFERENCES languages(code) ON DELETE CASCADE,
    word_class         TEXT NOT NULL,
    allowed_categories TEXT[] NOT NULL,
    UNIQUE (language_code, word_class)
);

-- Step 4 (this section): dictionary entries, their translations, and notes.
-- Inflection templates/forms land in a later step.

-- Identity is (language_code, phrase, tags): the same phrase with different
-- grammar tags is a different entry. tags is stored in the canonical order
-- languages.Schema.ValidateAndCanonicalizeTags produces (word_class tag
-- first, then the rest sorted by category), so two requests naming the same
-- tag set in different orders collide on this UNIQUE constraint as intended.
CREATE TABLE IF NOT EXISTS entries (
    id            BIGSERIAL PRIMARY KEY,
    language_code TEXT NOT NULL REFERENCES languages(code) ON DELETE CASCADE,
    phrase        TEXT NOT NULL CHECK (phrase <> '' AND phrase !~ '[\r\n]'),
    tags          TEXT[] NOT NULL,
    UNIQUE (language_code, phrase, tags)
);

-- One entry may have multiple translations, including more than one into the
-- same target language (synonyms). notes is nullable markdown, specific to
-- this one (entry, translation) pair — a different translation of the same
-- entry has its own, independent notes.
CREATE TABLE IF NOT EXISTS translations (
    id            BIGSERIAL PRIMARY KEY,
    entry_id      BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    language_code TEXT NOT NULL REFERENCES languages(code),
    text          TEXT NOT NULL CHECK (text <> '' AND text !~ '[\r\n]'),
    notes         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entry_id, language_code, text)
);

-- Step 5 (this section, final content piece): inflection templates + forms.
-- index_tags/tags are canonicalized and validated the same way as
-- entries.tags (internal/languages.Schema.ValidateAndCanonicalizeTags) — see
-- internal/inflection for the subset-matching + placeholder-rendering logic.

-- A template's markdown body (e.g. "{1} {2} {3}") names, per word class, one
-- rendering for a specific slice of the paradigm (e.g. {V,praes} = present
-- tense). index_tags is the identifying tag set; placeholders in body are
-- filled from inflection_forms whose tags are a superset of index_tags.
CREATE TABLE IF NOT EXISTS inflection_templates (
    id            BIGSERIAL PRIMARY KEY,
    language_code TEXT NOT NULL REFERENCES languages(code) ON DELETE CASCADE,
    index_tags    TEXT[] NOT NULL,
    body          TEXT NOT NULL CHECK (body <> ''),
    UNIQUE (language_code, index_tags)
);

-- One concrete wordform, e.g. text="am", tags={V,praes,sg,1}. Not tied to any
-- one dictionary entry — matched to a template (and thus to whichever entries
-- share its word class) purely by tag-set membership.
CREATE TABLE IF NOT EXISTS inflection_forms (
    id            BIGSERIAL PRIMARY KEY,
    language_code TEXT NOT NULL REFERENCES languages(code) ON DELETE CASCADE,
    tags          TEXT[] NOT NULL,
    text          TEXT NOT NULL CHECK (text <> '' AND text !~ '[\r\n]'),
    UNIQUE (language_code, tags)
);
