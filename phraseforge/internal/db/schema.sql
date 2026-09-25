-- phraseforge schema. Applied (idempotently — every statement is safe to
-- rerun) by `phraseforge migrate`. Dialogs and vocabulary lists (parsed from
-- the same markdown via cli-tools' goldmark extension) are read straight out
-- of `texts.body` for now — dedicated tables land when we build those
-- features, not before.

CREATE TABLE IF NOT EXISTS users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Site UI language (chrome, not text content) — a per-user preference, not a
-- lookup table: only two values exist right now and both are baked into the
-- binary's i18n catalog, so a controlled-vocabulary table would just be an
-- extra join for no benefit. Revisit if locales become user-extensible.
ALTER TABLE users ADD COLUMN IF NOT EXISTS locale text NOT NULL DEFAULT 'en';
-- Postgres has no "ADD CONSTRAINT IF NOT EXISTS" — guard manually so this is
-- safe to rerun (every other statement in this file already is).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_locale_check') THEN
        ALTER TABLE users ADD CONSTRAINT users_locale_check CHECK (locale IN ('en', 'pl'));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS texts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      text NOT NULL,
    body       text NOT NULL, -- markdown source (PhraseForge extensions: vocabulary/dialog blocks)
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ── Language / script catalog ───────────────────────────────────────────────
-- Controlled vocabularies as tables of rows, not enums (extensible: INSERT a
-- new row, no migration needed) — same convention phraseforge-api's dictionary
-- schema uses. Seeded verbatim from cli-tools' pkg/catalog/catalog.yml (the
-- single declared source of truth this whole project shares), so PhraseForge
-- and cli-tools never disagree on what a language/script code means.

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
    ('fas', 'fa', 'Persian'),
    ('fra', 'fr', 'French'),
    ('grc', 'el', 'Ancient Greek'),
    ('heb', 'he', 'Hebrew'),
    ('hin', 'hi', 'Hindi'),
    ('ind', 'id', 'Indonesian'),
    ('ita', 'it', 'Italian'),
    ('kaz', 'kk', 'Kazakh'),
    ('lat', 'la', 'Latin'),
    ('lit', 'lt', 'Lithuanian'),
    ('mon', 'mn', 'Mongolian'),
    ('nld', 'nl', 'Dutch'),
    ('pol', 'pl', 'Polish'),
    ('ron', 'ro', 'Romanian'),
    ('spa', 'es', 'Spanish'),
    ('srp', 'sr', 'Serbian'),
    ('tgk', 'tg', 'Tajik'),
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

-- texts gain language/script attributes. Added nullable + backfilled first
-- since the column can't be NOT NULL while existing rows have no value yet;
-- both the ADD COLUMN and the SET NOT NULL are no-ops on a rerun.
ALTER TABLE texts ADD COLUMN IF NOT EXISTS language text REFERENCES language(code);
ALTER TABLE texts ADD COLUMN IF NOT EXISTS script   text REFERENCES script(code);
UPDATE texts SET language = 'ron', script = 'latn' WHERE language IS NULL OR script IS NULL;
ALTER TABLE texts ALTER COLUMN language SET NOT NULL;
ALTER TABLE texts ALTER COLUMN script SET NOT NULL;

-- ── Roles ────────────────────────────────────────────────────────────────────
-- admin: site-wide, can do everything. teacher: scoped to one language, can
-- create/edit texts in it. student: scoped to one language, read/browse only.
-- A user can hold different roles in different languages (e.g. teacher of
-- Romanian, student of Vietnamese) — user_role is a many-to-many grant, not a
-- single column on users.

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

-- Backfill: auth.EnsureUser only grants 'admin' when it creates the very
-- first account. A user table that predates this migration (roles didn't
-- exist yet when it was created) would otherwise end up with zero admins —
-- nobody left who could grant anyone anything. Fires once: only when no
-- admin grant exists at all, so it's a no-op on every later rerun.
INSERT INTO user_role (user_id, role, language)
SELECT (SELECT id FROM users ORDER BY id LIMIT 1), 'admin', NULL
WHERE EXISTS (SELECT 1 FROM users) AND NOT EXISTS (SELECT 1 FROM user_role WHERE role = 'admin')
ON CONFLICT DO NOTHING;

