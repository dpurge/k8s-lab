-- Initial dictionary schema: 7 tables + reference-table seeds.
-- Applied exactly once by `just migrate-db` (FR-MIG-001).
-- Never edited after being applied; later changes go in 0002_*.sql (NFR-EXT-002).
--
-- Table creation order respects FK dependencies:
--   script → language → phrase, translation → dictionary → dictionary_tag
--   grammar_tag → dictionary_tag
--
-- No CREATE EXTENSION: gen_random_uuid() is core in Postgres 13+; we are on 18.
-- No ENUM types: controlled vocabularies are tables of rows (NFR-EXT-001, NFR-EXT-002).

-- ── Reference tables ──────────────────────────────────────────────────────────

-- Writing scripts (ISO 15924). DATA-DICT-005.
-- Extensible: INSERT a new row; no migration needed. `text` avoids char(n) padding.
CREATE TABLE script (
    code text PRIMARY KEY,
    CONSTRAINT script_code_iso15924 CHECK (code ~ '^[a-z]{4}$')
);

-- Language-script combinations. DATA-DICT-006.
-- `code` is the natural primary key (e.g. 'cmn-hans').
-- `language_code_derived` is a plain CHECK, not GENERATED STORED — keeps
-- the column usable as PK without relying on generated-column-in-PK support.
CREATE TABLE language (
    code          text PRIMARY KEY,
    language_code text NOT NULL,
    script_code   text NOT NULL REFERENCES script(code),
    CONSTRAINT language_lang_iso639  CHECK (language_code ~ '^[a-z]{3}$'),
    CONSTRAINT language_code_derived CHECK (code = language_code || '-' || script_code),
    CONSTRAINT language_pair_uq      UNIQUE (language_code, script_code)
);

-- Grammatical tags. DATA-DICT-007.
-- Extensible: INSERT a new row; no migration needed.
CREATE TABLE grammar_tag (
    code text PRIMARY KEY,
    CONSTRAINT grammar_tag_code_fmt CHECK (code ~ '^[a-z][a-z_-]{1,31}$')
);

-- ── Content tables ────────────────────────────────────────────────────────────

-- Phrases, uniquely identified by (language, text). DATA-DICT-001, 004, 009.
-- `transcription` is nullable and excluded from the unique key (DATA-DICT-009).
-- `language` FK ensures only known language-scripts are accepted (DATA-DICT-004).
CREATE TABLE phrase (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    language_script text        NOT NULL REFERENCES language(code),
    text            text        NOT NULL,
    transcription   text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT phrase_text_not_blank          CHECK (btrim(text) <> ''),
    CONSTRAINT phrase_transcription_not_blank CHECK (transcription IS NULL OR btrim(transcription) <> ''),
    CONSTRAINT phrase_language_text_uq        UNIQUE (language_script, text)
);

-- Translations, uniquely identified by (language_script, text). DATA-DICT-001, 004.
CREATE TABLE translation (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    language_script text        NOT NULL REFERENCES language(code),
    text            text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT translation_text_not_blank   CHECK (btrim(text) <> ''),
    CONSTRAINT translation_language_text_uq UNIQUE (language_script, text)
);

-- Dictionary entries linking a Phrase to a Translation. DATA-DICT-002.
-- Composite PK (phrase_id, translation_id) is both the uniqueness constraint and
-- the surrogate key for FR-API-008/009.
-- ON DELETE RESTRICT on translation_id is the backstop preventing a Translation
-- from being deleted while a Dictionary entry still references it (DATA-DICT-003).
CREATE TABLE dictionary (
    phrase_id      uuid        NOT NULL REFERENCES phrase(id)      ON DELETE CASCADE,
    translation_id uuid        NOT NULL REFERENCES translation(id) ON DELETE RESTRICT,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dictionary_pkey PRIMARY KEY (phrase_id, translation_id)
);

-- Grammatical tags attached to a Dictionary entry (0..n). DATA-DICT-008.
-- ON DELETE CASCADE: deleting a Dictionary entry drops its dictionary_tag rows automatically.
-- Composite FK to dictionary prevents orphaned dictionary_tag rows.
CREATE TABLE dictionary_tag (
    phrase_id      uuid NOT NULL,
    translation_id uuid NOT NULL,
    tag_code       text NOT NULL REFERENCES grammar_tag(code),
    CONSTRAINT dictionary_tag_pkey PRIMARY KEY (phrase_id, translation_id, tag_code),
    CONSTRAINT dictionary_tag_dictionary_fk FOREIGN KEY (phrase_id, translation_id)
        REFERENCES dictionary (phrase_id, translation_id) ON DELETE CASCADE
);

-- Supports the orphan check (WHERE translation_id = $1) — the PK leads on phrase_id.
CREATE INDEX dictionary_translation_id_idx ON dictionary     (translation_id);
-- Supports "entries by tag" and FK checks from dictionary_tag.tag_code.
CREATE INDEX dictionary_tag_tag_code_idx   ON dictionary_tag (tag_code);

-- ── Reference-table seeds ─────────────────────────────────────────────────────
-- FR-MIG-002, DATA-DICT-005, DATA-DICT-006, DATA-DICT-007.
-- Plain INSERT (not ON CONFLICT): migration runs exactly once against a fresh DB.
-- Do not extend these sets — the counts (7/9/4) are normative; T-6.10 asserts them.
--
-- cyrl, grek, hant, hebr are seeded scripts with no seeded language pair —
-- that is intentional per DATA-DICT-005/006; do not "reconcile" the two sets.

INSERT INTO script (code) VALUES
    ('latn'), ('cyrl'), ('grek'), ('hans'), ('hant'), ('arab'), ('hebr');

INSERT INTO language (code, language_code, script_code) VALUES
    ('pol-latn', 'pol', 'latn'),
    ('eng-latn', 'eng', 'latn'),
    ('fra-latn', 'fra', 'latn'),
    ('deu-latn', 'deu', 'latn'),
    ('arb-arab', 'arb', 'arab'),
    ('cmn-hans', 'cmn', 'hans'),
    ('spa-latn', 'spa', 'latn'),
    ('ita-latn', 'ita', 'latn'),
    ('tur-latn', 'tur', 'latn');

INSERT INTO grammar_tag (code) VALUES
    ('noun'), ('verb'), ('adjective'), ('adverb');
