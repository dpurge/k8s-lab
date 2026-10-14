-- phraseforge schema, applied by `phraseforge migrate`. Every statement is safe
-- to rerun. The tables are in their final shape; the only migration steps are
-- the two DROPs at the end of the file, which can go once they have run
-- everywhere.

CREATE TABLE IF NOT EXISTS users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    locale        text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'pl')) -- site UI language, a per-user preference
);

-- ── Language / script catalog ───────────────────────────────────────────────
-- Controlled vocabularies as tables of rows, not enums (extensible: INSERT a
-- new row, no migration needed). Seeded from cli-tools' pkg/catalog/catalog.yml,
-- the source of truth this project shares, so PhraseForge and cli-tools never
-- disagree on what a language/script code means.

CREATE TABLE IF NOT EXISTS language (
    code text PRIMARY KEY, -- ISO 639-3
    iso1 text,             -- ISO 639-1 (bare HTML `lang` subtag); not every ISO 639-3 code has one
    name text NOT NULL,
    CONSTRAINT language_code_iso6393 CHECK (code ~ '^[a-z]{3}$')
);

CREATE TABLE IF NOT EXISTS script (
    code      text PRIMARY KEY, -- ISO 15924
    name      text NOT NULL,
    direction text NOT NULL CHECK (direction IN ('ltr', 'rtl')),
    enlarged  boolean NOT NULL DEFAULT false, -- script needs a larger base font (see cli-tools catalog.yml)
    CONSTRAINT script_code_iso15924 CHECK (code ~ '^[a-z]{4}$')
);

INSERT INTO language (code, iso1, name) VALUES
    ('ajp', 'ar', 'South Levantine Arabic'),
    ('apc', 'ar', 'North Levantine Arabic'),
    ('arb', 'ar', 'Standard Arabic'),
    ('bul', 'bg', 'Bulgarian'),
    ('ces', 'cs', 'Czech'),
    ('cmn', 'zh', 'Mandarin Chinese'),
    ('dan', 'da', 'Danish'),
    ('deu', 'de', 'German'),
    ('ell', 'el', 'Modern Greek'),
    ('eng', 'en', 'English'),
    ('fas', 'fa', 'Persian'),
    ('fin', 'fi', 'Finnish'),
    ('fra', 'fr', 'French'),
    ('grc', 'el', 'Ancient Greek'),
    ('heb', 'he', 'Hebrew'),
    ('hin', 'hi', 'Hindi'),
    ('ind', 'id', 'Indonesian'),
    ('ita', 'it', 'Italian'),
    ('jpn', 'ja', 'Japanese'),
    ('kaz', 'kk', 'Kazakh'),
    ('kor', 'ko', 'Korean'),
    ('lat', 'la', 'Latin'),
    ('lit', 'lt', 'Lithuanian'),
    ('mon', 'mn', 'Mongolian'),
    ('nld', 'nl', 'Dutch'),
    ('pol', 'pl', 'Polish'),
    ('ron', 'ro', 'Romanian'),
    ('spa', 'es', 'Spanish'),
    ('srp', 'sr', 'Serbian'),
    ('swa', 'sw', 'Swahili'),
    ('swe', 'sv', 'Swedish'),
    ('tgk', 'tg', 'Tajik'),
    ('tgl', 'tl', 'Tagalog'),
    ('tha', 'th', 'Thai'),
    ('tur', 'tr', 'Turkish'),
    ('uig', 'ug', 'Uyghur'),
    ('ukr', 'uk', 'Ukrainian'),
    ('uzb', 'uz', 'Uzbek'),
    ('vie', 'vi', 'Vietnamese'),
    ('yid', 'yi', 'Yiddish'),
    ('yue', 'zh', 'Cantonese')
ON CONFLICT (code) DO NOTHING;

