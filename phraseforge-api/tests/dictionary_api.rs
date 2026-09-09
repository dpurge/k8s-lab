use axum::body::Body;
use axum::http::{Method, Request, StatusCode};
use serde_json::{json, Value};
use sqlx::{PgPool, postgres::{PgConnectOptions, PgPoolOptions}};
use std::time::Duration;
use tower::ServiceExt; // for Router::oneshot

use phraseforge_api::{http::router as build_router, state::AppState};

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

/// Build a fresh axum Router from the test pool.
/// Cheap: PgPool is Arc-backed; cloning is a ref-count bump.
fn app(pool: &PgPool) -> axum::Router {
    build_router(AppState { pool: pool.clone() })
}

async fn call(pool: &PgPool, req: Request<Body>) -> (StatusCode, Value) {
    let resp = app(pool).oneshot(req).await.unwrap();
    let status = resp.status();
    let bytes = axum::body::to_bytes(resp.into_body(), 1_048_576)
        .await
        .expect("body too large or read failed");
    let body = if bytes.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&bytes).expect("response body must be valid JSON")
    };
    (status, body)
}

async fn post(pool: &PgPool, uri: &str, body: Value) -> (StatusCode, Value) {
    call(
        pool,
        Request::builder()
            .method(Method::POST)
            .uri(uri)
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap(),
    )
    .await
}

async fn get(pool: &PgPool, uri: &str) -> (StatusCode, Value) {
    call(
        pool,
        Request::builder()
            .method(Method::GET)
            .uri(uri)
            .body(Body::empty())
            .unwrap(),
    )
    .await
}

async fn put(pool: &PgPool, uri: &str, body: Value) -> (StatusCode, Value) {
    call(
        pool,
        Request::builder()
            .method(Method::PUT)
            .uri(uri)
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap(),
    )
    .await
}

async fn delete(pool: &PgPool, uri: &str) -> StatusCode {
    let resp = app(pool)
        .oneshot(
            Request::builder()
                .method(Method::DELETE)
                .uri(uri)
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    resp.status()
}

// Shorthand URI for the dictionaries endpoint
const ENTRIES: &str = "/api/v1/dictionary/dictionaries";
const TRANSLATIONS: &str = "/api/v1/dictionary/translations";


#[sqlx::test]
async fn create_happy_path(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun"]
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED, "create must return 201");
    assert_eq!(body["phrase"]["text"], "house");
    assert_eq!(body["phrase"]["language_script"], "eng-latn");
    assert_eq!(body["translation"]["text"], "maison");
    assert_eq!(body["translation"]["language_script"], "fra-latn");
    assert_eq!(body["tags"], json!(["noun"]));
    // transcription absent — field must not appear (DATA-DICT-009 second criterion)
    assert!(
        body["phrase"]["transcription"].is_null(),
        "transcription must be absent from JSON when not set; got: {:?}",
        body["phrase"]["transcription"]
    );
}

/// Second translation for the same phrase must succeed. FR-API-001.
#[sqlx::test]
async fn create_second_translation_same_word(pool: PgPool) {
    let (s1, b1) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;
    assert_eq!(s1, StatusCode::CREATED);

    let (s2, b2) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "demeure",
        "target": "fra-latn"
    }))
    .await;
    assert_eq!(s2, StatusCode::CREATED, "second translation for same phrase must be 201");

    // Same phrase, different translation ids
    assert_eq!(b1["phrase"]["id"], b2["phrase"]["id"], "both dictionaries must share the same phrase ID");
    assert_ne!(b1["translation"]["id"], b2["translation"]["id"]);
}


#[sqlx::test]
async fn create_shared_translation_reuses_row(pool: PgPool) {
    // First dictionary: house → maison
    let (_, b1) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    // Second dictionary: home → maison (same translation text+language)
    let (s2, b2) = post(&pool, ENTRIES, json!({
        "phrase": "home",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;
    assert_eq!(s2, StatusCode::CREATED);

    // translation.id must be identical — same DB row reused (DATA-DICT-001)
    assert_eq!(
        b1["translation"]["id"], b2["translation"]["id"],
        "translation row must be reused when text+language-script are identical (DATA-DICT-001)"
    );
}


#[sqlx::test]
async fn create_exact_duplicate_returns_409(pool: PgPool) {
    let payload = json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    });

    let (s1, _) = post(&pool, ENTRIES, payload.clone()).await;
    assert_eq!(s1, StatusCode::CREATED);

    let (s2, body) = post(&pool, ENTRIES, payload).await;
    assert_eq!(s2, StatusCode::CONFLICT, "duplicate create must return 409 (E-01)");
    assert_eq!(body["error"]["code"], "conflict", "error code must be 'conflict' (E-01)");

    // Row counts must be unchanged
    let entry_count: i64 = sqlx::query_scalar("SELECT count(*) FROM dictionary")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(entry_count, 1, "duplicate create must not insert a second dictionary row (E-01)");
}


#[sqlx::test]
async fn create_with_multiple_tags_round_trips_all(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi",
        "tags": ["noun", "adjective"]
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED, "create with multi-tag must return 201 (E-16)");

    let tags = &body["tags"];
    let mut returned: Vec<&str> = tags.as_array().unwrap().iter()
        .map(|v| v.as_str().unwrap())
        .collect();
    returned.sort();
    assert_eq!(returned, vec!["adjective", "noun"], "both tags must be returned (E-16)");
}


