// Integration tests: migration seed content and idempotency.
//
// T-6.10 (seed assertions) and E-11 (idempotency) are verified here.
//
// These tests rely on #[sqlx::test] which:
//   1. Creates a fresh temporary database per test.
//   2. Runs all migrations from phraseforge-api/migrations/ automatically.
//   3. Injects a PgPool connected to that database.
//   4. Drops the database after the test completes.
//
// DATABASE_URL must point to a Postgres superuser capable of CREATE DATABASE.
// No hand-inserted reference rows — the migration seeds are what we are verifying.

use sqlx::PgPool;


#[sqlx::test]
async fn seed_scripts_exactly_7_rows(pool: PgPool) {
    let count: i64 = sqlx::query_scalar("SELECT count(*) FROM script")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(count, 7, "script table must contain exactly 7 rows after migration");

    let mut codes: Vec<String> =
        sqlx::query_scalar("SELECT code FROM script ORDER BY code")
            .fetch_all(&pool)
            .await
            .unwrap();
    codes.sort();
    assert_eq!(
        codes,
        vec!["arab", "cyrl", "grek", "hans", "hant", "hebr", "latn"],
        "script codes must match DATA-DICT-005 literally"
    );
}

#[sqlx::test]
async fn seed_language_scripts_exactly_9_rows(pool: PgPool) {
    let count: i64 = sqlx::query_scalar("SELECT count(*) FROM language")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(count, 9, "language_script table must contain exactly 9 rows after migration");

    let mut codes: Vec<String> =
        sqlx::query_scalar("SELECT code FROM language ORDER BY code")
            .fetch_all(&pool)
            .await
            .unwrap();
    codes.sort();
    assert_eq!(
        codes,
        vec!["arb-arab", "cmn-hans", "deu-latn", "eng-latn", "fra-latn", "ita-latn", "pol-latn", "spa-latn", "tur-latn"],
        "language_script codes must match DATA-DICT-006 literally"
    );
}

#[sqlx::test]
async fn seed_grammar_tags_exactly_4_rows(pool: PgPool) {
    let count: i64 = sqlx::query_scalar("SELECT count(*) FROM grammar_tag")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(count, 4, "grammar_tag table must contain exactly 4 rows after migration");

    let mut codes: Vec<String> =
        sqlx::query_scalar("SELECT code FROM grammar_tag ORDER BY code")
            .fetch_all(&pool)
            .await
            .unwrap();
    codes.sort();
    assert_eq!(
        codes,
        vec!["adjective", "adverb", "noun", "verb"],
        "tag codes must match DATA-DICT-007 literally"
    );
}

#[sqlx::test]
async fn content_tables_empty_after_migration(pool: PgPool) {
    let word_count: i64 = sqlx::query_scalar("SELECT count(*) FROM phrase")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(word_count, 0, "phrase table must be empty after migration");

    let translation_count: i64 = sqlx::query_scalar("SELECT count(*) FROM translation")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(translation_count, 0, "translation table must be empty after migration");

    let entry_count: i64 = sqlx::query_scalar("SELECT count(*) FROM dictionary")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(entry_count, 0, "dictionary table must be empty after migration");

    let dictionary_tag_count: i64 = sqlx::query_scalar("SELECT count(*) FROM dictionary_tag")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(dictionary_tag_count, 0, "dictionary_tag table must be empty after migration");
}


#[sqlx::test]
async fn migration_idempotent_second_run_applies_nothing(pool: PgPool) {
    // Run migrations a second time — must succeed (no panic, no error).
    sqlx::migrate!("./migrations")
        .run(&pool)
        .await
        .expect("second migration run must succeed without error (E-11)");

    // Counts must remain 7/9/4 — no rows duplicated.
    let script_count: i64 = sqlx::query_scalar("SELECT count(*) FROM script")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(script_count, 7, "second migration run must not duplicate script rows");

    let ls_count: i64 = sqlx::query_scalar("SELECT count(*) FROM language")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(ls_count, 9, "second migration run must not duplicate language_script rows");

    let tag_count: i64 = sqlx::query_scalar("SELECT count(*) FROM grammar_tag")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(tag_count, 4, "second migration run must not duplicate grammar_tag rows");
}


#[sqlx::test]
async fn db_rejects_word_with_unknown_language_script(pool: PgPool) {
    let result = sqlx::query(
        "INSERT INTO phrase (language_script, text) VALUES ('pol-cyrl', 'test')"
    )
    .execute(&pool)
    .await;

    assert!(result.is_err(), "FK on phrase.language_script must reject an unknown language-script");
    let err = result.unwrap_err().to_string();
    // Postgres SQLSTATE 23503 = foreign_key_violation
    assert!(
        err.contains("23503") || err.contains("foreign key") || err.contains("violates"),
        "error must be a foreign key violation, got: {err}"
    );
}


#[sqlx::test]
async fn db_rejects_dictionary_tag_with_unknown_tag(pool: PgPool) {
    // Set up a valid phrase, translation, and dictionary first.
    sqlx::query(
        "INSERT INTO phrase (language_script, text) VALUES ('eng-latn', 'test')"
    )
    .execute(&pool)
    .await
    .unwrap();

    sqlx::query(
        "INSERT INTO translation (language_script, text) VALUES ('fra-latn', 'test')"
    )
    .execute(&pool)
    .await
    .unwrap();

    let phrase_id: uuid::Uuid =
        sqlx::query_scalar("SELECT id FROM phrase WHERE text = 'test' AND language_script = 'eng-latn'")
            .fetch_one(&pool)
            .await
            .unwrap();

    let translation_id: uuid::Uuid =
        sqlx::query_scalar("SELECT id FROM translation WHERE text = 'test' AND language_script = 'fra-latn'")
            .fetch_one(&pool)
            .await
            .unwrap();

    sqlx::query("INSERT INTO dictionary (phrase_id, translation_id) VALUES ($1, $2)")
        .bind(phrase_id)
        .bind(translation_id)
        .execute(&pool)
        .await
        .unwrap();

    // Attempt to insert an unknown tag
    let result = sqlx::query(
        "INSERT INTO dictionary_tag (phrase_id, translation_id, tag_code) VALUES ($1, $2, 'pronoun')"
    )
    .bind(phrase_id)
    .bind(translation_id)
    .execute(&pool)
    .await;

    assert!(result.is_err(), "FK on dictionary_tag.tag_code must reject an unknown tag");
    let err = result.unwrap_err().to_string();
    assert!(
        err.contains("23503") || err.contains("foreign key") || err.contains("violates"),
        "error must be a foreign key violation, got: {err}"
    );
}


#[sqlx::test]
async fn db_rejects_language_script_with_mismatched_code(pool: PgPool) {
    let result = sqlx::query(
        "INSERT INTO language (code, language_code, script_code) \
         VALUES ('xx-latn', 'xxx', 'latn')"
    )
    .execute(&pool)
    .await;

    assert!(
        result.is_err(),
        "language_script_code_derived CHECK must reject code='xx-latn' when language_code='xxx'"
    );
}