-- ── Tags ─────────────────────────────────────────────────────────────────────
-- One tag table shared by every taggable resource, plus a polymorphic join
-- (resource_type + resource_id) instead of a dedicated text_tag table: the
-- point of this system is that Dialogs, Vocabulary lists, and future Media
-- resources reuse it by inserting rows with a new resource_type value —
-- no new migration or table needed when those land. Tradeoff, stated plainly:
-- resource_id can't carry a real foreign key since it points at a different
-- table per resource_type, so orphaned taggings after a hard-deleted resource
-- are possible; ON DELETE CASCADE only cleans up when the resource's own
-- store also cleans up its taggings (texts.Delete does — see internal/tags).
CREATE TABLE IF NOT EXISTS tag (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE -- stored lowercase/trimmed by internal/tags; enforced in Go, not a CHECK
);

CREATE TABLE IF NOT EXISTS tagging (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tag_id        bigint NOT NULL REFERENCES tag(id) ON DELETE CASCADE,
    resource_type text NOT NULL, -- 'text' today; 'dialog'/'vocabulary'/'media' later
    resource_id   bigint NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS tagging_unique ON tagging (tag_id, resource_type, resource_id);
CREATE INDEX IF NOT EXISTS tagging_resource_idx ON tagging (resource_type, resource_id);

-- ── IME (Input Method Editor) catalog + per-language/script configuration ──
-- Ported from the reference IMEs at deno-app/src/lang-edit (examined, not yet
-- wired into an editor — this migration only adds the *catalog* of available
-- presets and the config table an admin uses to assign them; the JSON
-- key-mapping data itself isn't copied in until something actually renders
-- an editor with live IME switching).
--
-- These 3 scripts are declared here, not in cli-tools' catalog.yml (this
-- project's normal script source of truth) — cli-tools doesn't know about
-- Devanagari/Greek/Georgian yet, but 3 of the reference IME presets need
-- them (Hindi, Modern/Ancient Greek, Georgian). Direction/enlarged are best-
-- guess defaults (all three are plain LTR scripts, no special sizing), not
-- values taken from cli-tools since it doesn't declare them.
INSERT INTO script (code, name, direction, enlarged) VALUES
    ('grek', 'Greek', 'ltr', false),
    ('deva', 'Devanagari', 'ltr', false),
    ('geor', 'Georgian', 'ltr', false)
ON CONFLICT (code) DO NOTHING;

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

-- One row per (language, script) pair this project actually uses for texts;
-- an admin assigns which catalog IME applies to source (the text typed in
-- that language/script) and transcription (cli-tools' "pinned Latin/LTR
-- romanization" field). No translation_ime: translated text is typed in
-- whatever other language the translator is working in, not something this
-- (language, script) pair can pin an IME for. Both roles are nullable — a
-- pair can have only one configured, or neither yet.
CREATE TABLE IF NOT EXISTS ime_config (
    language          text NOT NULL REFERENCES language(code),
    script            text NOT NULL REFERENCES script(code),
    source_ime        text REFERENCES ime(code),
    transcription_ime text REFERENCES ime(code),
    PRIMARY KEY (language, script)
);
-- Drops the column for anyone who already applied the earlier version of
-- this migration (safe to rerun: DROP COLUMN IF EXISTS is a no-op after).
ALTER TABLE ime_config DROP COLUMN IF EXISTS translation_ime;

-- Some scripts are phonetically transparent enough that a romanization
-- adds nothing (e.g. Romanian in Latin script); others genuinely need one
-- to be readable by learners (e.g. Arabic, Mandarin). This flag records
-- that per (language, script) pair — independent of whether a transcription
-- IME is actually configured, since an admin may want to flag "yes, this
-- needs a transcription" before picking which IME serves it.
ALTER TABLE ime_config ADD COLUMN IF NOT EXISTS needs_transcription boolean NOT NULL DEFAULT false;

-- ── Transcription / translation ─────────────────────────────────────────────
-- transcription is one field per text (the "pinned Latin/LTR romanization" —
-- a property of the source, not of who's reading it), so it lives directly
-- on texts.
ALTER TABLE texts ADD COLUMN IF NOT EXISTS transcription text;

-- Records the URL or original filename a text was ingested from
-- (phraseforge-ingest-texts-dialogs); NULL for manually-created or
-- pasted-text-ingested rows.
ALTER TABLE texts ADD COLUMN IF NOT EXISTS ingest_source text;

-- Translation is stored per *viewer* site locale AND per resource, using the
-- same resource_type + resource_id polymorphism as tagging (see below): a
-- resource can accumulate an English translation, a Polish one, both, or
-- neither, each contributed independently by whoever edited it while their
-- own profile language was set to it. Generalized (not "text_translation")
-- from the start this table's Dialogs support needs it too, and Vocabulary
-- lists will later — same reasoning as tagging's resource_type column.
CREATE TABLE IF NOT EXISTS resource_translation (
    resource_type text NOT NULL, -- 'text' today; 'dialog'/'vocabulary' later
    resource_id   bigint NOT NULL,
    locale        text NOT NULL CHECK (locale IN ('en', 'pl')),
    body          text NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, locale)
);

-- One-time migration from the earlier texts-only table, if it still exists
-- (safe to rerun: the IF EXISTS check is false and this is a no-op after the
-- first successful run drops the old table).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'text_translation') THEN
        INSERT INTO resource_translation (resource_type, resource_id, locale, body, updated_at)
        SELECT 'text', text_id, locale, body, updated_at FROM text_translation
        ON CONFLICT DO NOTHING;
        DROP TABLE text_translation;
    END IF;
END $$;

-- ── Dialogs ──────────────────────────────────────────────────────────────────
-- Same shape as texts (title/body/transcription/language/script, shared
-- per-language visibility, tags and translations via the resource-agnostic
-- tables above) but its own table: body holds cli-tools' {start-dialog}/
-- {end-dialog} markdown instead of {start-vocabulary} — a genuinely
-- different content shape, not just a relabeled text.
CREATE TABLE IF NOT EXISTS dialogs (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         text NOT NULL,
    body          text NOT NULL, -- markdown source (PhraseForge {start-dialog} blocks)
    transcription text,
    language      text NOT NULL REFERENCES language(code),
    script        text NOT NULL REFERENCES script(code),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Records the URL or original filename a dialog was ingested from
-- (phraseforge-ingest-texts-dialogs); NULL for manually-created or
-- pasted-text-ingested rows.
ALTER TABLE dialogs ADD COLUMN IF NOT EXISTS ingest_source text;

-- ── Vocabulary lists ─────────────────────────────────────────────────────────
-- Genuinely different shape from texts/dialogs, not a relabeled markdown
-- blob: phrase/grammar/transcription are common across every viewer, but
-- translation and notes are per-viewer-locale — a list can have items with
-- an English translation, a Polish one, both, or neither, editable
-- independently. That's a real per-item, per-locale structure, so it gets
-- its own item table plus a translation table keyed by (list, position,
-- locale) — not the resource_translation table above, which holds one body
-- per whole resource, not per item within it.
--
-- position is the item's stable identity within its list (not a synthetic
-- id): editing the common fields (phrase/grammar/transcription) upserts by
-- position, so other locales' translations at that position survive. This
-- is a known, disclosed limitation: reordering or deleting a middle item
-- shifts every later position, which re-associates their translations with
-- the wrong new occupant — acceptable for a first slice, revisit if list
-- reordering becomes a real need.
CREATE TABLE IF NOT EXISTS vocabulary_lists (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      text NOT NULL,
    language   text NOT NULL REFERENCES language(code),
    script     text NOT NULL REFERENCES script(code),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS vocabulary_items (
    list_id       bigint NOT NULL REFERENCES vocabulary_lists(id) ON DELETE CASCADE,
    position      integer NOT NULL,
    phrase        text NOT NULL,
    grammar       text,
    transcription text,
    PRIMARY KEY (list_id, position)
);

CREATE TABLE IF NOT EXISTS vocabulary_item_translation (
    list_id     bigint NOT NULL,
    position    integer NOT NULL,
    locale      text NOT NULL CHECK (locale IN ('en', 'pl')),
    translation text,
    notes       text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, position, locale),
    FOREIGN KEY (list_id, position) REFERENCES vocabulary_items(list_id, position) ON DELETE CASCADE
);

ALTER TABLE vocabulary_item_translation ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- Deleting an item shifts every later item's position down by one, and its
-- translations (every locale) need to move with it in the same transaction
-- — but the default immediate FK check refuses to update a referenced
-- vocabulary_items row's position while a translation row still points at
-- the old value, even though both are corrected before commit. Deferring
-- the check to commit time is the standard fix; ALTER CONSTRAINT is
-- idempotent (safe to rerun, matches this file's style).
ALTER TABLE vocabulary_item_translation
    ALTER CONSTRAINT vocabulary_item_translation_list_id_position_fkey
    DEFERRABLE INITIALLY DEFERRED;

-- ── Model lists ──────────────────────────────────────────────────────────────
-- cli-tools' own {start-models} block (SPECS/README: "like {start-vocabulary}
-- but without a grammar tag or notes" — phrase + optional [transcription] +
-- optional = translation). Same shape and tradeoffs as vocabulary_* above
-- (position-as-identity, deferred FK so delete-and-shift can move a
-- translation to a new position in the same transaction as its item), just
-- without the grammar/notes columns models doesn't have.
CREATE TABLE IF NOT EXISTS models_lists (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      text NOT NULL,
    language   text NOT NULL REFERENCES language(code),
    script     text NOT NULL REFERENCES script(code),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS models_items (
    list_id       bigint NOT NULL REFERENCES models_lists(id) ON DELETE CASCADE,
    position      integer NOT NULL,
    phrase        text NOT NULL,
    transcription text,
    PRIMARY KEY (list_id, position)
);

CREATE TABLE IF NOT EXISTS models_item_translation (
    list_id     bigint NOT NULL,
    position    integer NOT NULL,
    locale      text NOT NULL CHECK (locale IN ('en', 'pl')),
    translation text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, position, locale),
    FOREIGN KEY (list_id, position) REFERENCES models_items(list_id, position) ON DELETE CASCADE
);

ALTER TABLE models_item_translation
    ALTER CONSTRAINT models_item_translation_list_id_position_fkey
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS llm_prompts (
    kind text NOT NULL CHECK (kind IN ('translation', 'transcription')),
    source_language text NOT NULL,
    target_language text NOT NULL,
    model text NOT NULL DEFAULT '',
    prompt text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, source_language, target_language)
);
ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS model text NOT NULL DEFAULT '';
ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'ollama';
ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS think boolean NOT NULL DEFAULT false;
-- Ingest (phraseforge-ingest-texts-dialogs) adds three more LLM purposes on
-- top of the original translation/transcription pair. Postgres has no
-- "ALTER CONSTRAINT ... CHECK" — drop and recreate under the same name, same
-- guarded pattern as users_locale_check above, so this is safe to rerun and
-- correctly upgrades an installation created before this migration.
ALTER TABLE llm_prompts DROP CONSTRAINT IF EXISTS llm_prompts_kind_check;
ALTER TABLE llm_prompts ADD CONSTRAINT llm_prompts_kind_check
    CHECK (kind IN ('translation', 'transcription', 'title', 'process_text', 'process_dialog', 'generate_vocabulary', 'generate_models'));