#[sqlx::test]
async fn create_with_transcription_returns_it(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi"
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(
        body["phrase"]["transcription"], "fángzi",
        "transcription must appear in 201 response (E-17)"
    );
}


#[sqlx::test]
async fn create_without_transcription_omits_field(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED);

    // The field must not be serialized at all (skip_serializing_if = "Option::is_none")
    let word_obj = body["phrase"].as_object().unwrap();
    assert!(
        !word_obj.contains_key("transcription"),
        "transcription key must be absent from JSON when None (DATA-DICT-009)"
    );
}


#[sqlx::test]
async fn create_without_tags_returns_empty_array(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(
        body["tags"],
        json!([]),
        "omitted tags must serialize as empty array, not null or missing (DATA-DICT-008)"
    );
}


#[sqlx::test]
async fn create_overwrites_null_transcription_when_supplied(pool: PgPool) {
    // First create: no transcription → stored as NULL
    post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn"
    }))
    .await;

    // Second create: same phrase, supplies transcription → must overwrite NULL
    let (_, body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "dwelling",   // different translation to avoid 409
        "target": "eng-latn",
        "transcription": "fángzi"
    }))
    .await;

    assert_eq!(
        body["phrase"]["transcription"], "fángzi",
        "create with transcription must overwrite previously-NULL stored value (E-19)"
    );

    // Verify DB directly
    let stored: Option<String> = sqlx::query_scalar(
        "SELECT transcription FROM phrase WHERE text = '房子' AND language_script = 'cmn-hans'"
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(stored.as_deref(), Some("fángzi"), "DB must reflect the overwritten transcription");
}


#[sqlx::test]
async fn create_overwrites_different_transcription_when_supplied(pool: PgPool) {
    post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fangzi"    // initial (wrong) transcription
    }))
    .await;

    // Re-create same phrase with corrected transcription → must overwrite
    let (_, body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "dwelling",
        "target": "eng-latn",
        "transcription": "fángzi"   // correct transcription
    }))
    .await;

    assert_eq!(
        body["phrase"]["transcription"], "fángzi",
        "create with different transcription must overwrite the old value (E-19)"
    );
}


#[sqlx::test]
async fn create_absent_transcription_leaves_stored_value(pool: PgPool) {
    // First create: establishes transcription
    post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi"
    }))
    .await;

    // Second create: no transcription field → stored value must remain
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "dwelling",
        "target": "eng-latn"
        // transcription field absent entirely
    }))
    .await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(
        body["phrase"]["transcription"], "fángzi",
        "absent transcription field must leave stored value unchanged (E-19)"
    );
}


#[sqlx::test]
async fn lookup_unknown_word_returns_404(pool: PgPool) {
    let (status, body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=unknown&source=eng-latn&target=fra-latn"),
    )
    .await;

    assert_eq!(status, StatusCode::NOT_FOUND, "unknown phrase must return 404 (E-04)");
    assert_eq!(body["error"]["code"], "not_found");
}


#[sqlx::test]
async fn lookup_known_word_no_target_entries_returns_empty_200(pool: PgPool) {
    // Create dictionary for eng-latn → fra-latn but look up fra-latn → deu-latn (no dictionaries)
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "transcription": "howss"
    }))
    .await;

    // Target deu-latn: no dictionary exists, but phrase (house/eng-latn) does
    let (status, body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=house&source=eng-latn&target=deu-latn"),
    )
    .await;

    assert_eq!(status, StatusCode::OK, "known phrase, no target dictionaries must return 200 (E-03)");
    assert_eq!(
        body["translations"],
        json!([]),
        "translations must be empty array, not 404 (E-03)"
    );
    // E-17: transcription still present in the phrase object
    assert_eq!(
        body["phrase"]["transcription"], "howss",
        "transcription must appear in lookup response even when translation list is empty (E-17)"
    );
}


#[sqlx::test]
async fn lookup_untagged_entry_returns_empty_tags_not_dropped(pool: PgPool) {
    // Create dictionary without tags
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
        // no tags field
    }))
    .await;

    let (status, body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=house&source=eng-latn&target=fra-latn"),
    )
    .await;

    assert_eq!(status, StatusCode::OK);
    let translations = body["translations"].as_array().unwrap();
    assert_eq!(translations.len(), 1, "untagged dictionary must appear in lookup result (E-15)");
    assert_eq!(
        translations[0]["tags"],
        json!([]),
        "untagged dictionary must have tags:[] not dropped (E-15, DATA-DICT-008)"
    );
}


#[sqlx::test]
async fn lookup_returns_multiple_translations_with_correct_tags(pool: PgPool) {
    // house → maison (noun)
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun"]
    }))
    .await;

    // house → demeure (noun, adjective)
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "demeure",
        "target": "fra-latn",
        "tags": ["noun", "adjective"]
    }))
    .await;

    let (status, body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=house&source=eng-latn&target=fra-latn"),
    )
    .await;

    assert_eq!(status, StatusCode::OK);
    let translations = body["translations"].as_array().unwrap();
    assert_eq!(translations.len(), 2, "both translations must appear");

    // Each translation carries its own tag list
    let maison = translations.iter().find(|t| t["text"] == "maison").unwrap();
    let demeure = translations.iter().find(|t| t["text"] == "demeure").unwrap();

    let maison_tags: Vec<&str> = maison["tags"].as_array().unwrap().iter()
        .map(|v| v.as_str().unwrap()).collect();
    assert_eq!(maison_tags, vec!["noun"]);

    let mut demeure_tags: Vec<&str> = demeure["tags"].as_array().unwrap().iter()
        .map(|v| v.as_str().unwrap()).collect();
    demeure_tags.sort();
    assert_eq!(demeure_tags, vec!["adjective", "noun"]);
}


