package users_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"assistant/internal/dbtest"
	"assistant/internal/users"
)

func newStore(t *testing.T) *users.Store {
	t.Helper()
	return users.NewStore(dbtest.Pool(t))
}

func int64p(v int64) *int64 { return &v }

func TestCreateAndGet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	created, err := s.Create(ctx, "  Janusz  ", int64p(123456789012))
	if err != nil {
		t.Fatal(err)
	}
	if !users.ValidID(created.ID) || created.DisplayName != "Janusz" || created.TelegramID == nil || *created.TelegramID != 123456789012 {
		t.Errorf("created = %+v", created)
	}
	if time.Since(created.CreatedAt) > time.Minute || created.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("created_at = %v", created.CreatedAt)
	}
	got, err := s.Get(ctx, created.ID)
	if err != nil || got.ID != created.ID || got.DisplayName != "Janusz" {
		t.Errorf("get = %+v, %v", got, err)
	}
}

func TestCreateWithoutTelegramID(t *testing.T) {
	s := newStore(t)
	u, err := s.Create(context.Background(), "Ada", nil)
	if err != nil || u.TelegramID != nil {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestCreateRejectsABlankName(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := s.Create(context.Background(), name, nil); !errors.Is(err, users.ErrBlankName) {
			t.Errorf("%q: err = %v", name, err)
		}
	}
}

func TestTelegramIDMustBeUnique(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "First", int64p(7)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Second", int64p(7)); !errors.Is(err, users.ErrTelegramIDTaken) {
		t.Errorf("err = %v", err)
	}
	if _, err := s.Create(ctx, "Third", nil); err != nil {
		t.Errorf("users without a telegram id never clash: %v", err)
	}
}

func TestGetUnknownAndMalformedIDs(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"", "nope", "11111111-1111-1111-1111-111111111111", "11111111-1111-1111-1111-11111111111", "zzzzzzzz-1111-1111-1111-111111111111", "' OR 1=1 --"} {
		if _, err := s.Get(context.Background(), id); !errors.Is(err, users.ErrNotFound) {
			t.Errorf("%q: err = %v", id, err)
		}
	}
}

func TestListIsOldestFirstAndNeverNil(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	empty, err := s.List(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, %v", empty, err)
	}
	for _, name := range []string{"One", "Two", "Three"} {
		if _, err := s.Create(ctx, name, nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %v, %v", list, err)
	}
	var names []string
	for _, u := range list {
		names = append(names, u.DisplayName)
	}
	if strings.Join(names, ",") != "One,Two,Three" {
		t.Errorf("order = %v", names)
	}
}

func TestValidID(t *testing.T) {
	good := []string{"123e4567-e89b-12d3-a456-426614174000", "ABCDEF01-2345-6789-abcd-ef0123456789"}
	bad := []string{"", "123e4567e89b12d3a456426614174000", "123e4567-e89b-12d3-a456-42661417400", "123e4567-e89b-12d3-a456-4266141740000", "g23e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456_426614174000"}
	for _, s := range good {
		if !users.ValidID(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if users.ValidID(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
