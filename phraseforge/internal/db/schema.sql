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
-- its own phrases + phrase_translation tables (keyed by phrase, locale) — not
-- the resource_translation table above, which holds one body per whole
-- resource, not per item within it. A list links to phrases through
-- vocabulary_items (list_id, position, phrase_id); position is the item's
-- identity within its list, so deleting a middle item renumbers the later
-- positions of that list only — translations live on the phrase and are
-- unaffected.
CREATE TABLE IF NOT EXISTS vocabulary_lists (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      text NOT NULL,
    language   text NOT NULL REFERENCES language(code),
    script     text NOT NULL REFERENCES script(code),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A phrase is stored once per (language, script, phrase, grammar,
-- transcription) and linked from any number of vocabulary lists
-- (phraseforge-shared-phrases). Translations hang off the phrase, so a phrase
-- already translated in one list needs no translation job in another.
-- grammar/transcription are '' (not NULL) when absent, so the UNIQUE key
-- needs no NULL handling.
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

CREATE TABLE IF NOT EXISTS vocabulary_items (
    list_id   bigint NOT NULL REFERENCES vocabulary_lists(id) ON DELETE CASCADE,
    position  integer NOT NULL,
    phrase_id bigint NOT NULL REFERENCES phrases(id),
    PRIMARY KEY (list_id, position)
);

-- One-time migration from the earlier per-list shape (phrase/grammar/
-- transcription stored on each vocabulary_items row, translations keyed by
-- (list_id, position, locale)), if it still exists. Duplicates are merged
-- into one phrase; when several copies were translated for the same locale
-- the first non-empty one wins (lowest list_id, then position) and the rest
-- are lost, by the user's explicit decision. Safe to rerun: the column check
-- is false once the old columns are dropped.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'vocabulary_items' AND column_name = 'phrase') THEN
        INSERT INTO phrases (language, script, phrase, grammar, transcription)
        SELECT DISTINCT l.language, l.script, i.phrase, coalesce(i.grammar, ''), coalesce(i.transcription, '')
        FROM vocabulary_items i JOIN vocabulary_lists l ON l.id = i.list_id
        ON CONFLICT DO NOTHING;

        ALTER TABLE vocabulary_items ADD COLUMN phrase_id bigint REFERENCES phrases(id);
        UPDATE vocabulary_items i SET phrase_id = p.id
        FROM vocabulary_lists l, phrases p
        WHERE l.id = i.list_id
          AND p.language = l.language AND p.script = l.script AND p.phrase = i.phrase
          AND p.grammar = coalesce(i.grammar, '') AND p.transcription = coalesce(i.transcription, '');

        INSERT INTO phrase_translation (phrase_id, locale, translation, notes, updated_at)
        SELECT DISTINCT ON (i.phrase_id, t.locale) i.phrase_id, t.locale, t.translation, t.notes, t.updated_at
        FROM vocabulary_item_translation t
        JOIN vocabulary_items i ON i.list_id = t.list_id AND i.position = t.position
        ORDER BY i.phrase_id, t.locale, (nullif(btrim(t.translation), '') IS NULL), t.list_id, t.position
        ON CONFLICT DO NOTHING;

        DROP TABLE vocabulary_item_translation;
        ALTER TABLE vocabulary_items ALTER COLUMN phrase_id SET NOT NULL;
        ALTER TABLE vocabulary_items DROP COLUMN phrase, DROP COLUMN grammar, DROP COLUMN transcription;
    END IF;
END $$;

-- Phrase text is normalised (surrounding whitespace trimmed, Unicode NFC)
-- before it is stored or compared (phraseforge/internal/textnorm.Phrase), so
-- text that looks identical is one record. This is the same rule in SQL, for
-- normalising rows stored before it existed.
CREATE OR REPLACE FUNCTION normalize_phrase(t text) RETURNS text AS $$
    SELECT normalize(regexp_replace(t, '^[[:space:]]+|[[:space:]]+$', '', 'g'), NFC)
$$ LANGUAGE sql IMMUTABLE;