#[sqlx::test]
async fn lookup_returns_transcription_with_results(pool: PgPool) {
    post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi"
    }))
    .await;

    let (status, body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=%E6%88%BF%E5%AD%90&source=cmn-hans&target=eng-latn"),
    )
    .await;

    assert_eq!(status, StatusCode::OK);
    assert_eq!(
        body["phrase"]["transcription"], "fángzi",
        "transcription must appear in lookup response alongside translations (E-17)"
    );
    assert_eq!(body["translations"].as_array().unwrap().len(), 1);
}


#[sqlx::test]
async fn list_empty_database_returns_empty_object(pool: PgPool) {
    let (status, body) = get(&pool, ENTRIES).await;

    assert_eq!(status, StatusCode::OK, "empty list must return 200, not 404");
    assert_eq!(body["dictionaries"], json!([]), "dictionaries must be empty array");
    assert_eq!(body["count"], 0, "count must be 0 for empty database");
}


#[sqlx::test]
async fn list_items_carry_full_field_set(pool: PgPool) {
    post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hans",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi",
        "tags": ["noun"]
    }))
    .await;

    let (status, body) = get(&pool, ENTRIES).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["count"], 1);

    let dictionary = &body["dictionaries"][0];
    assert_eq!(dictionary["phrase"]["text"], "房子");
    assert_eq!(dictionary["phrase"]["language_script"], "cmn-hans");
    assert_eq!(dictionary["phrase"]["transcription"], "fángzi");
    assert_eq!(dictionary["translation"]["text"], "house");
    assert_eq!(dictionary["translation"]["language_script"], "eng-latn");
    assert_eq!(dictionary["tags"], json!(["noun"]));
}


#[sqlx::test]
async fn list_untagged_entry_not_dropped(pool: PgPool) {
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let (status, body) = get(&pool, ENTRIES).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["count"], 1, "untagged dictionary must appear in list (E-15 / R-9)");
    assert_eq!(body["dictionaries"][0]["tags"], json!([]));
}


#[sqlx::test]
async fn delete_by_natural_key_returns_204(pool: PgPool) {
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let status = delete(
        &pool,
        &format!("{ENTRIES}?phrase=house&source=eng-latn&target=fra-latn&translation=maison"),
    )
    .await;

    assert_eq!(status, StatusCode::NO_CONTENT, "delete by natural key must return 204");
}


#[sqlx::test]
async fn delete_by_surrogate_key_returns_204(pool: PgPool) {
    let (_, dictionary) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let phrase_id = dictionary["phrase"]["id"].as_str().unwrap();
    let translation_id = dictionary["translation"]["id"].as_str().unwrap();

    let status = delete(&pool, &format!("{ENTRIES}/{phrase_id}/{translation_id}")).await;
    assert_eq!(status, StatusCode::NO_CONTENT, "delete by surrogate key must return 204");
}