INSERT INTO script (code, name, direction, enlarged) VALUES
    ('latn', 'Latin', 'ltr', false),
    ('cyrl', 'Cyrillic', 'ltr', false),
    ('hans', 'Han (Simplified)', 'ltr', true),
    ('hant', 'Han (Traditional)', 'ltr', true),
    ('hani', 'Han (script unspecified)', 'ltr', true),
    ('arab', 'Arabic', 'rtl', true),
    ('hebr', 'Hebrew', 'rtl', true),
    ('kore', 'Korean (Hangul + Hanja)', 'ltr', true),
    ('hang', 'Hangul', 'ltr', true),
    ('jpan', 'Japanese (Han + Kana)', 'ltr', true),
    ('hira', 'Hiragana', 'ltr', true),
    ('kana', 'Katakana', 'ltr', true),
    ('syrc', 'Syriac', 'rtl', true)
ON CONFLICT (code) DO NOTHING;

-- Not in cli-tools' catalog.yml: needed by the Hindi, Greek and Georgian IME presets.
INSERT INTO script (code, name, direction, enlarged) VALUES
    ('grek', 'Greek', 'ltr', false),
    ('deva', 'Devanagari', 'ltr', false),
    ('geor', 'Georgian', 'ltr', false)
ON CONFLICT (code) DO NOTHING;