-- Normalise the phrases stored before that rule. A row whose normalised form is
-- already taken by another row is left as it is — merging two rows is the job
-- of an edit (vocabulary.Store.retargetPhrase), not of this statement — so it
-- never fails on the UNIQUE key; when several rows would normalise to the same
-- key, one that is already normal is kept as the representative. Safe to rerun.
UPDATE phrases p SET phrase = n.phrase, grammar = n.grammar, transcription = n.transcription
FROM (
    SELECT DISTINCT ON (language, script, nphrase, ngrammar, ntranscription)
           id, nphrase AS phrase, ngrammar AS grammar, ntranscription AS transcription
    FROM (
        SELECT id, language, script, phrase, grammar, transcription,
               normalize_phrase(phrase) AS nphrase, normalize_phrase(grammar) AS ngrammar, normalize_phrase(transcription) AS ntranscription
        FROM phrases
    ) t
    ORDER BY language, script, nphrase, ngrammar, ntranscription,
             (phrase = nphrase AND grammar = ngrammar AND transcription = ntranscription) DESC, id
) n
WHERE p.id = n.id
  AND (p.phrase, p.grammar, p.transcription) IS DISTINCT FROM (n.phrase, n.grammar, n.transcription)
  AND NOT EXISTS (SELECT 1 FROM phrases o WHERE o.id <> p.id AND o.language = p.language AND o.script = p.script
                  AND o.phrase = n.phrase AND o.grammar = n.grammar AND o.transcription = n.transcription);

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

-- ── Model lists ──────────────────────────────────────────────────────────────
-- cli-tools' own {start-models} block (SPECS/README: "like {start-vocabulary}
-- but without a grammar tag or notes" — phrase + optional [transcription] +
-- optional = translation). Same shape as vocabulary_* above (a list links to
-- shared phrases by (list_id, position)), just without the grammar/notes
-- columns models doesn't have.
CREATE TABLE IF NOT EXISTS models_lists (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      text NOT NULL,
    language   text NOT NULL REFERENCES language(code),
    script     text NOT NULL REFERENCES script(code),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Shared exactly like vocabulary's phrases (phraseforge-shared-phrases): one
-- row per (language, script, phrase, transcription), linked from any number
-- of models lists, with the per-locale translation on the phrase. There is no
-- grammar or notes — models doesn't have them. transcription is '' (not NULL)
-- when absent so the UNIQUE key needs no NULL handling.
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
    PRIMARY KEY (list_id, position)
);

-- One-time migration from the earlier per-list shape, mirroring vocabulary's
-- (see the DO block above vocabulary_items' orphan trigger): duplicates merge
-- into one phrase, the first non-empty translation per locale wins (lowest
-- list_id, then position), the rest are lost by the user's explicit decision.
-- Safe to rerun: the column check is false once the old columns are dropped.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'models_items' AND column_name = 'phrase') THEN
        INSERT INTO models_phrases (language, script, phrase, transcription)
        SELECT DISTINCT l.language, l.script, i.phrase, coalesce(i.transcription, '')
        FROM models_items i JOIN models_lists l ON l.id = i.list_id
        ON CONFLICT DO NOTHING;

        ALTER TABLE models_items ADD COLUMN phrase_id bigint REFERENCES models_phrases(id);
        UPDATE models_items i SET phrase_id = p.id
        FROM models_lists l, models_phrases p
        WHERE l.id = i.list_id
          AND p.language = l.language AND p.script = l.script AND p.phrase = i.phrase
          AND p.transcription = coalesce(i.transcription, '');

        INSERT INTO models_phrase_translation (phrase_id, locale, translation, updated_at)
        SELECT DISTINCT ON (i.phrase_id, t.locale) i.phrase_id, t.locale, t.translation, t.updated_at
        FROM models_item_translation t
        JOIN models_items i ON i.list_id = t.list_id AND i.position = t.position
        ORDER BY i.phrase_id, t.locale, (nullif(btrim(t.translation), '') IS NULL), t.list_id, t.position
        ON CONFLICT DO NOTHING;

        DROP TABLE models_item_translation;
        ALTER TABLE models_items ALTER COLUMN phrase_id SET NOT NULL;
        ALTER TABLE models_items DROP COLUMN phrase, DROP COLUMN transcription;
    END IF;
END $$;

-- Normalise the models phrases stored before the rule, exactly like phrases
-- above (rows whose normalised form is taken are left as they are).
UPDATE models_phrases p SET phrase = n.phrase, transcription = n.transcription
FROM (
    SELECT DISTINCT ON (language, script, nphrase, ntranscription)
           id, nphrase AS phrase, ntranscription AS transcription
    FROM (
        SELECT id, language, script, phrase, transcription,
               normalize_phrase(phrase) AS nphrase, normalize_phrase(transcription) AS ntranscription
        FROM models_phrases
    ) t
    ORDER BY language, script, nphrase, ntranscription,
             (phrase = nphrase AND transcription = ntranscription) DESC, id
) n
WHERE p.id = n.id
  AND (p.phrase, p.transcription) IS DISTINCT FROM (n.phrase, n.transcription)
  AND NOT EXISTS (SELECT 1 FROM models_phrases o WHERE o.id <> p.id AND o.language = p.language AND o.script = p.script
                  AND o.phrase = n.phrase AND o.transcription = n.transcription);

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
    CHECK (kind IN ('translation', 'transcription', 'title', 'process_text', 'process_dialog', 'generate_vocabulary', 'generate_models', 'vocabulary_item', 'models_item'));