#[sqlx::test]
async fn delete_last_referencing_entry_deletes_orphaned_translation(pool: PgPool) {
    let (_, dictionary) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let translation_id = dictionary["translation"]["id"].as_str().unwrap();

    delete(
        &pool,
        &format!("{ENTRIES}?phrase=house&source=eng-latn&target=fra-latn&translation=maison"),
    )
    .await;

    // Translation row must be gone
    let translation_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM translation WHERE id = $1"
    )
    .bind(uuid::Uuid::parse_str(translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();

    assert_eq!(
        translation_count, 0,
        "orphaned Translation must be deleted after the last Dictionary is removed (E-05, DATA-DICT-003)"
    );
}


#[sqlx::test]
async fn delete_one_entry_preserves_shared_translation(pool: PgPool) {
    // Two words pointing at the same translation (maison/fra-latn)
    let (_, entry1) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    post(&pool, ENTRIES, json!({
        "phrase": "home",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let translation_id = entry1["translation"]["id"].as_str().unwrap();

    // Delete the first dictionary (house → maison)
    delete(
        &pool,
        &format!("{ENTRIES}?phrase=house&source=eng-latn&target=fra-latn&translation=maison"),
    )
    .await;

    // Translation must still exist (home → maison still references it)
    let translation_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM translation WHERE id = $1"
    )
    .bind(uuid::Uuid::parse_str(translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();

    assert_eq!(
        translation_count, 1,
        "Translation must survive while another Dictionary references it (E-05, DATA-DICT-003 criterion 1)"
    );
}


#[sqlx::test]
async fn delete_dictionary_word_survives_at_zero_entries(pool: PgPool) {
    let (_, dictionary) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let phrase_id = dictionary["phrase"]["id"].as_str().unwrap();

    delete(
        &pool,
        &format!("{ENTRIES}?phrase=house&source=eng-latn&target=fra-latn&translation=maison"),
    )
    .await;

    let word_count: i64 = sqlx::query_scalar("SELECT count(*) FROM phrase WHERE id = $1")
        .bind(uuid::Uuid::parse_str(phrase_id).unwrap())
        .fetch_one(&pool)
        .await
        .unwrap();

    assert_eq!(word_count, 1, "Phrase must survive at zero dictionaries (E-06)");
}


#[sqlx::test]
async fn delete_dictionary_cascades_dictionary_tags(pool: PgPool) {
    let (_, dictionary) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun", "adjective"]
    }))
    .await;

    let phrase_id = dictionary["phrase"]["id"].as_str().unwrap();
    let translation_id = dictionary["translation"]["id"].as_str().unwrap();

    // Verify tags exist before delete
    let tag_count_before: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM dictionary_tag WHERE phrase_id = $1 AND translation_id = $2"
    )
    .bind(uuid::Uuid::parse_str(phrase_id).unwrap())
    .bind(uuid::Uuid::parse_str(translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(tag_count_before, 2, "2 dictionary_tag rows must exist before delete");

    delete(
        &pool,
        &format!("{ENTRIES}?phrase=house&source=eng-latn&target=fra-latn&translation=maison"),
    )
    .await;

    let tag_count_after: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM dictionary_tag WHERE phrase_id = $1 AND translation_id = $2"
    )
    .bind(uuid::Uuid::parse_str(phrase_id).unwrap())
    .bind(uuid::Uuid::parse_str(translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(
        tag_count_after, 0,
        "dictionary_tag rows must be cascade-deleted with the dictionary (DATA-DICT-008)"
    );

    // grammar_tag vocabulary rows must be untouched
    let vocab_count: i64 = sqlx::query_scalar("SELECT count(*) FROM grammar_tag")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(vocab_count, 4, "grammar_tag vocabulary rows must not be deleted");
}

/// Delete by natural key returns 404 for unknown dictionary. FR-API-004.
#[sqlx::test]
async fn delete_by_natural_key_not_found_returns_404(pool: PgPool) {
    let status = delete(
        &pool,
        &format!("{ENTRIES}?phrase=no_such_word&source=eng-latn&target=fra-latn&translation=no_such_trans"),
    )
    .await;
    assert_eq!(status, StatusCode::NOT_FOUND, "delete of unknown dictionary must return 404");
}

/// Delete by surrogate key returns 404 for non-existent IDs. FR-API-009.
#[sqlx::test]
async fn delete_by_surrogate_key_not_found_returns_404(pool: PgPool) {
    let fake_id = uuid::Uuid::new_v4();
    let status = delete(&pool, &format!("{ENTRIES}/{fake_id}/{fake_id}")).await;
    assert_eq!(status, StatusCode::NOT_FOUND, "delete with unknown UUIDs must return 404");
}


#[sqlx::test]
async fn delete_natural_key_and_surrogate_key_identical_behavior(pool: PgPool) {
    // Dictionary A: will delete by natural key (no need to capture the response body)
    post(&pool, ENTRIES, json!({
        "phrase": "alpha",
        "source": "eng-latn",
        "translation": "alpha-fr",
        "target": "fra-latn"
    }))
    .await;

    // Dictionary B: will delete by surrogate key
    let (_, entry_b) = post(&pool, ENTRIES, json!({
        "phrase": "beta",
        "source": "eng-latn",
        "translation": "beta-fr",
        "target": "fra-latn"
    }))
    .await;

    // Delete A by natural key
    let status_a = delete(
        &pool,
        &format!("{ENTRIES}?phrase=alpha&source=eng-latn&target=fra-latn&translation=alpha-fr"),
    )
    .await;

    // Delete B by surrogate key
    let phrase_id_b = entry_b["phrase"]["id"].as_str().unwrap();
    let trans_id_b = entry_b["translation"]["id"].as_str().unwrap();
    let status_b = delete(&pool, &format!("{ENTRIES}/{phrase_id_b}/{trans_id_b}")).await;

    assert_eq!(status_a, StatusCode::NO_CONTENT);
    assert_eq!(status_b, StatusCode::NO_CONTENT);

    // Both dictionaries gone, both translations orphaned and deleted
    let entry_count: i64 = sqlx::query_scalar("SELECT count(*) FROM dictionary")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(entry_count, 0);
    let translation_count: i64 = sqlx::query_scalar("SELECT count(*) FROM translation")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(translation_count, 0);
}


#[sqlx::test]
async fn relink_by_natural_key_happy_path(pool: PgPool) {
    let (_, original) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let old_translation_id = original["translation"]["id"].as_str().unwrap();

    let (status, body) = put(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "target": "fra-latn",
        "current_translation": "maison",
        "new_translation": "demeure"
    }))
    .await;

    assert_eq!(status, StatusCode::OK, "relink by natural key must return 200");
    assert_eq!(body["translation"]["text"], "demeure");
    // E-09: language-script inherited from old translation
    assert_eq!(body["translation"]["language_script"], "fra-latn");
    // Old translation must be orphaned → deleted
    let old_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM translation WHERE id = $1"
    )
    .bind(uuid::Uuid::parse_str(old_translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(old_count, 0, "orphaned old translation must be deleted after relink");
}


#[sqlx::test]
async fn relink_by_surrogate_key_happy_path(pool: PgPool) {
    let (_, original) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let phrase_id = original["phrase"]["id"].as_str().unwrap();
    let translation_id = original["translation"]["id"].as_str().unwrap();

    let (status, body) = put(
        &pool,
        &format!("{ENTRIES}/{phrase_id}/{translation_id}"),
        json!({ "new_translation": "demeure" }),
    )
    .await;

    assert_eq!(status, StatusCode::OK, "relink by surrogate key must return 200");
    assert_eq!(body["translation"]["text"], "demeure");
    assert_eq!(body["translation"]["language_script"], "fra-latn", "E-09: language inherited");
}


#[sqlx::test]
async fn relink_preserves_grammar_tags(pool: PgPool) {
    let (_, original) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun", "adjective"]
    }))
    .await;

    let phrase_id = original["phrase"]["id"].as_str().unwrap();
    let translation_id = original["translation"]["id"].as_str().unwrap();

    let (status, body) = put(
        &pool,
        &format!("{ENTRIES}/{phrase_id}/{translation_id}"),
        json!({ "new_translation": "demeure" }),
    )
    .await;

    assert_eq!(status, StatusCode::OK);

    let mut tags: Vec<&str> = body["tags"].as_array().unwrap().iter()
        .map(|v| v.as_str().unwrap())
        .collect();
    tags.sort();

    assert_eq!(
        tags, vec!["adjective", "noun"],
        "relinked dictionary must carry the same tags as the original (E-18, HITL-8)"
    );
}