CREATE TABLE IF NOT EXISTS texts (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         text NOT NULL,
    body          text NOT NULL, -- markdown source (PhraseForge extensions: vocabulary/dialog blocks)
    language      text NOT NULL REFERENCES language(code),
    script        text NOT NULL REFERENCES script(code),
    transcription text,
    ingest_source text, -- URL or original filename the text was ingested from; NULL otherwise
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ── Roles ────────────────────────────────────────────────────────────────────
-- admin: site-wide, can do everything. teacher: scoped to one language, can
-- create/edit texts in it. student: scoped to one language, read/browse only.
-- A user can hold different roles in different languages — user_role is a
-- many-to-many grant, not a single column on users.

CREATE TABLE IF NOT EXISTS role (
    code text PRIMARY KEY,
    name text NOT NULL
);

INSERT INTO role (code, name) VALUES
    ('admin', 'Admin'),
    ('teacher', 'Teacher'),
    ('student', 'Student')
ON CONFLICT (code) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_role (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id  bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     text NOT NULL REFERENCES role(code),
    language text REFERENCES language(code), -- NULL for admin (site-wide); required for teacher/student
    CONSTRAINT user_role_scope_check CHECK (
        (role = 'admin' AND language IS NULL) OR
        (role IN ('teacher', 'student') AND language IS NOT NULL)
    )
);

-- Partial unique indexes instead of a plain UNIQUE constraint: Postgres treats
-- every NULL as distinct, so a plain UNIQUE(user_id, role, language) would let
-- the same user be granted 'admin' any number of times (language always NULL).
CREATE UNIQUE INDEX IF NOT EXISTS user_role_unique_scoped
    ON user_role (user_id, role, language) WHERE language IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS user_role_unique_admin
    ON user_role (user_id, role) WHERE language IS NULL;

-- ── Tags ─────────────────────────────────────────────────────────────────────
-- One tag table shared by every taggable resource, plus a polymorphic join
-- (resource_type + resource_id). resource_id can't carry a real foreign key
-- since it points at a different table per resource_type, so a resource's own
-- store must delete its taggings (internal/tags).
CREATE TABLE IF NOT EXISTS tag (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE -- stored lowercase/trimmed by internal/tags; enforced in Go, not a CHECK
);

CREATE TABLE IF NOT EXISTS tagging (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tag_id        bigint NOT NULL REFERENCES tag(id) ON DELETE CASCADE,
    resource_type text NOT NULL, -- 'text', 'dialog', 'vocabulary', 'models'
    resource_id   bigint NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS tagging_unique ON tagging (tag_id, resource_type, resource_id);
CREATE INDEX IF NOT EXISTS tagging_resource_idx ON tagging (resource_type, resource_id);

-- ── IME (Input Method Editor) catalog + per-language/script configuration ──
CREATE TABLE IF NOT EXISTS ime (
    code   text PRIMARY KEY, -- deno-app's ime/<code>.json filename stem
    name   text NOT NULL,
    script text NOT NULL REFERENCES script(code)
);

INSERT INTO ime (code, name, script) VALUES
    ('arb', 'Arabic', 'arab'),
    ('bel', 'Belarusian', 'cyrl'),
    ('bul', 'Bulgarian', 'cyrl'),
    ('ces', 'Czech', 'latn'),
    ('cmn', 'Chinese', 'hans'),
    ('cxs', 'IPA', 'latn'),
    ('dan', 'Danish', 'latn'),
    ('ell', 'Modern Greek', 'grek'),
    ('fas', 'Farsi', 'arab'),
    ('grc', 'Ancient Greek', 'grek'),
    ('heb', 'Hebrew', 'hebr'),
    ('hin', 'Devanagari', 'deva'),
    ('hrv', 'Croatian', 'latn'),
    ('hun', 'Hungarian', 'latn'),
    ('kat', 'Georgian', 'geor'),
    ('kaz', 'Kazakh', 'cyrl'),
    ('kor', 'Korean', 'kore'),
    ('kur', 'Kurdish', 'latn'),
    ('lat', 'Latin', 'latn'),
    ('lav', 'Latvian', 'latn'),
    ('lit', 'Lithuanian', 'latn'),
    ('ron', 'Romanian', 'latn'),
    ('rus', 'Russian', 'cyrl'),
    ('semitic', 'Semitic', 'latn'),
    ('spa', 'Spanish', 'latn'),
    ('sqi', 'Albanian', 'latn'),
    ('tat', 'Tatar', 'cyrl'),
    ('tgk', 'Tajik', 'cyrl'),
    ('tuk', 'Turkmen', 'latn'),
    ('tur', 'Turkish', 'latn'),
    ('turkic', 'Turkic', 'latn'),
    ('uig', 'Uighur', 'arab'),
    ('ukr', 'Ukrainian', 'cyrl'),
    ('vie', 'Vietnamese', 'latn'),
    ('yid', 'Yiddish', 'hebr')
ON CONFLICT (code) DO NOTHING;

-- One row per (language, script) pair this project uses for texts: which
-- catalog IME types the source text and which the transcription (both
-- nullable), and whether the pair needs a transcription at all (Arabic and
-- Mandarin do; Romanian in Latin script does not).
CREATE TABLE IF NOT EXISTS ime_config (
    language            text NOT NULL REFERENCES language(code),
    script              text NOT NULL REFERENCES script(code),
    source_ime          text REFERENCES ime(code),
    transcription_ime   text REFERENCES ime(code),
    needs_transcription boolean NOT NULL DEFAULT false,
    PRIMARY KEY (language, script)
);

-- ── Translations ─────────────────────────────────────────────────────────────
-- Stored per viewer site locale and per resource, with the same resource_type +
-- resource_id polymorphism as tagging: a resource can accumulate an English
-- translation, a Polish one, both, or neither.
CREATE TABLE IF NOT EXISTS resource_translation (
    resource_type text NOT NULL, -- 'text' or 'dialog'
    resource_id   bigint NOT NULL,
    locale        text NOT NULL CHECK (locale IN ('en', 'pl')),
    body          text NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, locale)
);

-- ── Dialogs ──────────────────────────────────────────────────────────────────
-- Same shape as texts, but body holds {start-dialog}/{end-dialog} markdown.
CREATE TABLE IF NOT EXISTS dialogs (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         text NOT NULL,
    body          text NOT NULL, -- markdown source (PhraseForge {start-dialog} blocks)
    transcription text,
    language      text NOT NULL REFERENCES language(code),
    script        text NOT NULL REFERENCES script(code),
    ingest_source text, -- URL or original filename the dialog was ingested from; NULL otherwise
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ── Vocabulary lists ─────────────────────────────────────────────────────────
-- phrase/grammar/transcription are common to every viewer; translation and
-- notes are per viewer locale. A list links to shared phrases through
-- vocabulary_items (list_id, position, phrase_id); position is the item's place
-- in its list (deleting a middle item renumbers the later ones), id is a stable
-- identity for background jobs. source_text_id / source_dialog_id link a list to
-- the text or dialog it was generated from, so a rerun replaces its own list's
-- items; deleting the source never deletes the list.
CREATE TABLE IF NOT EXISTS vocabulary_lists (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title            text NOT NULL,
    language         text NOT NULL REFERENCES language(code),
    script           text NOT NULL REFERENCES script(code),
    source_text_id   bigint REFERENCES texts(id) ON DELETE SET NULL,
    source_dialog_id bigint REFERENCES dialogs(id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT vocabulary_lists_single_source_check CHECK (source_text_id IS NULL OR source_dialog_id IS NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS vocabulary_lists_source_text_id_idx ON vocabulary_lists (source_text_id) WHERE source_text_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS vocabulary_lists_source_dialog_id_idx ON vocabulary_lists (source_dialog_id) WHERE source_dialog_id IS NOT NULL;

-- A phrase is stored once per (language, script, phrase, grammar, transcription)
-- and linked from any number of lists, so a phrase translated in one list needs
-- no translation job in another. grammar/transcription are '' (not NULL) when
-- absent, so the UNIQUE key needs no NULL handling.
CREATE TABLE IF NOT EXISTS phrases (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    language      text NOT NULL REFERENCES language(code),
    script        text NOT NULL REFERENCES script(code),
    phrase        text NOT NULL,
    grammar       text NOT NULL DEFAULT '',
    transcription text NOT NULL DEFAULT '',
    UNIQUE (language, script, phrase, grammar, transcription)
);

CREATE TABLE IF NOT EXISTS phrase_translation (
    phrase_id   bigint NOT NULL REFERENCES phrases(id) ON DELETE CASCADE,
    locale      text NOT NULL CHECK (locale IN ('en', 'pl')),
    translation text,
    notes       text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (phrase_id, locale)
);

-- Short usage examples of a phrase, shared by every list that links it. The
-- limits are checked by vocabulary.Store.SetItemExamples; the constraints are
-- the backstop.
CREATE TABLE IF NOT EXISTS phrase_examples (
    phrase_id bigint NOT NULL REFERENCES phrases(id) ON DELETE CASCADE,
    position  integer NOT NULL CHECK (position BETWEEN 0 AND 4),
    example   text NOT NULL CHECK (btrim(example) <> '' AND char_length(example) <= 80),
    PRIMARY KEY (phrase_id, position)
);

CREATE TABLE IF NOT EXISTS vocabulary_items (
    list_id   bigint NOT NULL REFERENCES vocabulary_lists(id) ON DELETE CASCADE,
    position  integer NOT NULL,
    phrase_id bigint NOT NULL REFERENCES phrases(id),
    id        bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (list_id, position)
);
CREATE UNIQUE INDEX IF NOT EXISTS vocabulary_items_id_idx ON vocabulary_items (id);

-- A phrase no list links to is garbage: delete it (and, by cascade, its
-- translations) whenever links go away — a single item removed, a whole list
-- deleted, or a sync dropping a line. A statement-level trigger covers every
-- path, including list-deletion cascades that never run Go code. It fires on
-- DELETEd links only: code that re-points a link (UPDATE of phrase_id — a
-- merge in retargetPhrase, a language change in UpdateMeta) must delete the
-- phrase it left behind itself.
CREATE OR REPLACE FUNCTION delete_orphan_phrases() RETURNS trigger AS $$
BEGIN
    DELETE FROM phrases p
    WHERE p.id IN (SELECT phrase_id FROM removed_links)
      AND NOT EXISTS (SELECT 1 FROM vocabulary_items i WHERE i.phrase_id = p.id);
    RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS vocabulary_items_orphan_phrases ON vocabulary_items;
CREATE TRIGGER vocabulary_items_orphan_phrases
    AFTER DELETE ON vocabulary_items
    REFERENCING OLD TABLE AS removed_links
    FOR EACH STATEMENT EXECUTE FUNCTION delete_orphan_phrases();

-- A word a user knows well, hidden for that user only. Keyed by the shared
-- phrase, so it is hidden in every list that contains it; the row goes with the
-- user or, when no list links the phrase any more, with the phrase.
CREATE TABLE IF NOT EXISTS vocabulary_hidden_phrases (
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phrase_id  bigint NOT NULL REFERENCES phrases(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, phrase_id)
);

-- ── Model lists ──────────────────────────────────────────────────────────────
-- cli-tools' {start-models} block: like vocabulary, but a phrase has no grammar
-- or notes. Same shape as the vocabulary tables above.
CREATE TABLE IF NOT EXISTS models_lists (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title            text NOT NULL,
    language         text NOT NULL REFERENCES language(code),
    script           text NOT NULL REFERENCES script(code),
    source_text_id   bigint REFERENCES texts(id) ON DELETE SET NULL,
    source_dialog_id bigint REFERENCES dialogs(id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT models_lists_single_source_check CHECK (source_text_id IS NULL OR source_dialog_id IS NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS models_lists_source_text_id_idx ON models_lists (source_text_id) WHERE source_text_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS models_lists_source_dialog_id_idx ON models_lists (source_dialog_id) WHERE source_dialog_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS models_phrases (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    language      text NOT NULL REFERENCES language(code),
    script        text NOT NULL REFERENCES script(code),
    phrase        text NOT NULL,
    transcription text NOT NULL DEFAULT '',
    UNIQUE (language, script, phrase, transcription)
);

CREATE TABLE IF NOT EXISTS models_phrase_translation (
    phrase_id   bigint NOT NULL REFERENCES models_phrases(id) ON DELETE CASCADE,
    locale      text NOT NULL CHECK (locale IN ('en', 'pl')),
    translation text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (phrase_id, locale)
);

CREATE TABLE IF NOT EXISTS models_items (
    list_id   bigint NOT NULL REFERENCES models_lists(id) ON DELETE CASCADE,
    position  integer NOT NULL,
    phrase_id bigint NOT NULL REFERENCES models_phrases(id),
    id        bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (list_id, position)
);
CREATE UNIQUE INDEX IF NOT EXISTS models_items_id_idx ON models_items (id);

-- Same orphan cleanup as vocabulary's — including that it fires on DELETEd
-- links only, so code that re-points a link deletes the phrase it left behind.
CREATE OR REPLACE FUNCTION delete_orphan_models_phrases() RETURNS trigger AS $$
BEGIN
    DELETE FROM models_phrases p
    WHERE p.id IN (SELECT phrase_id FROM removed_links)
      AND NOT EXISTS (SELECT 1 FROM models_items i WHERE i.phrase_id = p.id);
    RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS models_items_orphan_phrases ON models_items;
CREATE TRIGGER models_items_orphan_phrases
    AFTER DELETE ON models_items
    REFERENCING OLD TABLE AS removed_links
    FOR EACH STATEMENT EXECUTE FUNCTION delete_orphan_models_phrases();

-- ── LLM settings ─────────────────────────────────────────────────────────────
-- A row overrides only provider, model, think and timeout for one (kind, source
-- language, target language); the prompt text is the purpose default, and a
-- language's rules live in language_llm_sections. timeout_seconds NULL means
-- "inherit the purpose default".
CREATE TABLE IF NOT EXISTS llm_prompts (
    kind            text NOT NULL,
    source_language text NOT NULL,
    target_language text NOT NULL,
    provider        text NOT NULL DEFAULT 'ollama',
    model           text NOT NULL DEFAULT '',
    think           boolean NOT NULL DEFAULT false,
    timeout_seconds integer CHECK (timeout_seconds IS NULL OR (timeout_seconds > 0 AND timeout_seconds <= 3600)),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, source_language, target_language)
);

-- The kinds are kept outside CREATE TABLE so that adding one widens the check on
-- an existing database too (CREATE TABLE IF NOT EXISTS would leave the old one).
-- ai.ValidKinds is the list this must agree with.
ALTER TABLE llm_prompts DROP CONSTRAINT IF EXISTS llm_prompts_kind_check;
ALTER TABLE llm_prompts ADD CONSTRAINT llm_prompts_kind_check
    CHECK (kind IN ('translation', 'transcription', 'title', 'process_text', 'process_dialog', 'generate_vocabulary', 'generate_models', 'vocabulary_item', 'models_item', 'check_vocabulary', 'review_vocabulary', 'review_transcription', 'review_translation', 'review_models'));

-- Per-language snippets inserted where a prompt has {{grammarPrompt}},
-- {{transcriptionPrompt}} or {{dictionaryPrompt}}; blank means "absent".
CREATE TABLE IF NOT EXISTS language_llm_sections (
    language             text PRIMARY KEY REFERENCES language(code) ON DELETE CASCADE,
    grammar_prompt       text NOT NULL DEFAULT '',
    transcription_prompt text NOT NULL DEFAULT '',
    dictionary_prompt    text NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- ── Jobs ─────────────────────────────────────────────────────────────────────
-- One table for every submitted operation, interactive and background alike.
-- id is generated in Go (jobs.uuid): the project has no pgcrypto/uuid-ossp
-- extension, so there is no gen_random_uuid() to default to.
CREATE TABLE IF NOT EXISTS jobs (
    id         uuid PRIMARY KEY,
    kind       text NOT NULL,
    priority   text NOT NULL CHECK (priority IN ('interactive', 'background')),
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed', 'cancelled')),
    payload    jsonb NOT NULL DEFAULT '{}',
    result     jsonb,
    error      text,
    step       text, -- opaque per-kind progress marker (jobs.Service.SetStep): lets a Retry skip a step already done
    checkpoint jsonb, -- results of the chunks a job has finished, so a Retry resumes at the first unfinished chunk; cleared when done
    progress   text,  -- how far a chunked job has got, "3/12"; a failed job keeps it
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Exactly one job running app-wide: the single-worker, one-Ollama-call-at-a-time constraint.
CREATE UNIQUE INDEX IF NOT EXISTS jobs_one_running_idx ON jobs ((true)) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (created_at ASC) WHERE status = 'pending';

-- ── Reviews ──────────────────────────────────────────────────────────────────
-- The latest review of a vocabulary item, one row per item id (a new review
-- replaces it). findings are the model's proposals, each with its own status
-- (open, applied, dismissed); response is the model's reply as it came; phrase is
-- the phrase that was reviewed: a proposal is only applied while the item still
-- has it.
CREATE TABLE IF NOT EXISTS vocabulary_reviews (
    item_id    bigint PRIMARY KEY REFERENCES vocabulary_items(id) ON DELETE CASCADE,
    phrase     text NOT NULL,
    findings   jsonb NOT NULL,
    response   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The latest review of a models item: the same shape as vocabulary_reviews.
CREATE TABLE IF NOT EXISTS models_reviews (
    item_id    bigint PRIMARY KEY REFERENCES models_items(id) ON DELETE CASCADE,
    phrase     text NOT NULL,
    findings   jsonb NOT NULL,
    response   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The latest review of a text or dialog, one row per resource and part. version
-- is a hash of the reviewed source text: a proposal is only applied while the
-- resource still has it. No foreign key (the resource is polymorphic).
CREATE TABLE IF NOT EXISTS resource_reviews (
    resource_type text NOT NULL CHECK (resource_type IN ('text', 'dialog')),
    resource_id   bigint NOT NULL,
    part          text NOT NULL CHECK (part IN ('transcription', 'translation')),
    version       text NOT NULL,
    findings      jsonb NOT NULL,
    response      jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, part)
);

-- One-time cleanup of objects older versions created. Delete these two lines
-- once they have run on every installation.
ALTER TABLE llm_prompts DROP COLUMN IF EXISTS prompt;
DROP FUNCTION IF EXISTS normalize_phrase(text);