-- llm-purpose-timeout-and-prompt-config: an optional per-(kind, source,
-- target) admin override of that call's timeout. NULL (the default, and
-- what every pre-existing row has) means "inherit" — fall through to the
-- purpose's own config.yaml default, then shared/llm's built-in 2-minute
-- default — deliberately different from `think`, which takes the row's
-- value unconditionally whenever a row exists at all.
ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS timeout_seconds integer;
ALTER TABLE llm_prompts DROP CONSTRAINT IF EXISTS llm_prompts_timeout_seconds_check;
ALTER TABLE llm_prompts ADD CONSTRAINT llm_prompts_timeout_seconds_check
    CHECK (timeout_seconds IS NULL OR (timeout_seconds > 0 AND timeout_seconds <= 3600));

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

-- dialog-vocabulary-models-generation: "Dialog is just a specialized text" —
-- Generate Vocabulary/Generate Models extend to Dialogs, mirroring
-- source_text_id above exactly (same ON DELETE SET NULL rationale).
ALTER TABLE vocabulary_lists ADD COLUMN IF NOT EXISTS source_dialog_id bigint REFERENCES dialogs(id) ON DELETE SET NULL;
ALTER TABLE models_lists ADD COLUMN IF NOT EXISTS source_dialog_id bigint REFERENCES dialogs(id) ON DELETE SET NULL;

-- Confirmed against a live DB (no existing duplicates) before adding these:
-- nothing previously stopped two lists linking to the same source text (a
-- race between two enqueued generate jobs, or a Retry, could otherwise
-- produce two lists with an arbitrary "winner" thereafter). One partial
-- unique index per (table x source column) — partial, since most rows have
-- a NULL source and NULLs must stay unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS vocabulary_lists_source_text_id_idx ON vocabulary_lists (source_text_id) WHERE source_text_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS vocabulary_lists_source_dialog_id_idx ON vocabulary_lists (source_dialog_id) WHERE source_dialog_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS models_lists_source_text_id_idx ON models_lists (source_text_id) WHERE source_text_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS models_lists_source_dialog_id_idx ON models_lists (source_dialog_id) WHERE source_dialog_id IS NOT NULL;

-- A hand-edited import could otherwise set both source_text_id and
-- source_dialog_id on the same list, which makes no sense (a list has at
-- most one source). Same drop-and-recreate pattern as
-- llm_prompts_kind_check above.
ALTER TABLE vocabulary_lists DROP CONSTRAINT IF EXISTS vocabulary_lists_single_source_check;
ALTER TABLE vocabulary_lists ADD CONSTRAINT vocabulary_lists_single_source_check
    CHECK (source_text_id IS NULL OR source_dialog_id IS NULL);
ALTER TABLE models_lists DROP CONSTRAINT IF EXISTS models_lists_single_source_check;
ALTER TABLE models_lists ADD CONSTRAINT models_lists_single_source_check
    CHECK (source_text_id IS NULL OR source_dialog_id IS NULL);

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
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'done', 'failed', 'cancelled')),
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

-- jobs-cancel-pending-running: a job an admin deliberately stopped reads
-- differently in the Status column than one that genuinely failed. Same
-- drop-and-recreate pattern as llm_prompts_kind_check above — confirmed
-- against a live DB that the auto-generated constraint name really is
-- jobs_status_check (Postgres names an unnamed column CHECK
-- <table>_<column>_check).
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('pending', 'running', 'done', 'failed', 'cancelled'));

-- phraseforge-ingest-very-long-texts: the results of the chunks a job has
-- finished, {key: [result, ...]}, so a Retry (which copies it onto the new
-- job, see jobs.Service.Retry) resumes at the first unfinished chunk instead
-- of repeating hours of LLM calls. Cleared when the job is done.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS checkpoint jsonb;

-- phraseforge-ingest-very-long-texts: how far a chunked job has got, "3/12"
-- (finished chunks / all chunks), shown in the Jobs menu while it runs.
-- Cleared when the job is done; a failed job keeps it to show where it stopped.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS progress text;

-- phraseforge-structured-item-translation: optional per-language prompt
-- sections rendered into the vocabularyItem/modelsItem templates as
-- {{grammarPrompt}}/{{transcriptionPrompt}} — keyed by source language only,
-- since grammar tags and the transcription system describe the source
-- phrase whatever the target locale. Blank means "section absent".
CREATE TABLE IF NOT EXISTS language_llm_sections (
    language             text PRIMARY KEY REFERENCES language(code) ON DELETE CASCADE,
    grammar_prompt       text NOT NULL DEFAULT '',
    transcription_prompt text NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT now()
);