#[sqlx::test]
async fn relink_already_linked_target_returns_409(pool: PgPool) {
    // Dictionary A: house → maison
    let (_, entry_a) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun"]
    }))
    .await;

    // Dictionary B: house → demeure (same phrase, different translation)
    post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "demeure",
        "target": "fra-latn"
    }))
    .await;

    let phrase_id = entry_a["phrase"]["id"].as_str().unwrap();
    let translation_id_a = entry_a["translation"]["id"].as_str().unwrap();

    // Attempt to relink dictionary A (house → maison) to demeure, which house already links to
    let (status, body) = put(
        &pool,
        &format!("{ENTRIES}/{phrase_id}/{translation_id_a}"),
        json!({ "new_translation": "demeure" }),
    )
    .await;

    assert_eq!(status, StatusCode::CONFLICT, "relink to already-linked target must return 409 (E-08)");
    assert_eq!(body["error"]["code"], "conflict");

    // Original Dictionary (house → maison) must still exist with its tag intact
    let entry_still_exists: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM dictionary WHERE phrase_id = $1 AND translation_id = $2"
    )
    .bind(uuid::Uuid::parse_str(phrase_id).unwrap())
    .bind(uuid::Uuid::parse_str(translation_id_a).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(entry_still_exists, 1, "original dictionary must survive a failed relink (E-08)");

    let tag_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM dictionary_tag WHERE phrase_id = $1 AND translation_id = $2"
    )
    .bind(uuid::Uuid::parse_str(phrase_id).unwrap())
    .bind(uuid::Uuid::parse_str(translation_id_a).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(tag_count, 1, "dictionary tags must survive a failed relink (E-08)");
}


#[sqlx::test]
async fn relink_identical_target_noop_200(pool: PgPool) {
    let (_, original) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["noun"]
    }))
    .await;

    let phrase_id = original["phrase"]["id"].as_str().unwrap();
    let translation_id = original["translation"]["id"].as_str().unwrap();

    // Relink to "maison" which is the current translation (identical target)
    let (status, body) = put(
        &pool,
        &format!("{ENTRIES}/{phrase_id}/{translation_id}"),
        json!({ "new_translation": "maison" }),
    )
    .await;

    assert_eq!(status, StatusCode::OK, "identical-target relink must return 200 unchanged");
    assert_eq!(body["translation"]["text"], "maison");
    assert_eq!(body["translation"]["id"], translation_id, "translation id must be unchanged");
    assert_eq!(body["tags"], json!(["noun"]), "tags must be unchanged on no-op relink");
}


#[sqlx::test]
async fn relink_not_found_returns_404(pool: PgPool) {
    let fake_id = uuid::Uuid::new_v4();

    let (status, body) = put(
        &pool,
        &format!("{ENTRIES}/{fake_id}/{fake_id}"),
        json!({ "new_translation": "something" }),
    )
    .await;

    assert_eq!(status, StatusCode::NOT_FOUND, "relink with unknown IDs must return 404");
    assert_eq!(body["error"]["code"], "not_found");
}


