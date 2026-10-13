package vocabulary

import (
	"context"
	"errors"
	"testing"
)

func TestHideIsPerUserAndPerPhraseAcrossLists(t *testing.T) {
	s, pool, alice := newTestStore(t)
	ctx := context.Background()
	var bob int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ('bob', 'x') RETURNING id`).Scan(&bob); err != nil {
		t.Fatal(err)
	}
	a := newList(t, s, alice, "A", "arb", "arab")
	b := newList(t, s, alice, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "n", "kitab")
	mustAdd(t, s, a, "قلم", "n", "qalam")
	posB := mustAdd(t, s, b, "كتاب", "n", "kitab")

	if err := s.Hide(ctx, alice, a, posA); err != nil {
		t.Fatal(err)
	}
	if err := s.Hide(ctx, alice, a, posA); err != nil {
		t.Fatalf("hiding twice must not fail: %v", err)
	}

	itemsB, _ := s.Items(ctx, b)
	hiddenAlice, err := s.HiddenPhraseIDs(ctx, alice, []int64{itemsB[posB-itemsB[0].Position].PhraseID})
	if err != nil {
		t.Fatal(err)
	}
	if len(hiddenAlice) != 1 {
		t.Errorf("alice: phrase hidden in list A must be hidden in list B too, got %v", hiddenAlice)
	}
	itemsA, _ := s.Items(ctx, a)
	var ids []int64
	for _, it := range itemsA {
		ids = append(ids, it.PhraseID)
	}
	if hidden, _ := s.HiddenPhraseIDs(ctx, alice, ids); len(hidden) != 1 {
		t.Errorf("alice: only one of two phrases is hidden, got %v", hidden)
	}
	if hidden, _ := s.HiddenPhraseIDs(ctx, bob, ids); len(hidden) != 0 {
		t.Errorf("bob must not be affected by alice's Hide, got %v", hidden)
	}
	if n := count(t, pool, "vocabulary_hidden_phrases"); n != 1 {
		t.Errorf("rows = %d, want 1 (idempotent)", n)
	}

	if err := s.Unhide(ctx, alice, b, posB); err != nil {
		t.Fatal(err)
	}
	if err := s.Unhide(ctx, alice, b, posB); err != nil {
		t.Fatalf("unhiding twice must not fail: %v", err)
	}
	if hidden, _ := s.HiddenPhraseIDs(ctx, alice, ids); len(hidden) != 0 {
		t.Errorf("after Unhide nothing is hidden, got %v", hidden)
	}
}

func TestHideUnknownItemIsNotFound(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	l := newList(t, s, uid, "A", "arb", "arab")
	if err := s.Hide(ctx, uid, l, 99); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Hide of a missing item = %v, want ErrItemNotFound", err)
	}
	if err := s.Unhide(ctx, uid, l, 99); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Unhide of a missing item = %v, want ErrItemNotFound", err)
	}
}

func TestHiddenRowsGoWithThePhraseAndTheUser(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	l := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, l, "كتاب", "n", "kitab")
	if err := s.Hide(ctx, uid, l, pos); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, l, pos); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "vocabulary_hidden_phrases"); n != 0 {
		t.Errorf("hidden row survived its phrase: %d", n)
	}
	pos = mustAdd(t, s, l, "قلم", "n", "qalam")
	if err := s.Hide(ctx, uid, l, pos); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "vocabulary_hidden_phrases"); n != 0 {
		t.Errorf("hidden row survived its user: %d", n)
	}
}