-- phraseforge-generate-vocab-models-from-text: links a vocabulary/models
-- list to the text it was generated from, so a rerun of "Generate
-- Vocabulary"/"Generate Models" on that same text can find and
-- wholesale-replace its own list's items instead of creating a duplicate one
-- every time. NULL for every hand-created list (the normal, pre-existing
-- case) and ON DELETE SET NULL rather than CASCADE — deleting the source
-- text must never silently delete a list of vocabulary/models a learner may
-- still want to keep studying.
ALTER TABLE vocabulary_lists ADD COLUMN IF NOT EXISTS source_text_id bigint REFERENCES texts(id) ON DELETE SET NULL;
ALTER TABLE models_lists ADD COLUMN IF NOT EXISTS source_text_id bigint REFERENCES texts(id) ON DELETE SET NULL;

-- ── Jobs ─────────────────────────────────────────────────────────────────────
-- Ported from knowledge/internal/queue's "operations" table (see
-- specs/features/phraseforge-job-queue.md), but as a single unified table
-- rather than knowledge's split jobs (ingest-specific)/operations (general
-- LLM queue) design — a future Jobs menu is meant to show every submitted
-- operation, interactive and background alike, in one place. id is generated
-- client-side in Go (phraseforge/internal/jobs.uuid), matching knowledge's
-- own operations/jobs tables — this project has no pgcrypto/uuid-ossp
-- extension enabled, so there's no gen_random_uuid() to default to.
CREATE TABLE IF NOT EXISTS jobs (
    id         uuid PRIMARY KEY,
    kind       text NOT NULL,
    priority   text NOT NULL CHECK (priority IN ('interactive', 'background')),
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed')),
    payload    jsonb NOT NULL DEFAULT '{}',
    result     jsonb,
    error      text,
    step       text, -- opaque per-kind progress marker (jobs.Service.SetStep); phraseforge/internal/ingest's process_text/process_dialog handlers use it to record "row already created" so a Retry (which carries this value forward onto the new job it enqueues) doesn't redo that step
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Enforces exactly one job running app-wide: the single-worker/single-
-- Ollama-call-at-a-time constraint (see specs/memory.md on Ollama load
-- starving the k3d node).
CREATE UNIQUE INDEX IF NOT EXISTS jobs_one_running_idx ON jobs ((true)) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (created_at ASC) WHERE status = 'pending';