#[sqlx::test]
async fn relink_keeps_shared_translation_alive(pool: PgPool) {
    // Two dictionaries sharing "maison": house → maison, home → maison
    let (_, entry_house) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    post(&pool, ENTRIES, json!({
        "phrase": "home",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    let phrase_id = entry_house["phrase"]["id"].as_str().unwrap();
    let translation_id = entry_house["translation"]["id"].as_str().unwrap();

    // Relink house → demeure (orphans maison only from house, not from home)
    put(
        &pool,
        &format!("{ENTRIES}/{phrase_id}/{translation_id}"),
        json!({ "new_translation": "demeure" }),
    )
    .await;

    let maison_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM translation WHERE id = $1"
    )
    .bind(uuid::Uuid::parse_str(translation_id).unwrap())
    .fetch_one(&pool)
    .await
    .unwrap();

    assert_eq!(
        maison_count, 1,
        "shared translation must survive relink while still referenced by another dictionary (DATA-DICT-003)"
    );
}


#[sqlx::test]
async fn relink_natural_key_and_surrogate_key_identical_behavior(pool: PgPool) {
    // Dictionary A relinked by natural key
    post(&pool, ENTRIES, json!({
        "phrase": "alpha",
        "source": "eng-latn",
        "translation": "alpha-fr",
        "target": "fra-latn",
        "tags": ["noun"]
    }))
    .await;

    let (status_a, body_a) = put(&pool, ENTRIES, json!({
        "phrase": "alpha",
        "source": "eng-latn",
        "target": "fra-latn",
        "current_translation": "alpha-fr",
        "new_translation": "alpha-new-fr"
    }))
    .await;

    // Dictionary B relinked by surrogate key
    let (_, entry_b) = post(&pool, ENTRIES, json!({
        "phrase": "beta",
        "source": "eng-latn",
        "translation": "beta-fr",
        "target": "fra-latn",
        "tags": ["verb"]
    }))
    .await;

    let word_b = entry_b["phrase"]["id"].as_str().unwrap();
    let trans_b = entry_b["translation"]["id"].as_str().unwrap();

    let (status_b, body_b) = put(
        &pool,
        &format!("{ENTRIES}/{word_b}/{trans_b}"),
        json!({ "new_translation": "beta-new-fr" }),
    )
    .await;

    assert_eq!(status_a, StatusCode::OK);
    assert_eq!(status_b, StatusCode::OK);
    assert_eq!(body_a["translation"]["text"], "alpha-new-fr");
    assert_eq!(body_b["translation"]["text"], "beta-new-fr");
    // E-18: tags preserved by both schemes
    assert_eq!(body_a["tags"], json!(["noun"]));
    assert_eq!(body_b["tags"], json!(["verb"]));
}


#[tokio::test]
async fn unavailability_data_endpoints_return_503_healthz_still_200() {
    // Build a pool pointing at a closed port with a very short timeout so the test is fast.
    let bad_opts = PgConnectOptions::new()
        .host("127.0.0.1")
        .port(1) // closed port — guaranteed connection refused
        .database("testdb")
        .username("testuser")
        .password("testpass");

    let bad_pool = PgPoolOptions::new()
        .max_connections(1)
        .acquire_timeout(Duration::from_millis(500))
        .connect_lazy_with(bad_opts);

    let state = AppState { pool: bad_pool };

    // /healthz must return 200 even with Postgres unreachable (E-07 liveness half)
    let healthz_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .uri("/healthz")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(
        healthz_resp.status(),
        StatusCode::OK,
        "/healthz must return 200 when Postgres is unreachable (E-07)"
    );

    // /readyz must return 503 (E-07 readiness half)
    let readyz_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .uri("/readyz")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(
        readyz_resp.status(),
        StatusCode::SERVICE_UNAVAILABLE,
        "/readyz must return 503 when Postgres is unreachable (E-07)"
    );

    // POST /dictionaries → 503
    let create_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .method(Method::POST)
                .uri(ENTRIES)
                .header("content-type", "application/json")
                .body(Body::from(json!({
                    "phrase": "house",
                    "source": "eng-latn",
                    "translation": "maison",
                    "target": "fra-latn"
                }).to_string()))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(
        create_resp.status(),
        StatusCode::SERVICE_UNAVAILABLE,
        "POST /dictionaries must return 503 when Postgres is unreachable (E-07)"
    );
    let create_bytes = axum::body::to_bytes(create_resp.into_body(), 65536).await.unwrap();
    let create_body: Value = serde_json::from_slice(&create_bytes).unwrap();
    assert_eq!(
        create_body["error"]["code"], "service_unavailable",
        "503 body must have error.code = service_unavailable (E-07)"
    );

    // GET /dictionaries → 503
    let list_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .uri(ENTRIES)
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(list_resp.status(), StatusCode::SERVICE_UNAVAILABLE,
        "GET /dictionaries must return 503 when Postgres is unreachable");

    // GET /translations → 503
    let lookup_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .uri(&format!("{TRANSLATIONS}?phrase=test&source=eng-latn&target=fra-latn"))
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(lookup_resp.status(), StatusCode::SERVICE_UNAVAILABLE,
        "GET /translations must return 503 when Postgres is unreachable");

    // DELETE /dictionaries (natural key) → 503 (M-2: verifies DELETE verb covered by unavailability)
    let delete_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .method(Method::DELETE)
                .uri(&format!(
                    "{ENTRIES}?phrase=test&source=eng-latn\
                     &target=fra-latn&translation=test"
                ))
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(
        delete_resp.status(),
        StatusCode::SERVICE_UNAVAILABLE,
        "DELETE /dictionaries must return 503 when Postgres is unreachable (E-07, M-2)"
    );

    // PUT /dictionaries (natural key) → 503 (M-2: verifies PUT verb covered by unavailability)
    let put_resp = build_router(state.clone())
        .oneshot(
            Request::builder()
                .method(Method::PUT)
                .uri(ENTRIES)
                .header("content-type", "application/json")
                .body(Body::from(
                    json!({
                        "phrase": "test",
                        "source": "eng-latn",
                        "target": "fra-latn",
                        "current_translation": "test",
                        "new_translation": "other"
                    })
                    .to_string(),
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(
        put_resp.status(),
        StatusCode::SERVICE_UNAVAILABLE,
        "PUT /dictionaries must return 503 when Postgres is unreachable (E-07, M-2)"
    );
}


#[sqlx::test]
async fn extensibility_new_language_script_existing_script(pool: PgPool) {
    // Step 1: cmn-hant not yet inserted → must be 400 unknown_reference
    let (pre_status, pre_body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hant",  // not yet seeded
        "translation": "house",
        "target": "eng-latn"
    }))
    .await;

    assert_eq!(pre_status, StatusCode::BAD_REQUEST,
        "cmn-hant before INSERT must return 400 (validates the test is meaningful, E-10)");
    assert_eq!(pre_body["error"]["code"], "unknown_reference");

    // Step 2: INSERT cmn-hant into language_script (hant is already in script)
    sqlx::query(
        "INSERT INTO language (code, language_code, script_code) \
         VALUES ('cmn-hant', 'cmn', 'hant')"
    )
    .execute(&pool)
    .await
    .expect("inserting cmn-hant should succeed since hant is seeded");

    // Step 3: Create and lookup must now succeed without any migration or restart (E-10)
    let (create_status, create_body) = post(&pool, ENTRIES, json!({
        "phrase": "房子",
        "source": "cmn-hant",
        "translation": "house",
        "target": "eng-latn",
        "transcription": "fángzi"
    }))
    .await;

    assert_eq!(create_status, StatusCode::CREATED,
        "create must succeed after inserting language_script row (E-10)");
    assert_eq!(create_body["phrase"]["language_script"], "cmn-hant");

    let (lookup_status, lookup_body) = get(
        &pool,
        &format!("{TRANSLATIONS}?phrase=%E6%88%BF%E5%AD%90&source=cmn-hant&target=eng-latn"),
    )
    .await;

    assert_eq!(lookup_status, StatusCode::OK,
        "lookup must succeed immediately after language_script INSERT (E-10, no redeploy required)");
    assert_eq!(lookup_body["translations"].as_array().unwrap().len(), 1);
}


#[sqlx::test]
async fn extensibility_new_script_and_language_script(pool: PgPool) {
    // Verify hin-deva is rejected before any inserts
    let (pre_status, pre_body) = post(&pool, ENTRIES, json!({
        "phrase": "घर",
        "source": "hin-deva",
        "translation": "house",
        "target": "eng-latn"
    }))
    .await;
    assert_eq!(pre_status, StatusCode::BAD_REQUEST,
        "hin-deva before INSERT must be 400 (validates meaningful test, E-10)");
    assert_eq!(pre_body["error"]["code"], "unknown_reference");

    // Insert the new script first
    sqlx::query("INSERT INTO script (code) VALUES ('deva')")
        .execute(&pool)
        .await
        .expect("inserting new script 'deva' must succeed");

    // Then insert the language_script pair
    sqlx::query(
        "INSERT INTO language (code, language_code, script_code) \
         VALUES ('hin-deva', 'hin', 'deva')"
    )
    .execute(&pool)
    .await
    .expect("inserting hin-deva must succeed after deva script exists");

    // Create and lookup must succeed with no migration or restart (E-10)
    let (create_status, create_body) = post(&pool, ENTRIES, json!({
        "phrase": "घर",
        "source": "hin-deva",
        "translation": "house",
        "target": "eng-latn"
    }))
    .await;

    assert_eq!(create_status, StatusCode::CREATED,
        "create must succeed after inserting script + language_script rows (E-10)");
    assert_eq!(create_body["phrase"]["language_script"], "hin-deva");

    let (lookup_status, _) = get(
        &pool,
        // %E0%A4%98%E0%A4%B0 = URL-encoded "घर"
        &format!("{TRANSLATIONS}?phrase=%E0%A4%98%E0%A4%B0&source=hin-deva&target=eng-latn"),
    )
    .await;

    assert_eq!(lookup_status, StatusCode::OK,
        "lookup must succeed immediately after INSERT (E-10, no DDL required)");
}


#[sqlx::test]
async fn e13_unknown_source_returns_400(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "dom",
        "source": "pol-cyrl",  // well-formed but not seeded
        "translation": "house",
        "target": "eng-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST, "unknown source language-script must be 400 (E-13)");
    assert_eq!(body["error"]["code"], "unknown_reference", "error code must be unknown_reference (E-13)");

    let details = body["error"]["details"].as_array().unwrap();
    let fields: Vec<&str> = details.iter()
        .map(|d| d["field"].as_str().unwrap())
        .collect();
    assert!(
        fields.contains(&"source"),
        "details must name source field (E-13); details: {details:?}"
    );

    // All 7 tables must be unchanged (creates nothing — FR-API-001 criterion 4)
    let word_count: i64 = sqlx::query_scalar("SELECT count(*) FROM phrase")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(word_count, 0, "unknown source must not create any phrase row (E-13)");

    let translation_count: i64 = sqlx::query_scalar("SELECT count(*) FROM translation")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(translation_count, 0, "unknown source must not create any translation row (E-13)");
}


#[sqlx::test]
async fn e13_unknown_target_returns_400(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "dom",
        "target": "pol-cyrl"  // well-formed but not seeded
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST, "unknown target language-script must be 400 (E-13)");
    assert_eq!(body["error"]["code"], "unknown_reference");

    let details = body["error"]["details"].as_array().unwrap();
    let fields: Vec<&str> = details.iter()
        .map(|d| d["field"].as_str().unwrap())
        .collect();
    assert!(
        fields.contains(&"target"),
        "details must name target field (E-13)"
    );
}


#[sqlx::test]
async fn e14_unknown_tag_returns_400_and_creates_nothing(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["pronoun"]  // well-formed but not seeded
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST, "unknown tag must be 400 (E-14)");
    assert_eq!(body["error"]["code"], "unknown_reference", "error code must be unknown_reference (E-14)");

    let details = body["error"]["details"].as_array().unwrap();
    let fields: Vec<&str> = details.iter()
        .map(|d| d["field"].as_str().unwrap())
        .collect();
    assert!(
        fields.contains(&"tags"),
        "details must name the tags field (E-14); details: {details:?}"
    );

    // Creates nothing — all tables unchanged
    let entry_count: i64 = sqlx::query_scalar("SELECT count(*) FROM dictionary")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(entry_count, 0, "unknown tag must not create any dictionary row (E-14)");
    let word_count: i64 = sqlx::query_scalar("SELECT count(*) FROM phrase")
        .fetch_one(&pool).await.unwrap();
    assert_eq!(word_count, 0, "unknown tag must not create any phrase row (E-14)");
}


#[sqlx::test]
async fn e02_bare_language_code_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng",   // bare code, missing script — malformed
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST, "bare language code must return 400 (E-02)");
    assert_eq!(body["error"]["code"], "validation_failed", "error code must be validation_failed (E-02)");
}


#[sqlx::test]
async fn e02_wrong_separator_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng_latn",  // underscore, not hyphen
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn e02_short_script_code_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-lat",  // script part only 3 letters
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn e02_uppercase_language_script_normalised_and_accepted(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "ENG-LATN",  // normalised to eng-latn
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::CREATED,
        "uppercase language-script must be normalised and accepted (E-02)");
    assert_eq!(body["phrase"]["language_script"], "eng-latn",
        "normalised language-script must appear in response as lowercase");
}


#[sqlx::test]
async fn blank_phrase_text_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn whitespace_phrase_text_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "   ",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn non_uuid_path_segment_returns_400_validation_failed(pool: PgPool) {
    // DELETE /dictionaries/not-a-uuid/also-not-a-uuid
    let status = delete(&pool, &format!("{ENTRIES}/not-a-uuid/also-not-a-uuid")).await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "non-UUID path segment must return 400 (E-02)");

    // PUT /dictionaries/not-a-uuid/also-not-a-uuid
    let (put_status, put_body) = put(
        &pool,
        &format!("{ENTRIES}/not-a-uuid/also-not-a-uuid"),
        json!({ "new_translation": "something" }),
    )
    .await;
    assert_eq!(put_status, StatusCode::BAD_REQUEST);
    assert_eq!(put_body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn missing_required_field_returns_400_validation_failed(pool: PgPool) {
    // Missing translation field
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn"
        // target and translation missing
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn blank_transcription_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "transcription": "   "  // blank transcription
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert_eq!(body["error"]["code"], "validation_failed");
}


#[sqlx::test]
async fn e02_validation_failed_wire_shape_includes_details_with_field_name(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng",   // bare code, not xxx-xxxx — malformed (E-02)
        "translation": "maison",
        "target": "fra-latn"
    }))
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST, "bare language code must return 400 (E-02)");
    assert_eq!(
        body["error"]["code"], "validation_failed",
        "error code must be validation_failed (E-02)"
    );

    // Wire-shape: details must be present and non-empty (E-02 format errors always populate it)
    let details = body["error"]["details"].as_array()
        .expect("error.details must be a JSON array for validation_failed responses (E-02)");
    assert!(
        !details.is_empty(),
        "error.details must be a non-empty array for E-02 validation errors; got: {details:?}"
    );

    // Each dictionary must carry a non-empty field name — for JsonRejection-path E-02 errors
    // the field is "body" (the deserialization container), matching the API's wire contract.
    let fields: Vec<&str> = details.iter()
        .map(|d| d["field"].as_str().unwrap())
        .collect();
    assert!(
        fields.contains(&"body"),
        "details must name the offending field as 'body' for body-level format errors (E-02); \
         details: {details:?}"
    );
}


#[sqlx::test]
async fn uppercase_tag_returns_400_validation_failed(pool: PgPool) {
    let (status, body) = post(&pool, ENTRIES, json!({
        "phrase": "house",
        "source": "eng-latn",
        "translation": "maison",
        "target": "fra-latn",
        "tags": ["Noun"]   // uppercase N — must be rejected (SPECS §10, GrammarTag fix)
    }))
    .await;

    assert_eq!(
        status,
        StatusCode::BAD_REQUEST,
        "uppercase tag must return 400 (GrammarTag uppercase-rejection, SPECS §10)"
    );
    assert_eq!(
        body["error"]["code"], "validation_failed",
        "error code must be validation_failed for uppercase tag (SPECS §10)"
    );
}
